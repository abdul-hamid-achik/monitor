package events

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/contextids"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/project"
	"github.com/abdul-hamid-achik/monitor/internal/scrub"
	"github.com/abdul-hamid-achik/monitor/internal/statedir"
)

// File layout inside an events directory. An SDK writes ".<name>.tmp" and
// renames it to "<name>.json", where <name> starts with the zero-padded
// Unix-nanosecond time, so a name sort is oldest first. Dot-prefixed names
// are never read.
const (
	eventExt    = ".json"
	rejectedDir = ".rejected"
	// maxRejected bounds how many quarantined files a directory keeps.
	maxRejected = 20
	// staleTempAge is how old an abandoned ".tmp" (an SDK killed mid-
	// write) must be before a drain removes it.
	staleTempAge = time.Hour
	// DefaultLimit bounds one drain, so a flooded inbox never holds the
	// issues writer lock for long.
	DefaultLimit = 500
)

// InboxDir returns the global inbox an SDK writes to when no `monitor run
// --` launch gave it a directory. Pure path computation: a reader that
// finds nothing there must not create it.
func InboxDir() (string, error) {
	return statedir.Path("events", "inbox")
}

// LaunchDir returns, creating it with mode 0700, the private events
// directory of one `monitor run --` launch.
func LaunchDir(launchID string) (string, error) {
	dir, err := statedir.Path("events", "launches", launchID)
	if err != nil {
		return "", err
	}
	return dir, EnsureDir(dir)
}

// EnsureDir creates dir (and its parents) with mode 0700.
func EnsureDir(dir string) error {
	return statedir.Ensure(dir)
}

// Write stores ev in dir exactly as an SDK does (temp file, then rename),
// filling the schema, id and timestamp when unset. It returns the file's
// path.
func Write(dir string, ev Event) (string, error) {
	if ev.Schema == "" {
		ev.Schema = Schema
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	if ev.EventID == "" {
		ev.EventID = newID()
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return "", fmt.Errorf("encode event: %w", err)
	}
	if err := EnsureDir(dir); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%019d-%d-%s", ev.Timestamp.UnixNano(), os.Getpid(), ev.EventID)
	tmp := filepath.Join(dir, "."+name+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", fmt.Errorf("write event: %w", err)
	}
	final := filepath.Join(dir, name+eventExt)
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("commit event: %w", err)
	}
	return final, nil
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Pending lists every committed event file in dir, oldest first. A
// missing directory is simply empty. Abandoned temp files older than
// staleTempAge are removed on the way.
func Pending(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read events directory: %w", err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(name, ".") {
			if strings.HasSuffix(name, ".tmp") {
				removeIfStale(filepath.Join(dir, name))
			}
			continue
		}
		if strings.HasSuffix(name, eventExt) {
			out = append(out, filepath.Join(dir, name))
		}
	}
	sort.Strings(out)
	return out, nil
}

func removeIfStale(path string) {
	if info, err := os.Lstat(path); err == nil && time.Since(info.ModTime()) > staleTempAge {
		_ = os.Remove(path)
	}
}

// Load reads and decodes one event file. A file that is not a regular
// file, is too large, or does not decode returns an error wrapping
// ErrInvalid. An event with no usable id takes its file name instead, so
// it still dedupes.
func Load(path string) (Event, error) {
	// Lstat first: an inbox entry must be a plain file the SDK wrote, never
	// a symlink pointing somewhere else.
	if info, err := os.Lstat(path); err != nil {
		return Event{}, err
	} else if !info.Mode().IsRegular() {
		return Event{}, fmt.Errorf("%w: not a regular file", ErrInvalid)
	}
	f, err := os.Open(path)
	if err != nil {
		return Event{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Event{}, err
	}
	if !info.Mode().IsRegular() {
		return Event{}, fmt.Errorf("%w: not a regular file", ErrInvalid)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return Event{}, err
	}
	ev, err := Decode(data)
	if err != nil {
		return Event{}, err
	}
	if ev.EventID == "" {
		ev.EventID = cleanID(strings.TrimSuffix(filepath.Base(path), eventExt))
	}
	return ev, nil
}

// Reject moves a file that can never be recorded into the directory's
// .rejected folder, keeping only the newest maxRejected, so a bad file is
// neither retried forever nor silently destroyed.
func Reject(path string) error {
	dir := filepath.Join(filepath.Dir(path), rejectedDir)
	if err := EnsureDir(dir); err != nil {
		return err
	}
	if err := os.Rename(path, filepath.Join(dir, filepath.Base(path))); err != nil {
		return fmt.Errorf("quarantine event: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= maxRejected {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names[:len(names)-maxRejected] {
		_ = os.Remove(filepath.Join(dir, name))
	}
	return nil
}

// IngestOptions configure one drain of an events directory into the
// issues store.
type IngestOptions struct {
	Dir       string
	StorePath string
	// Wait bounds how long to wait for the store's writer lock;
	// issues.DefaultWriterWait when zero.
	Wait time.Duration
	// Limit bounds how many events one call records; DefaultLimit when
	// zero.
	Limit int
	// RedactEnvNames are extra environment variable names whose values
	// are redacted (see scrub.SecretEnvValues).
	RedactEnvNames []string
}

// IngestResult reports one drain.
type IngestResult struct {
	Recorded  int `json:"recorded"`
	Deduped   int `json:"deduped"`
	Rejected  int `json:"rejected"`
	Remaining int `json:"remaining"`
	// IssueIDs are the issues this drain wrote to, in order, without
	// repeats.
	IssueIDs []string `json:"issue_ids,omitempty"`
}

// Ingest drains up to Limit events from Dir into the store, oldest first,
// under ONE writer-lock acquisition: each event is decoded, scrubbed,
// attributed to the project its cwd resolves to and recorded. Files are
// removed only once the store has closed cleanly; after any store failure
// every file stays for the next drain, and the event id (the DedupeKey)
// folds whatever did land into the same occurrence then. A file that can
// never be recorded is quarantined (Reject). An empty or missing
// directory never touches the store.
func Ingest(ctx context.Context, opts IngestOptions) (IngestResult, error) {
	var res IngestResult
	paths, err := Pending(opts.Dir)
	if err != nil || len(paths) == 0 {
		return res, err
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if len(paths) > limit {
		res.Remaining = len(paths) - limit
		paths = paths[:limit]
	}

	scrubber := scrub.New(scrub.WithValues(scrub.SecretEnvValues(os.Environ(), opts.RedactEnvNames)))
	type item struct {
		path string
		p    Prepared
	}
	items := make([]item, 0, len(paths))
	for _, path := range paths {
		ev, err := Load(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // another drainer took it
			}
			if rerr := Reject(path); rerr == nil {
				res.Rejected++
			}
			continue
		}
		p, ok := Prepare(ev, scrubber)
		if !ok {
			if rerr := Reject(path); rerr == nil {
				res.Rejected++
			}
			continue
		}
		items = append(items, item{path: path, p: p})
	}
	if len(items) == 0 {
		return res, nil
	}

	wait := opts.Wait
	if wait <= 0 {
		wait = issues.DefaultWriterWait
	}
	seen := map[string]bool{}
	var recorded []string
	err = issues.WithWriter(ctx, opts.StorePath, wait, func(store *issues.Store) error {
		for _, it := range items {
			if err := ctx.Err(); err != nil {
				return err
			}
			id := project.Resolve(it.p.Hints)
			result, err := issues.RecordExceptionOnStore(store, it.p.Exception, id, it.p.Run, it.p.Options)
			if err != nil {
				return fmt.Errorf("record event %s: %w", filepath.Base(it.path), err)
			}
			recorded = append(recorded, it.path)
			if result.Deduped {
				res.Deduped++
				continue
			}
			res.Recorded++
			if !seen[result.Issue.ID] {
				seen[result.Issue.ID] = true
				res.IssueIDs = append(res.IssueIDs, result.Issue.ID)
			}
		}
		return nil
	})
	if err != nil {
		res.Remaining += len(items)
		res.Recorded, res.Deduped, res.IssueIDs = 0, 0, nil
		return res, err
	}
	for _, path := range recorded {
		_ = os.Remove(path)
	}
	return res, nil
}

// RunIDs merges an event's release/environment over the launch's own run
// IDs, for a live `monitor run --` that already resolved them.
func RunIDs(base contextids.IDs, p Prepared) contextids.IDs {
	if p.Run.Release != "" {
		base.Release = p.Run.Release
	}
	if p.Run.Environment != "" {
		base.Environment = p.Run.Environment
	}
	return base
}
