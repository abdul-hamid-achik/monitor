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
	mu       sync.Mutex
	db       *veclite.DB
	collName string
	path     string
}

const defaultCollection = "logs"

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
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create log store directory: %w", err)
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
		opts.Limit = 50
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
