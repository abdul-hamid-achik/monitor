package issues

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	veclite "github.com/abdul-hamid-achik/veclite"
)

// holdForLongerThanVecliteInternalRetry must exceed veclite's OWN internal
// Lock retry (DefaultLockConfig: 100+200+400ms = ~700ms total, see
// TestOpenStoreWaitZeroTriesOnce). A hold shorter than that lets a bare,
// non-retrying OpenStore succeed on its own -- verified by mutation: with
// OpenStoreWait mutated to return on the first ErrFileLocked instead of
// retrying, a 150ms hold still let this test pass, because veclite's own
// retry (not OpenStoreWait's) absorbed the whole wait. Only a hold that
// outlasts veclite's own retry budget can distinguish "OpenStoreWait
// retries" from "veclite's Lock already retries internally".
const holdForLongerThanVecliteInternalRetry = 1200 * time.Millisecond

func TestOpenStoreWaitAcquiresAfterRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	holder := openTestStore(t, path)

	released := make(chan struct{})
	go func() {
		time.Sleep(holdForLongerThanVecliteInternalRetry)
		_ = holder.Close()
		close(released)
	}()

	start := time.Now()
	store, err := OpenStoreWait(context.Background(), path, 3*time.Second)
	if err != nil {
		t.Fatalf("OpenStoreWait: %v", err)
	}
	defer store.Close()
	if elapsed := time.Since(start); elapsed < holdForLongerThanVecliteInternalRetry {
		t.Fatalf("OpenStoreWait returned after %s, before the holder released its lock at %s: only OpenStoreWait's OWN retry loop (not veclite's internal one) should be able to succeed here", elapsed, holdForLongerThanVecliteInternalRetry)
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

// TestConcurrentWithWriterUpsertsNeverSeeFileLocked exercises WithWriter's
// production entry point (the same call watch.go / investigate.go /
// issues.go use) with 100 goroutines against the same store.
//
// IMPORTANT SCOPE NOTE: because every one of these goroutines runs inside
// THIS process, WithWriter's own per-path in-process mutex (writerLockFor)
// serializes all 100 of them before any of them even attempts to open the
// file -- so this test does NOT exercise OpenStoreWait's retry loop against
// real OS-level flock contention; it only proves WithWriter's open/run/close
// contract is safe to call concurrently from goroutines within one process
// (which is all `watch --stash`'s own concurrent alert deliveries ever do).
// See TestConcurrentCrossProcessWritersNeverSeeFileLocked below for a test
// that spawns genuinely separate OS processes -- the real-world shape of
// `monitor investigate` / `monitor issues resolve` racing a live
// `monitor watch --stash` -- and so actually contends on the real flock.
//
// It intentionally does NOT require every one of the 100 writes to
// succeed. A SEPARATE, pre-existing veclite v0.22.1 defect in this
// environment can surface under real contention: storage.File.Unlock()
// truncates, unlocks, closes, and then os.Remove()s the lock file
// (internal/storage/file.go), and Save() writes to a fixed, non-unique
// "<path>.tmp" (file.go:414). A fresh Open() racing that unlink-then-recreate
// window can win its own flock on a *new* inode while the just-finished
// writer's Save is still mid-rename, producing "storage rename: ... no such
// file or directory" or a checksum mismatch -- never ErrFileLocked. This is
// exactly the class of bug local-sentry roadmap item E1.8a (atomic Save,
// upstream veclite) exists to fix; it is out of scope here (internal/issues
// must not vendor a veclite patch), so this test asserts monitor's own
// contract -- zero ErrFileLocked -- and logs any other error as a known,
// tracked upstream issue instead of failing the build on it.
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

// Environment variables used to re-exec this test binary as an independent
// helper OS process for TestConcurrentCrossProcessWritersNeverSeeFileLocked.
const (
	crossProcessWriterHelperEnv       = "MONITOR_ISSUES_CROSS_PROCESS_WRITER_HELPER"
	crossProcessWriterHelperPath      = "MONITOR_ISSUES_CROSS_PROCESS_WRITER_PATH"
	crossProcessWriterHelperIndex     = "MONITOR_ISSUES_CROSS_PROCESS_WRITER_INDEX"
	crossProcessWriterHelperLocked    = "LOCKED"
	crossProcessWriterHelperOKPrefix  = "OK "
	crossProcessWriterHelperErrPrefix = "ERROR "
)

// TestCrossProcessWriterHelperProcess is not a real test: under a normal
// `go test` run (crossProcessWriterHelperEnv unset) it is a no-op, exactly
// like TestTelemetrySIGTERMHelperProcess in internal/cli. It is re-exec'd by
// TestConcurrentCrossProcessWritersNeverSeeFileLocked as one of N
// independent OS processes, each with its OWN fresh writerLocks map, so
// unlike TestConcurrentWithWriterUpsertsNeverSeeFileLocked's goroutines they
// genuinely race on the real OS-level flock (syscall.Flock is scoped to the
// open file description, not the process, but two SEPARATE processes each
// opening their own file descriptor is the least ambiguous way to prove it,
// and it matches production: `monitor investigate` / `monitor issues
// resolve` are always separate OS processes from a live `monitor watch
// --stash`). It prints exactly one line ("OK <issue-id>", "LOCKED", or
// "ERROR <message>") and always exits 0, so the parent can distinguish a
// genuine WithWriter outcome from a helper crash via cmd.Output()'s error.
// crossProcessHelperWait is the per-helper lock wait budget in
// TestConcurrentCrossProcessWritersNeverSeeFileLocked (see the comment at its
// use site).
const crossProcessHelperWait = 90 * time.Second

func TestCrossProcessWriterHelperProcess(t *testing.T) {
	if os.Getenv(crossProcessWriterHelperEnv) != "1" {
		return
	}
	path := os.Getenv(crossProcessWriterHelperPath)
	index := os.Getenv(crossProcessWriterHelperIndex)
	// 100 helpers serialize on one flock; each holds it for a full-snapshot
	// Save (plus fsync), which on a 2-vCPU CI runner under -race adds up to
	// well over DefaultWriterWait. This test verifies that the retry loop
	// absorbs contention given a sufficient budget, not the default budget
	// for a 100-way burst, so it waits generously.
	var issueID string
	err := WithWriter(context.Background(), path, crossProcessHelperWait, func(store *Store) error {
		issue, _, err := store.UpsertOccurrence(OccurrenceInput{
			// The same normalized message for every helper (digits fold to
			// <n>) so every successful write groups into one issue.
			Project: "p", Service: "api", Message: fmt.Sprintf("request %s failed", index),
			RunID: "run-" + index,
		})
		issueID = issue.ID
		return err
	})
	switch {
	case err == nil:
		fmt.Println(crossProcessWriterHelperOKPrefix + issueID)
	case errors.Is(err, veclite.ErrFileLocked):
		fmt.Println(crossProcessWriterHelperLocked)
	default:
		fmt.Println(crossProcessWriterHelperErrPrefix + err.Error())
	}
	os.Exit(0)
}

// TestConcurrentCrossProcessWritersNeverSeeFileLocked is the cross-process
// counterpart TestConcurrentWithWriterUpsertsNeverSeeFileLocked's doc
// comment promises: it re-execs this test binary as 100 independent OS
// processes (never through this package's in-process writerLocks mutex,
// which would hide exactly the contention this test exists to exercise),
// each calling WithWriter once against the SAME store, so it genuinely
// contends on the real OS-level flock the way separate `monitor` invocations
// do in production.
//
// This is deliberately NOT "100/100 successes required, fail on any other
// error": that would assert something the branch's own measurements show is
// false. A real cross-process burst of this size (100 concurrent
// `bin/monitor issues resolve` processes against one store, measured
// separately from this test) saw 0 ErrFileLocked but anywhere from 0 to 98
// non-ErrFileLocked storage errors across repeated runs, from the SAME
// pre-existing veclite v0.22.1 unlink/rename race described on
// TestConcurrentWithWriterUpsertsNeverSeeFileLocked's doc comment (also
// reproducible against the unmodified base binary, just as ErrFileLocked
// instead). Asserting 100/100 here would make this test flake on an
// upstream defect this package cannot fix (E1.8a, out of scope) rather than
// verify OpenStoreWait's own contract. That contract -- and the one thing
// this test hard-fails on -- is zero ErrFileLocked: OpenStoreWait's retry
// loop must absorb real cross-process lock contention, full stop.
//
// The same upstream race can also make the final occurrence_count disagree
// with successCount in EITHER direction: it can silently drop an already-
// reported-successful write (a later writer's full-snapshot Save overwrites
// an earlier one's, per writer.go's package doc), or -- observed directly
// while writing this test -- credit a write whose OWN call reported
// failure, when its multi-step Save (write .tmp, rename old->.bak, rename
// .tmp->final) partially interleaves with a concurrent writer's. Neither
// direction is OpenStoreWait's contract to uphold, so both are logged, not
// failed.
func TestConcurrentCrossProcessWritersNeverSeeFileLocked(t *testing.T) {
	if os.Getenv(crossProcessWriterHelperEnv) == "1" {
		t.Skip("re-exec helper process; see TestCrossProcessWriterHelperProcess")
	}

	path := filepath.Join(t.TempDir(), "issues.veclite")
	// Bootstrap the store/collections once so every helper's first
	// OpenStore call contends on file locking, not directory creation.
	bootstrap := openTestStore(t, path)
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("bootstrap Close: %v", err)
	}

	const n = 100
	type helperResult struct {
		idx int
		out string
		err error
	}
	results := make(chan helperResult, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestCrossProcessWriterHelperProcess$")
			cmd.Env = append(os.Environ(),
				crossProcessWriterHelperEnv+"=1",
				crossProcessWriterHelperPath+"="+path,
				crossProcessWriterHelperIndex+"="+strconv.Itoa(i),
			)
			out, err := cmd.Output()
			results <- helperResult{idx: i, out: strings.TrimSpace(string(out)), err: err}
		}(i)
	}
	wg.Wait()
	close(results)

	lockedCount, otherCount, successCount := 0, 0, 0
	var issueID string
	for r := range results {
		if r.err != nil {
			t.Fatalf("helper process %d failed to run (%v); this is a harness failure, not a WithWriter outcome", r.idx, r.err)
		}
		switch {
		case r.out == crossProcessWriterHelperLocked:
			lockedCount++
		case strings.HasPrefix(r.out, crossProcessWriterHelperOKPrefix):
			successCount++
			id := strings.TrimPrefix(r.out, crossProcessWriterHelperOKPrefix)
			if issueID == "" {
				issueID = id
			} else if id != issueID {
				t.Errorf("helper %d created issue %s, want %s", r.idx, id, issueID)
			}
		case strings.HasPrefix(r.out, crossProcessWriterHelperErrPrefix):
			otherCount++
			t.Logf("helper %d hit a non-ErrFileLocked storage error under real cross-process contention (tracked upstream veclite issue, see TestConcurrentWithWriterUpsertsNeverSeeFileLocked's doc comment): %s", r.idx, r.out)
		default:
			t.Fatalf("helper %d produced unexpected output %q", r.idx, r.out)
		}
	}
	if lockedCount != 0 {
		t.Fatalf("%d/%d cross-process writers saw ErrFileLocked, want 0: OpenStoreWait's retry loop must absorb real cross-process flock contention", lockedCount, n)
	}
	if successCount == 0 {
		t.Fatal("every cross-process writer failed; expected at least some to succeed")
	}
	if otherCount > 0 {
		t.Logf("%d/%d cross-process writers hit the tracked upstream veclite storage race instead of succeeding (0 saw ErrFileLocked)", otherCount, n)
	}

	// Reading the store back afterward is best-effort, NOT part of
	// OpenStoreWait's contract: under enough real cross-process contention
	// the same known upstream veclite race can leave the store file itself
	// unopenable (observed directly while writing this test: a run with a
	// high failure rate above left a store that failed to even Open,
	// matching local-sentry finding "store sano" is not met for bursts this
	// size -- tracked, E1.8a, out of scope here). OpenStoreWait's own
	// contract (zero ErrFileLocked) was already asserted above and does not
	// depend on any of this succeeding.
	reader, err := OpenStore(path)
	if err != nil {
		t.Logf("could not reopen the store afterward (%v): the known upstream veclite race, logged not failed (see above)", err)
		return
	}
	defer reader.Close()
	issue, err := reader.Get(issueID)
	if err != nil {
		t.Logf("could not read back issue %s afterward (%v): the known upstream veclite race, logged not failed (see above)", issueID, err)
		return
	}
	if issue.OccurrenceCount != int64(successCount) {
		// The known upstream veclite Save/rename race can go either way
		// under real cross-process contention: it can silently drop an
		// already-reported-successful write, or credit a write whose OWN
		// call reported failure. Neither direction is OpenStoreWait's
		// contract to uphold, so both are logged, not failed.
		t.Logf("occurrence_count = %d, successCount (writers that reported success) = %d: the known upstream veclite Save/rename race (E1.8a, out of scope here) either dropped a successful write or credited a reported-failed one", issue.OccurrenceCount, successCount)
	}
}
