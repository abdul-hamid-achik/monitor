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
	shared   bool
}

const defaultCollection = "logs"

// OpenStore opens (and creates) a log store at path. The writer does NOT use
// shared-read; only readers do.
func OpenStore(path string) (*Store, error) {
	db, err := veclite.Open(path)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, path: path, shared: true, collName: defaultCollection}
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
	return &Store{db: db, path: path, shared: true, collName: defaultCollection}, nil
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

// Search scans recent records whose Content or raw payload contains the query.
// veclite's high-level SearchText requires an embedder; we use a simple linear
// scan over Find() (no embedder required). For local TUI/CLI use the volume is
// small enough that this is acceptable.
func (s *Store) Search(query, mode string, limit int) ([]Entry, error) {
	if s.db == nil {
		return nil, errors.New("store not open")
	}
	if limit <= 0 {
		limit = 50
	}
	coll := s.db.Collection(defaultCollection)
	// Pull the most recent records first; Find returns ordered by insertion.
	res, err := coll.Find()
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, limit)
	for _, rec := range res {
		if rec == nil {
			continue
		}
		e := entryFromRecord(rec)
		if !strings.Contains(strings.ToLower(e.Message), strings.ToLower(query)) &&
			!strings.Contains(strings.ToLower(e.Raw), strings.ToLower(query)) {
			continue
		}
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
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