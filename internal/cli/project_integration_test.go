package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/incidents"
	"github.com/abdul-hamid-achik/monitor/internal/procbind"
)

// TestWatchAndInvestigateAgreeOnProject is the E1.4 done-when criterion:
// watch --stash and investigate must derive the SAME project/service
// identity for the SAME real process, so the same underlying problem never
// splits into two separate issues (bug 15). A pid/cwd inside a monorepo
// like <tmp>/graphite/web-api/package.json, with .git at <tmp>/graphite,
// must give project=graphite service=web-api from both call sites.
//
// This spawns ONE real child process whose OWN cwd is the monorepo service
// directory, and deliberately keeps the TEST process's own cwd somewhere
// else entirely for the whole test. An earlier version of this test instead
// os.Chdir'd the whole test process into serviceDir before calling
// recordAlertOccurrence, which read monitor's own os.Getwd() -- so both
// call sites trivially agreed by construction and this test could not have
// caught a regression to that (verified: a probe with a real child process
// and monitor's cwd elsewhere gave watch project=sleep/service=sleep while
// investigate gave project=graphite/service=web-api for the SAME pid).
// Spawning a real child and reading its cwd from the OS (as
// recordAlertOccurrence and procbind.Inspect both now do) is what actually
// exercises the fix.
func TestWatchAndInvestigateAgreeOnProject(t *testing.T) {
	root := t.TempDir()
	repoRoot := filepath.Join(root, "graphite")
	serviceDir := filepath.Join(repoRoot, "web-api")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serviceDir, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// On macOS, t.TempDir() commonly lives under a symlinked $TMPDIR
	// (/var/folders/... -> /private/var/folders/...); the OS reports a
	// spawned process's cwd fully resolved, so resolve serviceDir the same
	// way before comparing against it, exactly as
	// TestResolveDefaultsDirToWorkingDirectory does for os.Getwd().
	if resolved, err := filepath.EvalSymlinks(serviceDir); err == nil {
		serviceDir = resolved
	}

	// Keep monitor's OWN cwd well outside the repo for the whole test, so a
	// regression to os.Getwd()-based derivation cannot pass by accident.
	outsideCwd := t.TempDir()
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(outsideCwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	cmd := exec.Command("sleep", "60")
	cmd.Dir = serviceDir
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	pid := int32(cmd.Process.Pid)

	// -- watch's call site: recordAlertOccurrence must read the ALERTED
	// process's own cwd via gopsutil (alertProcessCwd), never monitor's.
	watchPath := filepath.Join(t.TempDir(), "watch-issues.veclite")
	alert := collector.Alert{Rule: "rss_growth", Severity: "warning", Detail: "rss growing", PID: pid, Process: "sleep"}
	watchIssue, _, err := recordAlertOccurrence(context.Background(), watchPath, collector.Event{Timestamp: time.Now()}, alert, nil, incidents.CaptureResult{})
	if err != nil {
		t.Fatalf("recordAlertOccurrence: %v", err)
	}

	// -- investigate's call site: procbind.Inspect on the SAME pid.
	investigatePath := filepath.Join(t.TempDir(), "investigate-issues.veclite")
	t.Setenv("MONITOR_ISSUES_STORE", investigatePath)
	binding, err := procbind.Inspect(context.Background(), pid, "")
	if err != nil {
		t.Fatalf("procbind.Inspect: %v", err)
	}
	if binding.Cwd != serviceDir {
		t.Fatalf("binding.Cwd = %q, want %q (sanity check on the spawned child before comparing identities)", binding.Cwd, serviceDir)
	}
	report := investigateReport{
		PID: pid, StartedAt: "2026-01-02T03:00:00Z",
		Process: &binding,
	}
	investigateIssue, _, err := recordInvestigateOccurrence(&report)
	if err != nil {
		t.Fatalf("recordInvestigateOccurrence: %v", err)
	}

	if watchIssue.Project != "graphite" || watchIssue.Service != "web-api" {
		t.Fatalf("watch identity = project=%q service=%q, want project=graphite service=web-api", watchIssue.Project, watchIssue.Service)
	}
	if investigateIssue.Project != "graphite" || investigateIssue.Service != "web-api" {
		t.Fatalf("investigate identity = project=%q service=%q, want project=graphite service=web-api", investigateIssue.Project, investigateIssue.Service)
	}
	if watchIssue.Project != investigateIssue.Project || watchIssue.Service != investigateIssue.Service {
		t.Fatalf("watch and investigate disagree: watch=%+v investigate=%+v", watchIssue, investigateIssue)
	}
}
