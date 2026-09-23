// Package logger captures process log lines into a local veclite store and
// supports keyword search via shared-read for CLI tools.
//
// Architecture: logs capture holds the writer lock; CLI tools (search, export)
// open the same database read-only with shared read-lock so concurrent queries
// do not block capture. External processes can call Reload() (or POST to the
// TUI's HTTP /reload endpoint) to nudge readers to re-query.
package logger

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	veclite "github.com/abdul-hamid-achik/veclite"
)

// Entry is one captured log line.
type Entry struct {
	Timestamp time.Time `json:"timestamp"`
	PID       int32     `json:"pid"`
	Process   string    `json:"process"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	Raw       string    `json:"raw"`
}

// Store is a veclite-backed log store.
type Store struct {
	mu        sync.Mutex
	db        *veclite.DB
	collName  string
	path      string
	retention RetentionPolicy
	lastSweep time.Time

	// pendingWrites / lastSync / syncCount implement the bounded flush policy
	// described on maybeSyncLocked.
	pendingWrites int
	lastSync      time.Time
	syncCount     int

	// stopFlusher / flusherDone implement the background flusher started by
	// OpenStoreWithRetention (see startFlusher's doc comment). Both are nil
	// on a read-only store, which has nothing to flush.
	stopFlusher chan struct{}
	flusherDone chan struct{}
}

const (
	defaultCollection = "logs"

	// DefaultMaxRecords bounds the durable log store even for long-running
	// capture sessions. FIFO eviction keeps the newest observations.
	DefaultMaxRecords = 100_000
	// DefaultMaxAge is how long captured log entries are retained.
	DefaultMaxAge = 7 * 24 * time.Hour
	// DefaultSearchLimit is used when callers omit a positive limit.
	DefaultSearchLimit = 50
	// MaxSearchLimit prevents a malformed or agent-generated query from
	// returning an unbounded response.
	MaxSearchLimit = 1_000

	defaultSweepInterval = 5 * time.Minute

	// DefaultSyncEvery / DefaultSyncInterval bound how many captured lines can
	// accumulate in memory before Append durably flushes them, whichever
	// bound is crossed first. See maybeSyncLocked.
	DefaultSyncEvery    = 1_000
	DefaultSyncInterval = 5 * time.Second
)

// RetentionPolicy bounds a writer by record age, count, and flush cadence.
// Sweeps run inside the Store writer mutex; no background goroutine mutates
// the database, so read-only shared readers retain their point-in-time
// snapshot semantics.
type RetentionPolicy struct {
	MaxAge        time.Duration
	MaxRecords    int
	SweepInterval time.Duration
	// SyncEvery / SyncInterval bound how long captured lines can sit
	// unflushed; see maybeSyncLocked. Zero or negative selects the defaults.
	SyncEvery    int
	SyncInterval time.Duration
}

func defaultRetentionPolicy() RetentionPolicy {
	return RetentionPolicy{
		MaxAge:        DefaultMaxAge,
		MaxRecords:    DefaultMaxRecords,
		SweepInterval: defaultSweepInterval,
		SyncEvery:     DefaultSyncEvery,
		SyncInterval:  DefaultSyncInterval,
	}
}

// StorePathEnv overrides the default log database for every logs command.
// A command's explicit --store flag takes precedence over this variable.
const StorePathEnv = "MONITOR_LOG_STORE"

// DefaultPath returns Monitor's durable per-user log store, creating only its
// parent directory. Log capture used to write under $TMPDIR, where OS cleanup
// could silently erase the database between sessions.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	dir := filepath.Join(home, ".local", "share", "monitor")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create log store directory: %w", err)
	}
	// MkdirAll preserves an existing directory's mode. Tighten it explicitly
	// because captured logs can contain credentials and other sensitive data.
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("secure log store directory: %w", err)
	}
	return filepath.Join(dir, "logs.veclite"), nil
}

// ResolvePath applies the shared log-store precedence: an explicit path,
// MONITOR_LOG_STORE, then DefaultPath. Relative paths are made absolute and a
// leading ~/ is expanded, making the path printed by capture unambiguous.
func ResolvePath(override string) (string, error) {
	path := strings.TrimSpace(override)
	if path == "" {
		path = strings.TrimSpace(os.Getenv(StorePathEnv))
	}
	if path == "" {
		return DefaultPath()
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand log store path: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve log store path: %w", err)
	}
	return abs, nil
}

// OpenStore opens (and creates) a log store at path. The writer does NOT use
// shared-read; only readers do.
func OpenStore(path string) (*Store, error) {
	return OpenStoreWithRetention(path, defaultRetentionPolicy())
}

// OpenStoreWithRetention opens a writer with an explicit retention policy.
// Zero or negative fields use the safe defaults.
func OpenStoreWithRetention(path string, retention RetentionPolicy) (*Store, error) {
	retention = normalizeRetentionPolicy(retention)
	db, err := veclite.Open(path)
	if err != nil {
		return nil, err
	}
	s := &Store{
		db:        db,
		path:      path,
		collName:  defaultCollection,
		retention: retention,
		lastSync:  time.Now(),
	}
	if err := s.ensureCollection(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := s.applyRetention(time.Now(), true); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply log retention: %w", err)
	}
	s.startFlusher()
	return s, nil
}

// OpenReadOnly opens the store in read-only shared mode for CLI search.
func OpenReadOnly(path string) (*Store, error) {
	db, err := veclite.Open(path,
		veclite.WithReadOnly(true),
		veclite.WithSharedRead(true),
	)
	if err != nil {
		return nil, err
	}
	return &Store{
		db:        db,
		path:      path,
		collName:  defaultCollection,
		retention: defaultRetentionPolicy(),
	}, nil
}

func (s *Store) ensureCollection() error {
	if s.db == nil {
		return errors.New("store not open")
	}
	if s.db.HasCollection(defaultCollection) {
		return nil
	}
	// Deliberately created WITHOUT veclite.WithMemoryLimits. veclite@v0.22.1's
	// enforceMemoryLimitIfConfigured runs on every single InsertTextDocument*
	// call for a collection created with a MemoryConfig (collection.go:172-176),
	// which evicts min(count-MaxRecords, EvictionBatchSize) records — 1, the
	// first time the cap is reached — via a full sorted scan of the collection
	// EVERY insert once at capacity (cleanup.go:221-233,244-274). That pins the
	// count at MaxRecords forever and defeats enforceRecordLimit's batching
	// below: its 10%-overflow threshold never fires because veclite's own
	// per-insert eviction never lets the collection grow past the cap in the
	// first place. Letting enforceRecordLimit (Go-level state, unconditional
	// on every Append) be the ONLY enforcement is what makes eviction actually
	// batched, in the same session that creates the store, not only after a
	// reopen.
	_, err := s.db.CreateCollection(defaultCollection)
	return err
}

func normalizeRetentionPolicy(policy RetentionPolicy) RetentionPolicy {
	defaults := defaultRetentionPolicy()
	if policy.MaxAge <= 0 {
		policy.MaxAge = defaults.MaxAge
	}
	if policy.MaxRecords <= 0 {
		policy.MaxRecords = defaults.MaxRecords
	}
	if policy.SweepInterval <= 0 {
		policy.SweepInterval = defaults.SweepInterval
	}
	if policy.SyncEvery <= 0 {
		policy.SyncEvery = defaults.SyncEvery
	}
	if policy.SyncInterval <= 0 {
		policy.SyncInterval = defaults.SyncInterval
	}
	return policy
}

// Append writes one entry as a text document with payload metadata.
func (s *Store) Append(e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return errors.New("store not open")
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	expiresAt := e.Timestamp.Add(s.retention.MaxAge)
	if !expiresAt.After(time.Now()) {
		// Do not briefly admit an already-expired line between scheduled
		// sweeps (for example when importing an old log file).
		return nil
	}
	coll := s.db.Collection(defaultCollection)
	payload := map[string]any{
		"timestamp": e.Timestamp,
		"pid":       e.PID,
		"process":   e.Process,
		"level":     e.Level,
		"raw":       e.Raw,
	}
	_, err := coll.InsertTextDocumentWithOptions(
		e.Message,
		payload,
		veclite.WithExpiresAt(expiresAt),
	)
	if err != nil {
		return err
	}
	if _, err := s.applyRetention(time.Now(), false); err != nil {
		return err
	}
	return s.maybeSyncLocked()
}

// applyRetention removes expired/legacy-old entries and enforces FIFO record
// bounds. It runs only on writers while Store.mu is held. It no longer syncs
// on its own; Append's call to maybeSyncLocked is the single place that
// decides when to durably flush (see that method's doc comment for why).
func (s *Store) applyRetention(now time.Time, force bool) (int, error) {
	if !force && !s.lastSweep.IsZero() && now.Sub(s.lastSweep) < s.retention.SweepInterval {
		return s.enforceRecordLimit()
	}
	s.lastSweep = now

	coll := s.db.Collection(defaultCollection)
	deleted, err := coll.CleanupExpired()
	if err != nil {
		return 0, err
	}

	// Records written before retention was introduced do not have ExpiresAt.
	// Migrate them lazily based on their captured timestamp (or insertion time
	// if legacy payload metadata is missing).
	cutoff := now.Add(-s.retention.MaxAge)
	records, err := coll.Find()
	if err != nil {
		return deleted, err
	}
	for _, rec := range records {
		if rec == nil || !recordTime(rec).Before(cutoff) {
			continue
		}
		if err := coll.Delete(rec.ID); err != nil {
			return deleted, err
		}
		deleted++
	}

	evicted, err := s.enforceRecordLimit()
	deleted += evicted
	return deleted, err
}

// enforceRecordLimit re-applies the MaxRecords cap, batching the (expensive,
// O(n log n)) sorted eviction pass so it runs roughly once every ~10% of
// MaxRecords rather than once per captured line once the store is at
// capacity. It no longer syncs directly — see maybeSyncLocked.
func (s *Store) enforceRecordLimit() (int, error) {
	coll := s.db.Collection(defaultCollection)
	count := coll.Count()
	overflow := count - s.retention.MaxRecords
	if overflow < evictionBatchThreshold(s.retention.MaxRecords) {
		return 0, nil
	}
	evicted := coll.EnforceMemoryLimit(veclite.MemoryConfig{
		MaxRecords:        s.retention.MaxRecords,
		EvictionPolicy:    "fifo",
		EvictionBatchSize: overflow,
	})
	return evicted, nil
}

// evictionBatchThreshold is how far a collection may grow past its cap before
// enforceRecordLimit pays for a sorted eviction pass, as a fraction of the cap
// (minimum 1 record). See the identical helper and its longer rationale in
// internal/history/store.go — logger and history share the same batching
// strategy.
func evictionBatchThreshold(max int) int {
	b := max / 10
	if b < 1 {
		b = 1
	}
	return b
}

// maybeSyncLocked durably flushes the store once SyncEvery Append calls have
// accumulated or SyncInterval of wall-clock time has passed since the last
// flush, whichever comes first, then resets both counters. Must be called
// with s.mu held.
//
// Append used to call db.Sync() (a full gob-encode + fsync of the whole
// database) every time enforceRecordLimit evicted a record, which — once the
// store reached its cap — meant every single captured line paid for a full
// rewrite. Bounding the flush cadence instead trades a small, documented
// crash-loss / reader-staleness window for a large reduction in write
// amplification; Close and Sync still flush unconditionally.
func (s *Store) maybeSyncLocked() error {
	s.pendingWrites++
	if s.pendingWrites < s.retention.SyncEvery && time.Since(s.lastSync) < s.retention.SyncInterval {
		return nil
	}
	return s.syncLocked()
}

// syncLocked performs the actual flush and resets the batching counters. Must
// be called with s.mu held.
func (s *Store) syncLocked() error {
	if err := s.db.Sync(); err != nil {
		return err
	}
	s.pendingWrites = 0
	s.lastSync = time.Now()
	s.syncCount++
	return nil
}

// startFlusher launches the background goroutine that keeps a quiet writer's
// pending lines from sitting unflushed indefinitely.
//
// maybeSyncLocked's time bound (SyncInterval) is only ever CHECKED from
// inside Append — so once a captured process stops producing lines (a burst
// followed by silence, or the more extreme case of a process that logs 1000+
// lines and then goes idle before being kill -9'd), nothing calls Append
// again to notice that SyncInterval has elapsed, and the pending lines never
// reach disk: an OpenReadOnly reader sees nothing, and a crash loses
// everything since the last flush instead of at most SyncInterval. This
// ticker is the writer-side counterpart that flushes on wall-clock time
// alone, independent of whether Append is still being called.
func (s *Store) startFlusher() {
	// Create the channels as LOCAL variables and capture those (not the
	// struct fields) in the goroutine's select. stopFlusherAndWait mutates
	// s.stopFlusher/s.flusherDone under s.mu from a different goroutine; an
	// earlier version had the select read s.stopFlusher directly on every
	// loop iteration, which is an unsynchronized read racing that write. Once
	// stopFlusherAndWait had set the field to nil, the NEXT time this
	// goroutine re-entered select it could evaluate `case <-s.stopFlusher`
	// against nil — a nil channel receive that never fires — leaving only the
	// ticker case alive forever: the goroutine never returns, flusherDone
	// never closes, and Close (which waits on it) hangs permanently. Fixed
	// local captures make close(stop) always reach the exact channel this
	// goroutine is actually blocked on, independent of anything the fields
	// are mutated to afterward.
	stop := make(chan struct{})
	done := make(chan struct{})
	s.stopFlusher = stop
	s.flusherDone = done
	interval := s.retention.SyncInterval
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				s.mu.Lock()
				if s.db != nil && s.pendingWrites > 0 {
					_ = s.syncLocked()
				}
				s.mu.Unlock()
			}
		}
	}()
}

// stopFlusherAndWait signals startFlusher's goroutine to stop and blocks
// until it has. Must be called WITHOUT s.mu held (the goroutine takes s.mu
// itself on every tick), and before Close tears down s.db.
func (s *Store) stopFlusherAndWait() {
	s.mu.Lock()
	stop := s.stopFlusher
	done := s.flusherDone
	s.stopFlusher = nil
	s.flusherDone = nil
	s.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	<-done
}

// Sync flushes pending writes to disk immediately, ignoring the batching
// policy in maybeSyncLocked. Callers that need a captured line visible to a
// concurrent OpenReadOnly reader sooner than SyncInterval — or that are about
// to stop, e.g. on SIGTERM/SIGINT — should call this directly.
func (s *Store) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	return s.syncLocked()
}

// SyncCount returns how many times Sync has actually flushed to disk (via
// either the batching policy or an explicit Sync call). Exposed for tests and
// diagnostics that need to observe write amplification, not for callers that
// need durability guarantees.
func (s *Store) SyncCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syncCount
}

func recordTime(rec *veclite.Record) time.Time {
	entryTime := entryFromRecord(rec).Timestamp
	if !entryTime.IsZero() {
		return entryTime
	}
	return rec.CreatedAt
}

// SearchOptions narrows a log search. Empty filter fields match everything.
type SearchOptions struct {
	Query   string
	Limit   int
	Levels  []string
	Process string
	PID     int32
	Since   time.Time
	Until   time.Time
}

// Search scans records whose message or raw payload contains the query and
// preserves the original API for callers that only need keyword + limit.
func (s *Store) Search(query string, limit int) ([]Entry, error) {
	return s.SearchWithOptions(SearchOptions{Query: query, Limit: limit})
}

// SearchWithOptions performs a case-insensitive substring search, applies
// metadata/time filters, returns newest-first, and caps at Limit (default 50).
// veclite's high-level SearchText requires an embedder, so this uses a full
// linear scan over Find(). Locked so db access is atomic with Append/Close.
func (s *Store) SearchWithOptions(opts SearchOptions) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, errors.New("store not open")
	}
	if opts.Limit <= 0 {
		opts.Limit = DefaultSearchLimit
	} else if opts.Limit > MaxSearchLimit {
		opts.Limit = MaxSearchLimit
	}
	// A read-only search may legitimately open before the first capture has
	// created the collection. Treat that as an empty store instead of asking
	// veclite for a nil collection (which would panic in Find).
	if !s.db.HasCollection(defaultCollection) {
		return []Entry{}, nil
	}
	coll := s.db.Collection(defaultCollection)
	res, err := coll.Find()
	if err != nil {
		return nil, err
	}
	// Find() iterates veclite's record map in arbitrary (non-insertion)
	// order, so collect ALL matches, sort newest-first, then truncate.
	// Breaking at `limit` mid-scan would return a non-deterministic subset
	// that can silently drop the most recent matches. Non-nil so an empty
	// result marshals as `[]`, not `null`.
	query := strings.ToLower(opts.Query)
	process := strings.ToLower(opts.Process)
	now := time.Now()
	retentionCutoff := now.Add(-s.retention.MaxAge)
	levels := make(map[string]struct{}, len(opts.Levels))
	for _, level := range opts.Levels {
		level = strings.ToLower(strings.TrimSpace(level))
		if level != "" {
			levels[level] = struct{}{}
		}
	}
	matches := make([]Entry, 0, opts.Limit)
	for _, rec := range res {
		if rec == nil {
			continue
		}
		if (!rec.ExpiresAt.IsZero() && !rec.ExpiresAt.After(now)) || recordTime(rec).Before(retentionCutoff) {
			continue
		}
		e := entryFromRecord(rec)
		if !strings.Contains(strings.ToLower(e.Message), query) &&
			!strings.Contains(strings.ToLower(e.Raw), query) {
			continue
		}
		if opts.PID > 0 && e.PID != opts.PID {
			continue
		}
		if process != "" && !strings.Contains(strings.ToLower(e.Process), process) {
			continue
		}
		if len(levels) > 0 {
			if _, ok := levels[strings.ToLower(e.Level)]; !ok {
				continue
			}
		}
		if !opts.Since.IsZero() && e.Timestamp.Before(opts.Since) {
			continue
		}
		if !opts.Until.IsZero() && e.Timestamp.After(opts.Until) {
			continue
		}
		matches = append(matches, e)
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Timestamp.After(matches[j].Timestamp)
	})
	if len(matches) > opts.Limit {
		matches = matches[:opts.Limit]
	}
	return matches, nil
}

func entryFromRecord(rec *veclite.Record) Entry {
	e := Entry{Message: rec.Content, Raw: rec.Content}
	if rec.Payload != nil {
		if ts, ok := rec.Payload["timestamp"].(time.Time); ok {
			e.Timestamp = ts
		} else if raw, ok := rec.Payload["timestamp"].(string); ok {
			e.Timestamp, _ = time.Parse(time.RFC3339Nano, raw)
		}
		if p, ok := rec.Payload["pid"].(int32); ok {
			e.PID = p
		} else if p, ok := rec.Payload["pid"].(int); ok {
			e.PID = int32(p)
		} else if p, ok := rec.Payload["pid"].(int64); ok {
			e.PID = int32(p)
		} else if pf, ok := rec.Payload["pid"].(float64); ok {
			e.PID = int32(pf)
		}
		if p, ok := rec.Payload["process"].(string); ok {
			e.Process = p
		}
		if p, ok := rec.Payload["level"].(string); ok {
			e.Level = p
		}
		if p, ok := rec.Payload["raw"].(string); ok {
			e.Raw = p
		}
	}
	return e
}

// Close releases the underlying veclite handle. It stops the background
// flusher (see startFlusher) first, so no tick can race Close's own final
// db.Close.
func (s *Store) Close() error {
	s.stopFlusherAndWait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

// Reload is a no-op on the in-process store; the HTTP /reload endpoint
// notifies other readers that the data has changed.
func (s *Store) Reload() error { return nil }
