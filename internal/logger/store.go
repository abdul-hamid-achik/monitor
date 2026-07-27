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
)

// RetentionPolicy bounds a writer by record age and count. Sweeps run inside
// the Store writer mutex; no background goroutine mutates the database, so
// read-only shared readers retain their point-in-time snapshot semantics.
type RetentionPolicy struct {
	MaxAge        time.Duration
	MaxRecords    int
	SweepInterval time.Duration
}

func defaultRetentionPolicy() RetentionPolicy {
	return RetentionPolicy{
		MaxAge:        DefaultMaxAge,
		MaxRecords:    DefaultMaxRecords,
		SweepInterval: defaultSweepInterval,
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
	}
	if err := s.ensureCollection(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := s.applyRetention(time.Now(), true); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply log retention: %w", err)
	}
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
	_, err := s.db.CreateCollection(defaultCollection, veclite.WithMemoryLimits(veclite.MemoryConfig{
		MaxRecords:        s.retention.MaxRecords,
		EvictionPolicy:    "fifo",
		EvictionBatchSize: max(1, s.retention.MaxRecords/10),
	}))
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
	_, err = s.applyRetention(time.Now(), false)
	return err
}

// applyRetention removes expired/legacy-old entries and enforces FIFO record
// bounds. It runs only on writers while Store.mu is held. When it deletes
// anything, Sync publishes a compact atomic snapshot that new shared readers
// can open without contending with the writer.
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
	if err != nil {
		return deleted, err
	}
	if deleted > 0 {
		if err := s.db.Sync(); err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

func (s *Store) enforceRecordLimit() (int, error) {
	coll := s.db.Collection(defaultCollection)
	count := coll.Count()
	if count <= s.retention.MaxRecords {
		return 0, nil
	}
	evicted := coll.EnforceMemoryLimit(veclite.MemoryConfig{
		MaxRecords:        s.retention.MaxRecords,
		EvictionPolicy:    "fifo",
		EvictionBatchSize: count,
	})
	if evicted > 0 {
		if err := s.db.Sync(); err != nil {
			return evicted, err
		}
	}
	return evicted, nil
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

// Close releases the underlying veclite handle.
func (s *Store) Close() error {
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
