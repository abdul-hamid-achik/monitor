package issues

import (
	"context"
	"errors"
	"math/rand/v2"
	"path/filepath"
	"sync"
	"time"

	veclite "github.com/abdul-hamid-achik/veclite"
)

// jitterBackoffMin/Max bound the retry delay between OpenStore attempts
// while another process holds the store's exclusive writer lock.
const (
	jitterBackoffMin = 50 * time.Millisecond
	jitterBackoffMax = 400 * time.Millisecond
)

// writerLocks serializes WithWriter's open->run->close cycle per store path
// WITHIN this process. veclite v0.22.1's file lock is not safe against a
// fresh Open() racing another Close() on the same path (storage.File.Unlock
// truncates, flock-unlocks, and os.Remove()s the ".lock" file before a new
// Open() re-creates it, and Save() writes to a fixed, non-unique ".tmp"
// path); two such cycles overlapping by even a few microseconds can corrupt
// or silently drop a write (verified: concurrent WithWriter calls without
// this mutex occasionally saw "no such file", a checksum mismatch, or an
// occurrence_count lower than the number of callers that reported success).
// This mutex cannot fix cross-process contention -- that still depends on
// veclite's own (buggy) file lock -- but it removes the far more common
// intra-process case, e.g. `monitor watch --stash`'s own concurrent alert
// deliveries. The underlying defect belongs to local-sentry roadmap item
// E1.8a (atomic Save, upstream veclite).
var writerLocks sync.Map // map[string]*sync.Mutex

func writerLockFor(path string) *sync.Mutex {
	key := path
	if abs, err := filepath.Abs(path); err == nil {
		key = abs
	}
	actual, _ := writerLocks.LoadOrStore(key, &sync.Mutex{})
	return actual.(*sync.Mutex)
}

// OpenStoreWait opens a writable issue store, retrying with jittered
// backoff (50-400ms) while OpenStore fails with veclite.ErrFileLocked,
// until wait elapses or ctx is cancelled. A non-lock error returns
// immediately. wait <= 0 tries exactly once, like OpenStore.
//
// This exists because monitor writers are short-lived by design (a `watch
// --stash` delivery, one `investigate`, one `issues resolve`): the store is
// opened, written, and closed per call rather than held for a process's
// lifetime, so two short writers racing each other should each succeed by
// waiting out the other's brief exclusive lock instead of failing outright.
func OpenStoreWait(ctx context.Context, path string, wait time.Duration) (*Store, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(wait)
	for {
		store, err := OpenStore(path)
		if err == nil {
			return store, nil
		}
		if !errors.Is(err, veclite.ErrFileLocked) {
			return nil, err
		}
		if wait <= 0 {
			return nil, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, err
		}
		backoff := jitterBackoff()
		if backoff > remaining {
			backoff = remaining
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// WithWriter opens a writable store via OpenStoreWait, runs fn, and always
// closes the store afterward. When fn succeeds, WithWriter still returns a
// Close error (e.g. a permission problem re-securing the file); when fn
// fails, WithWriter closes the store to release the lock promptly but
// returns fn's error, since that is almost always the more actionable one.
//
// Every WithWriter call for the same path also serializes against every
// OTHER WithWriter call for that path within this process (see writerLocks):
// the `wait` budget below still bounds only the cross-process file-lock
// wait, not this in-process queue, but each cycle is a single open, one
// fn call, and a close, so the added latency in practice is small.
func WithWriter(ctx context.Context, path string, wait time.Duration, fn func(*Store) error) error {
	mu := writerLockFor(path)
	mu.Lock()
	defer mu.Unlock()

	store, err := OpenStoreWait(ctx, path, wait)
	if err != nil {
		return err
	}
	fnErr := fn(store)
	closeErr := store.Close()
	if fnErr != nil {
		return fnErr
	}
	return closeErr
}

func jitterBackoff() time.Duration {
	span := jitterBackoffMax - jitterBackoffMin
	return jitterBackoffMin + time.Duration(rand.Int64N(int64(span)+1))
}
