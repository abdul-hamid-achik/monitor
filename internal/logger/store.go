// Package logger captures process log lines into a local veclite store and
// supports keyword search via shared-read for CLI tools.
//
// Architecture: the TUI holds the writer lock; CLI tools (search, export) open
// the same database read-only with shared read-lock so concurrent queries do
// not block collection. External processes can call Reload() (or POST to the
// TUI's HTTP /reload endpoint) to nudge readers to re-query.
package logger

import (
	"errors"
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
	mu       sync.Mutex
	db       *veclite.DB
	collName string
	path     string
}

const defaultCollection = "logs"

// OpenStore opens (and creates) a log store at path. The writer does NOT use
// shared-read; only readers do.
func OpenStore(path string) (*Store, error) {
	db, err := veclite.Open(path)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, path: path, collName: defaultCollection}
	if err := s.ensureCollection(); err != nil {
		_ = db.Close()
		return nil, err
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
	return &Store{db: db, path: path, collName: defaultCollection}, nil
}

func (s *Store) ensureCollection() error {
	if s.db == nil {
		return errors.New("store not open")
	}
	if s.db.HasCollection(defaultCollection) {
		return nil
	}
	_, err := s.db.CreateCollection(defaultCollection)
	return err
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
	coll := s.db.Collection(defaultCollection)
	payload := map[string]any{
		"timestamp": e.Timestamp,
		"pid":       e.PID,
		"process":   e.Process,
		"level":     e.Level,
		"raw":       e.Raw,
	}
	_, err := coll.InsertTextDocument(e.Message, payload)
	return err
}

// Search scans records whose message or raw payload contains the query
// (case-insensitive substring), returns them newest-first, and caps at
// limit (default 50). veclite's high-level SearchText requires an embedder;
// we use a simple linear scan over Find(). Locked for the duration so the
// nil check and db access are atomic with Append/Close.
func (s *Store) Search(query string, limit int) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, errors.New("store not open")
	}
	if limit <= 0 {
		limit = 50
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
	matches := make([]Entry, 0, limit)
	for _, rec := range res {
		if rec == nil {
			continue
		}
		e := entryFromRecord(rec)
		if !strings.Contains(strings.ToLower(e.Message), strings.ToLower(query)) &&
			!strings.Contains(strings.ToLower(e.Raw), strings.ToLower(query)) {
			continue
		}
		matches = append(matches, e)
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Timestamp.After(matches[j].Timestamp)
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}

func entryFromRecord(rec *veclite.Record) Entry {
	e := Entry{Message: rec.Content, Raw: rec.Content}
	if rec.Payload != nil {
		if ts, ok := rec.Payload["timestamp"].(time.Time); ok {
			e.Timestamp = ts
		}
		if p, ok := rec.Payload["pid"].(int32); ok {
			e.PID = p
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
