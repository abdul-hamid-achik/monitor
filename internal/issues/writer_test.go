package issues

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	veclite "github.com/abdul-hamid-achik/veclite"
)

func TestOpenStoreWaitAcquiresAfterRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	holder := openTestStore(t, path)

	released := make(chan struct{})
	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = holder.Close()
		close(released)
	}()

	start := time.Now()
	store, err := OpenStoreWait(context.Background(), path, 2*time.Second)
	if err != nil {
		t.Fatalf("OpenStoreWait: %v", err)
	}
	defer store.Close()
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("OpenStoreWait returned before the holder released its lock (%s elapsed)", elapsed)
	}
	<-released
	if _, _, err := store.UpsertOccurrence(OccurrenceInput{Project: "p", Message: "boom"}); err != nil {
		t.Fatalf("UpsertOccurrence after wait-acquire: %v", err)
	}
}

func TestOpenStoreWaitTimesOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	holder := openTestStore(t, path)
	defer holder.Close()

	start := time.Now()
	_, err := OpenStoreWait(context.Background(), path, 150*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("OpenStoreWait succeeded while the holder still held the lock")
	}
	if !errors.Is(err, veclite.ErrFileLocked) {
		t.Fatalf("error = %v, want ErrFileLocked", err)
	}
	if elapsed < 150*time.Millisecond {
		t.Fatalf("OpenStoreWait gave up after %s, want at least the requested wait", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("OpenStoreWait took %s, want it bounded near the requested wait", elapsed)
	}
}

func TestOpenStoreWaitZeroTriesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	holder := openTestStore(t, path)
	defer holder.Close()

	// veclite's own OpenStore already retries internally while locked
	// (storage.DefaultLockConfig: 3 retries, ~700ms total), so a single
	// bare OpenStore call is not instantaneous when contended. wait<=0
	// must not add ANOTHER retry cycle on top of that; it should cost
	// about the same as one bare OpenStore call, not roughly double.
	start := time.Now()
	_, bareErr := OpenStore(path)
	bareElapsed := time.Since(start)
	if !errors.Is(bareErr, veclite.ErrFileLocked) {
		t.Fatalf("bare OpenStore error = %v, want ErrFileLocked", bareErr)
	}

	start = time.Now()
	_, err := OpenStoreWait(context.Background(), path, 0)
	waitElapsed := time.Since(start)
	if !errors.Is(err, veclite.ErrFileLocked) {
		t.Fatalf("OpenStoreWait(wait=0) error = %v, want ErrFileLocked", err)
	}
	if waitElapsed > bareElapsed+250*time.Millisecond {
		t.Fatalf("OpenStoreWait(wait=0) took %s vs a bare OpenStore's %s; wait<=0 must try exactly once", waitElapsed, bareElapsed)
	}
}

func TestOpenStoreWaitContextCancelled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	holder := openTestStore(t, path)
	defer holder.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := OpenStoreWait(ctx, path, time.Hour)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
}

func TestWithWriterOpensRunsAndCloses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	var issue Issue
	err := WithWriter(context.Background(), path, time.Second, func(store *Store) error {
		var writeErr error
		issue, _, writeErr = store.UpsertOccurrence(OccurrenceInput{Project: "p", Message: "boom"})
		return writeErr
	})
	if err != nil {
		t.Fatalf("WithWriter: %v", err)
	}
	if issue.ID == "" {
		t.Fatal("WithWriter did not run fn")
	}
	// The store must be closed afterward: another writer should acquire it
	// immediately rather than time out.
	second, err := OpenStoreWait(context.Background(), path, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("second writer could not acquire the store after WithWriter returned: %v", err)
	}
	_ = second.Close()
}

func TestWithWriterReturnsFnErrorAndStillCloses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	sentinel := errors.New("boom")
	err := WithWriter(context.Background(), path, time.Second, func(*Store) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want sentinel", err)
	}
	// fn's error must not leak the lock.
	second, err := OpenStoreWait(context.Background(), path, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("second writer could not acquire the store after a failing WithWriter: %v", err)
	}
	_ = second.Close()
}

// TestConcurrentWithWriterUpsertsNeverSeeFileLocked is the E1.2 done-when
// criterion: 100 concurrent short-lived writers against the same store must
// never surface veclite.ErrFileLocked to the caller, thanks to
// OpenStoreWait's retry.
//
// It intentionally does NOT require every one of the 100 writes to
// succeed. Retrying a contended writer instead of failing it fast (as
// OpenStoreWait now does) means many more real open->write->close cycles
// land back-to-back against the same store file than a bare OpenStore
// (which mostly just fails immediately with ErrFileLocked under this much
// contention -- verified separately). That higher realized concurrency
// reliably exposes a SEPARATE, pre-existing veclite v0.22.1 defect in this
// environment: storage.File.Unlock() truncates, unlocks, closes, and then
// os.Remove()s the lock file (internal/storage/file.go), and Save() writes
// to a fixed, non-unique "<path>.tmp" (file.go:414). A fresh Open() racing
// that unlink-then-recreate window can win its own flock on a *new* inode
// while the just-finished writer's Save is still mid-rename, producing
// "storage rename: ... no such file or directory" or a checksum mismatch
// -- never ErrFileLocked. This is exactly the class of bug local-sentry
// roadmap item E1.8a (atomic Save, upstream veclite) exists to fix; it is
// out of scope here (internal/issues must not vendor a veclite patch), so
// this test asserts monitor's own contract -- zero ErrFileLocked -- and
// logs any other error as a known, tracked upstream issue instead of
// failing the build on it.
func TestConcurrentWithWriterUpsertsNeverSeeFileLocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	// Bootstrap the store/collections once so every goroutine's first
	// OpenStore call contends on file locking, not directory creation.
	bootstrap := openTestStore(t, path)
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("bootstrap Close: %v", err)
	}

	const n = 100
	var wg sync.WaitGroup
	results := make(chan error, n)
	ids := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var issueID string
			err := WithWriter(context.Background(), path, DefaultWriterWait, func(store *Store) error {
				issue, _, err := store.UpsertOccurrence(OccurrenceInput{
					// The same normalized message for every goroutine (digits
					// fold to <n>) so every successful write groups into one
					// issue, like TestConcurrentUpsertIsAtomicWithinStore.
					Project: "p", Service: "api", Message: fmt.Sprintf("request %d failed", i),
					RunID: fmt.Sprintf("run-%d", i),
				})
				issueID = issue.ID
				return err
			})
			if err == nil {
				ids <- issueID
			}
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	close(ids)

	lockedCount, otherCount := 0, 0
	for err := range results {
		switch {
		case err == nil:
		case errors.Is(err, veclite.ErrFileLocked):
			lockedCount++
		default:
			otherCount++
			t.Logf("non-ErrFileLocked error under contention (tracked upstream veclite issue, see test doc comment): %v", err)
		}
	}
	if lockedCount != 0 {
		t.Fatalf("%d/%d concurrent writers saw ErrFileLocked, want 0", lockedCount, n)
	}
	if otherCount > 0 {
		t.Logf("%d/%d concurrent writers hit the tracked upstream veclite storage race instead of succeeding (0 saw ErrFileLocked)", otherCount, n)
	}

	var issueID string
	successCount := 0
	for id := range ids {
		successCount++
		if issueID == "" {
			issueID = id
		} else if id != issueID {
			t.Errorf("concurrent writers created issue %s, want %s", id, issueID)
		}
	}
	if successCount == 0 {
		t.Fatal("every concurrent writer failed; expected at least some to succeed")
	}

	reader := openTestStore(t, path)
	issue, err := reader.Get(issueID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if issue.OccurrenceCount != int64(successCount) {
		t.Fatalf("occurrence_count = %d, want %d (the number of writers that actually succeeded)", issue.OccurrenceCount, successCount)
	}
}
