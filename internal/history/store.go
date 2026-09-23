// Package history persists scalar metric samples (cpu, memory, network, ...)
// to a local veclite store so they can be queried over real time windows —
// the durable counterpart to the collector's in-memory ring buffers.
//
// The recorder (`monitor history record`) and the query (`monitor history
// <metric>`) run as separate processes, so timestamps are stored as
// Unix-nanoseconds (which round-trip through veclite's on-disk serialization
// reliably) rather than as time.Time values.
//
// Concurrency model: the writer (Open) holds an exclusive file lock for the
// whole recording session, and OpenReadOnly takes a shared lock — the two are
// mutually exclusive, so a query CANNOT open the store while a recorder is
// actively running (it gets a lock error, surfaced with guidance). To keep the
// on-disk snapshot reasonably current without paying for a full gob-encode +
// fsync of the whole database on every tick, Append flushes with db.Sync()
// every syncEvery batches or syncInterval of wall-clock time, whichever comes
// first (veclite's default syncOnWrite is off), and Close always flushes
// whatever is still pending. A reader opened once the recorder has released
// the lock therefore sees every sample as of the last flush, which lags the
// most recent tick by at most syncInterval.
package history

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	veclite "github.com/abdul-hamid-achik/veclite"
)

const collection = "metrics"

// defaultMaxRecords caps the recorder's collection so a long-running `monitor
// history record` can't grow memory/disk without bound. veclite evicts
// oldest-first (fifo) once the cap is hit. At the default 1s interval × 8
// metrics/tick that is ~36 hours of history; higher intervals stretch
// proportionally.
//
// The cap lives on Store.maxRecords (set from this default by Open) and is
// enforced by enforceRecordLimitLocked on every Append, independent of
// veclite's own per-collection MemoryConfig. veclite v0.22.1 does not persist
// a collection's MemoryConfig across a reopen (loadFromSnapshot rebuilds the
// collection without it — a verified upstream bug), so relying solely on
// WithMemoryLimits at creation time silently disables the cap forever the
// first time the store is closed and reopened. Enforcing the cap ourselves,
// unconditionally, from Go-level Store state that Open always sets, keeps
// history bounded across the store's whole lifetime regardless of reopens.
const defaultMaxRecords = 1_000_000

// evictionBatchThreshold is how far the collection may grow past maxRecords
// before enforceRecordLimit pays for a sorted eviction pass, expressed as a
// fraction of maxRecords (minimum 1 record). EnforceMemoryLimit's underlying
// eviction sorts every live record to select what to evict; running that sort
// once per single record over the cap made every insert past the cap an
// O(n log n) full scan. Batching lets the collection grow up to
// maxRecords+threshold-1 before one pass evicts the whole accumulated
// overflow back down to maxRecords, trading a small amount of extra memory
// for far fewer sorts.
func evictionBatchThreshold(max int) int {
	b := max / 10
	if b < 1 {
		b = 1
	}
	return b
}

// defaultSyncEvery / defaultSyncInterval bound how many Append batches (ticks)
// can accumulate in memory before a durable flush. See maybeSyncLocked.
const (
	defaultSyncEvery    = 5
	defaultSyncInterval = 5 * time.Second
)

// DefaultPath returns the default history store path
// (~/.local/share/monitor/history.veclite), creating the directory.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".local", "share", "monitor")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "history.veclite"), nil
}

// Sample is one scalar metric reading.
type Sample struct {
	Timestamp time.Time
	Metric    string
	Value     float64
}

// Point is a single (time, value) datum returned by a query.
type Point struct {
	Timestamp time.Time `json:"t"`
	Value     float64   `json:"v"`
}

// Store is a veclite-backed time-series of metric samples.
type Store struct {
	mu sync.Mutex
	db *veclite.DB

	// maxRecords is this store's Go-level record cap (see defaultMaxRecords'
	// doc comment for why it lives here rather than solely in veclite).
	maxRecords int

	// syncEvery / syncInterval and the counters below implement the bounded
	// flush policy described on maybeSyncLocked. pendingWrites counts Append
	// batches since the last Sync; syncCount is exported via SyncCount for
	// tests and diagnostics that need to observe how often a full rewrite
	// actually happened.
	syncEvery     int
	syncInterval  time.Duration
	pendingWrites int
	lastSync      time.Time
	syncCount     int
}

// Open opens (and creates) a history store at path for writing.
func Open(path string) (*Store, error) {
	db, err := veclite.Open(path)
	if err != nil {
		return nil, err
	}
	s := &Store{
		db:           db,
		maxRecords:   defaultMaxRecords,
		syncEvery:    defaultSyncEvery,
		syncInterval: defaultSyncInterval,
		lastSync:     time.Now(),
	}
	if err := s.ensureCollection(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// OpenReadOnly opens the store read-only with shared read, for queries that
// run while a recorder holds the writer.
func OpenReadOnly(path string) (*Store, error) {
	db, err := veclite.Open(path, veclite.WithReadOnly(true), veclite.WithSharedRead(true))
	if err != nil {
		return nil, err
	}
	return &Store{db: db, maxRecords: defaultMaxRecords}, nil
}

func (s *Store) ensureCollection() error {
	if s.db == nil {
		return errors.New("store not open")
	}
	if s.db.HasCollection(collection) {
		return nil
	}
	// Deliberately created WITHOUT veclite.WithMemoryLimits. It is not what
	// keeps the cap alive across a reopen (see defaultMaxRecords' doc
	// comment) — that is enforceRecordLimitLocked's job, from Go-level Store
	// state on every Append. Passing a MemoryConfig here is not "free" extra
	// enforcement either: veclite@v0.22.1's enforceMemoryLimitIfConfigured
	// runs on every single InsertTextDocument call for a collection created
	// with one, evicting min(count-MaxRecords, EvictionBatchSize) records —
	// 1, the first time the cap is reached — via a full sorted scan of the
	// collection on EVERY insert once at capacity. That pins the count at
	// maxRecords forever in the very session that creates the store and
	// defeats enforceRecordLimitLocked's batching below, whose 10%-overflow
	// threshold then never fires because veclite's own per-insert eviction
	// never lets the collection grow past the cap in the first place.
	// Letting enforceRecordLimitLocked be the ONLY enforcement is what makes
	// eviction actually batched from the moment the store is created, not
	// only after a reopen.
	_, err := s.db.CreateCollection(collection)
	return err
}

// Append records one or more samples. A zero timestamp defaults to now.
func (s *Store) Append(samples ...Sample) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return errors.New("store not open")
	}
	c := s.db.Collection(collection)
	for _, smp := range samples {
		ts := smp.Timestamp
		if ts.IsZero() {
			ts = time.Now()
		}
		payload := map[string]any{
			"ts":     ts.UnixNano(),
			"metric": smp.Metric,
			"value":  smp.Value,
		}
		if _, err := c.InsertTextDocument(smp.Metric, payload); err != nil {
			return err
		}
	}
	s.enforceRecordLimitLocked(c)
	return s.maybeSyncLocked()
}

// enforceRecordLimitLocked re-applies s.maxRecords unconditionally, from
// Go-level Store state rather than veclite's per-collection MemoryConfig, so
// the cap survives a Close+reopen (see defaultMaxRecords' doc comment). Must
// be called with s.mu held.
func (s *Store) enforceRecordLimitLocked(c *veclite.Collection) int {
	count := c.Count()
	overflow := count - s.maxRecords
	if overflow < evictionBatchThreshold(s.maxRecords) {
		return 0
	}
	return c.EnforceMemoryLimit(veclite.MemoryConfig{
		MaxRecords:        s.maxRecords,
		EvictionPolicy:    "fifo",
		EvictionBatchSize: overflow,
	})
}

// maybeSyncLocked durably flushes the store once syncEvery Append batches
// have accumulated or syncInterval of wall-clock time has passed since the
// last flush, whichever comes first, then resets both counters. Must be
// called with s.mu held.
//
// history.Append used to call db.Sync() unconditionally on every batch, and
// veclite's Sync re-gob-encodes and fsyncs the WHOLE database — cost that
// grows with total record count, paid on every single tick of a long-running
// `monitor history record` session. Bounding the flush cadence instead trades
// a small, documented crash-loss / reader-staleness window (at most
// syncInterval, or syncEvery ticks) for a large reduction in write
// amplification. Close always flushes any still-pending writes.
func (s *Store) maybeSyncLocked() error {
	s.pendingWrites++
	if s.pendingWrites < s.syncEvery && time.Since(s.lastSync) < s.syncInterval {
		return nil
	}
	return s.syncLocked()
}

// syncLocked performs the actual flush and resets the batching counters.
// Must be called with s.mu held.
func (s *Store) syncLocked() error {
	if err := s.db.Sync(); err != nil {
		return err
	}
	s.pendingWrites = 0
	s.lastSync = time.Now()
	s.syncCount++
	return nil
}

// Sync flushes pending writes to disk immediately, ignoring the batching
// policy in maybeSyncLocked. No-op (returns nil) on a read-only or closed
// store.
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

// Query returns the samples for metric at or after `since`, oldest-first.
func (s *Store) Query(metric string, since time.Time) ([]Point, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, errors.New("store not open")
	}
	if !s.db.HasCollection(collection) {
		return nil, nil // nothing recorded yet
	}
	// Push the metric-name and time-window predicates into Find so veclite
	// never clones records for other metrics / older than the window.
	res, err := s.db.Collection(collection).Find(
		veclite.Equal("metric", metric),
		veclite.FilterFunc(func(r *veclite.Record) bool {
			if r == nil || r.Payload == nil {
				return false
			}
			return !payloadTime(r.Payload["ts"]).Before(since)
		}),
	)
	if err != nil {
		return nil, err
	}
	out := make([]Point, 0, len(res))
	for _, rec := range res {
		if rec == nil || rec.Payload == nil {
			continue
		}
		out = append(out, Point{
			Timestamp: payloadTime(rec.Payload["ts"]),
			Value:     payloadFloat(rec.Payload["value"]),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.Before(out[j].Timestamp) })
	return out, nil
}

// Metrics returns the distinct metric names present in the store.
func (s *Store) Metrics() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, errors.New("store not open")
	}
	if !s.db.HasCollection(collection) {
		return nil, nil
	}
	res, err := s.db.Collection(collection).Find()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, rec := range res {
		if rec == nil || rec.Payload == nil {
			continue
		}
		if m, _ := rec.Payload["metric"].(string); m != "" {
			seen[m] = true
		}
	}
	names := make([]string, 0, len(seen))
	for m := range seen {
		names = append(names, m)
	}
	sort.Strings(names)
	return names, nil
}

// Close releases the store.
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

// payloadTime converts a stored Unix-nano value (which may come back as
// int64, float64, or string after serialization) into a time.Time.
func payloadTime(v any) time.Time {
	switch n := v.(type) {
	case int64:
		return time.Unix(0, n)
	case float64:
		return time.Unix(0, int64(n))
	case int:
		return time.Unix(0, int64(n))
	default:
		return time.Time{}
	}
}

func payloadFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	default:
		return 0
	}
}
