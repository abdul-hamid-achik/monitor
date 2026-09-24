package issues

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync"
	"time"

	veclite "github.com/abdul-hamid-achik/veclite"
)

// jitterBackoffMin/Max bound the retry delay between OpenStore attempts
// while another process holds the store's exclusive writer lock, and
// between crossProcessLock acquisition attempts below.
const (
	jitterBackoffMin = 50 * time.Millisecond
	jitterBackoffMax = 400 * time.Millisecond
)

// writerLockSuffix names monitor's own cross-process lock file, next to the
// store: "<store>.writer.lock". See acquireCrossProcessLock's doc comment.
const writerLockSuffix = ".writer.lock"

// errWriterLockHeld is flockExclusive's (lock_unix.go/lock_other.go)
// sentinel for "another file descriptor already holds this lock" -- kept
// package-private since acquireCrossProcessLock always translates it to
// veclite.ErrFileLocked before it reaches a caller, matching the error
// every existing caller of OpenStoreWait/WithWriter already checks for
// (errors.Is(err, veclite.ErrFileLocked)).
var errWriterLockHeld = errors.New("issues: writer lock held by another process")

// acquireCrossProcessLock opens (creating if necessary) absPath's
// ".writer.lock" file and takes an exclusive advisory lock on it via
// flockExclusive, retrying with jittered backoff until deadline passes or
// ctx is cancelled. wait<=0 tries exactly once, matching OpenStoreWait's
// own "wait<=0 tries exactly once" contract.
//
// This is monitor's OWN lock, entirely separate from veclite's ".lock"
// file, and it is why OpenStoreWait now serializes writers safely across
// processes where it previously did not: veclite v0.22.1's own exclusive
// lock is not safe against a fresh Open() racing another Close() on the
// same path (storage.File.Unlock truncates, flock-unlocks, and then
// os.Remove()s the ".lock" file *before* a new Open() re-creates it, so two
// processes can end up each holding LOCK_EX on two DIFFERENT inodes at
// once; separately, Save() writes to a single fixed, non-unique ".tmp"
// path). Two such veclite open->write->close cycles overlapping by even a
// few microseconds can silently drop a write, corrupt the store's checksum,
// or wipe it back to a near-empty snapshot (verified cross-process, see
// writer_test.go). This lock file is created once and NEVER removed --
// unlike veclite's own -- so its inode is stable for the store's entire
// lifetime and a fresh acquirer can never win a flock on a *different*
// inode while a previous holder's release is still in flight: as long as
// EVERY writer (OpenStoreWait, and therefore WithWriter) takes this lock
// BEFORE calling veclite.Open and holds it until AFTER veclite.Close
// returns (see Store.Close), no two processes are ever inside veclite's
// own open->close cycle at the same time, so its unlink race can never
// trigger. The underlying veclite defect itself belongs to local-sentry
// roadmap item E1.8a (atomic Save, upstream); this lock is monitor's own
// workaround and does not require or wait for that upstream fix.
func acquireCrossProcessLock(ctx context.Context, absPath string, wait time.Duration) (*os.File, error) {
	f, err := os.OpenFile(absPath+writerLockSuffix, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open writer lock: %w", err)
	}
	deadline := time.Now().Add(wait)
	for {
		lockErr := flockExclusive(f)
		if lockErr == nil {
			return f, nil
		}
		if !errors.Is(lockErr, errWriterLockHeld) {
			_ = f.Close()
			return nil, fmt.Errorf("acquire writer lock: %w", lockErr)
		}
		if wait <= 0 {
			_ = f.Close()
			return nil, veclite.ErrFileLocked
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			_ = f.Close()
			return nil, veclite.ErrFileLocked
		}
		backoff := jitterBackoff()
		if backoff > remaining {
			backoff = remaining
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// releaseCrossProcessLock unlocks and closes f. It never os.Remove()s the
// lock file -- see acquireCrossProcessLock's doc comment for why that
// unlink is exactly the veclite defect this lock exists to avoid
// repeating.
func releaseCrossProcessLock(f *os.File) {
	_ = flockRelease(f)
	_ = f.Close()
}

// writerLocks serializes WithWriter's open->run->close cycle per store path
// WITHIN this process, on top of (not instead of) acquireCrossProcessLock's
// cross-process lock above: it removes a little pointless contention on the
// cross-process lock/flock syscalls for the common case of many goroutines
// in ONE process (e.g. `monitor watch --stash`'s own concurrent alert
// deliveries) all targeting the same store.
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
// backoff (50-400ms) while the store's exclusive lock is held by another
// caller, until wait elapses or ctx is cancelled. A non-lock error returns
// immediately. wait <= 0 tries exactly once, like OpenStore.
//
// This exists because monitor writers are short-lived by design (a `watch
// --stash` delivery, one `investigate`, one `issues resolve`): the store is
// opened, written, and closed per call rather than held for a process's
// lifetime, so two short writers racing each other should each succeed by
// waiting out the other's brief exclusive lock instead of failing outright.
//
// Every open now goes through acquireCrossProcessLock FIRST -- monitor's
// own cross-process lock, held for the returned Store's whole lifetime and
// released only in Store.Close -- before veclite.Open is ever attempted;
// see acquireCrossProcessLock's doc comment for why this, not veclite's own
// file lock, is what makes concurrent OpenStoreWait callers across
// SEPARATE processes actually safe.
func OpenStoreWait(ctx context.Context, path string, wait time.Duration) (*Store, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	absPath, err := resolveStorePath(path)
	if err != nil {
		return nil, err
	}
	lockFile, err := acquireCrossProcessLock(ctx, absPath, wait)
	if err != nil {
		return nil, err
	}

	deadline := time.Now().Add(wait)
	for {
		store, err := OpenStore(absPath)
		if err == nil {
			store.writerLock = lockFile
			return store, nil
		}
		if !errors.Is(err, veclite.ErrFileLocked) {
			releaseCrossProcessLock(lockFile)
			return nil, err
		}
		if wait <= 0 {
			releaseCrossProcessLock(lockFile)
			return nil, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			releaseCrossProcessLock(lockFile)
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
			releaseCrossProcessLock(lockFile)
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
// OTHER WithWriter call for that path within this process (see writerLocks)
// AND, via OpenStoreWait, against every WithWriter/OpenStoreWait call for
// that path in every OTHER process (see acquireCrossProcessLock): the
// `wait` budget below bounds both the in-process queue and the
// cross-process lock wait, but each cycle is a single open, one fn call,
// and a close, so the added latency in practice is small.
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
