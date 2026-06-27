// Package history persists scalar metric samples (cpu, memory, network, ...)
// to a local veclite store so they can be queried over real time windows —
// the durable counterpart to the collector's in-memory ring buffers.
//
// The recorder (`monitor history record`) and the query (`monitor history
// <metric>`) typically run as separate processes, so timestamps are stored as
// Unix-nanoseconds (which round-trip through veclite's on-disk serialization
// reliably) rather than as time.Time values.
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
}

// Open opens (and creates) a history store at path for writing.
func Open(path string) (*Store, error) {
	db, err := veclite.Open(path)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
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
	return &Store{db: db}, nil
}

func (s *Store) ensureCollection() error {
	if s.db == nil {
		return errors.New("store not open")
	}
	if s.db.HasCollection(collection) {
		return nil
	}
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
	return nil
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
	res, err := s.db.Collection(collection).Find()
	if err != nil {
		return nil, err
	}
	out := make([]Point, 0)
	for _, rec := range res {
		if rec == nil || rec.Payload == nil {
			continue
		}
		if m, _ := rec.Payload["metric"].(string); m != metric {
			continue
		}
		t := payloadTime(rec.Payload["ts"])
		if t.Before(since) {
			continue
		}
		out = append(out, Point{Timestamp: t, Value: payloadFloat(rec.Payload["value"])})
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
