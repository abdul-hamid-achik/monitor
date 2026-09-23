package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/incidents"
	"github.com/abdul-hamid-achik/monitor/internal/procbind"
)

// TestWatchAndInvestigateAgreeOnProject is the E1.4 done-when criterion:
// watch --stash and investigate must derive the SAME project/service
// identity for the same process, so the same underlying problem never
// splits into two separate issues (bug 15). A pid/cwd inside a monorepo
// like <tmp>/graphite/web-api/package.json, with .git at <tmp>/graphite,
// must give project=graphite service=web-api from both call sites.
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

	// -- watch's call site: recordAlertOccurrence reads os.Getwd() itself,
	// so simulate the process being launched from inside the service dir.
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(serviceDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	watchPath := filepath.Join(t.TempDir(), "watch-issues.veclite")
	alert := collector.Alert{Rule: "rss_growth", Severity: "warning", Detail: "rss growing", PID: 4242, Process: "node"}
	watchIssue, _, err := recordAlertOccurrence(context.Background(), watchPath, collector.Event{Timestamp: time.Now()}, alert, nil, incidents.CaptureResult{})
	if err != nil {
		t.Fatalf("recordAlertOccurrence: %v", err)
	}

	// -- investigate's call site: the process binding carries its own Cwd.
	investigatePath := filepath.Join(t.TempDir(), "investigate-issues.veclite")
	t.Setenv("MONITOR_ISSUES_STORE", investigatePath)
	report := investigateReport{
		PID: 4242, StartedAt: "2026-01-02T03:00:00Z",
		Process: &procbind.Binding{Name: "node", Runtime: procbind.RuntimeNode, Cwd: serviceDir, CodebaseRoot: serviceDir},
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
