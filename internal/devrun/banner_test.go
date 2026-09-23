package devrun

import (
	"strings"
	"testing"
	"time"
)

func TestStartBannerContainsKeyFields(t *testing.T) {
	line := StartBanner("node workload.js", 51002, "polyglot", "workload", ScanStderr)
	for _, want := range []string{bannerPrefix, "node workload.js", "pid 51002", "polyglot/workload", "scanning stderr"} {
		if !strings.Contains(line, want) {
			t.Errorf("StartBanner = %q, missing %q", line, want)
		}
	}
}

func TestStartBannerBothNotesStdoutIsAPipe(t *testing.T) {
	line := StartBanner("go-zap-stdout", 1, "polyglot", "ingest", ScanBoth)
	if !strings.Contains(line, "stdout is a pipe now") {
		t.Errorf("StartBanner(scan=both) = %q, want it to note stdout became a pipe", line)
	}
}

func TestStartBannerNoServiceOmitsSlash(t *testing.T) {
	line := StartBanner("node", 1, "polyglot", "", ScanStderr)
	if strings.Contains(line, "polyglot/") {
		t.Errorf("StartBanner with no service = %q, should not print a trailing slash", line)
	}
}

// R-minor-banner-argv: the banner shows a short, base-named command (the
// mockup's "node workload.js"), not a full absolute path.
func TestRenderCommandTextUsesBaseNameForArgv0(t *testing.T) {
	got := renderCommandText([]string{"/usr/local/bin/node", "examples/polyglot/js/workload.js"})
	if got != "node examples/polyglot/js/workload.js" {
		t.Errorf("renderCommandText = %q, want argv[0] reduced to its base name", got)
	}
}

func TestRenderCommandTextTruncatesLongArgv(t *testing.T) {
	long := strings.Repeat("x", commandTextMaxLen*2)
	got := renderCommandText([]string{"cmd", long})
	if len(got) > commandTextMaxLen {
		t.Errorf("renderCommandText length = %d, want at most %d", len(got), commandTextMaxLen)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("renderCommandText = %q, want a truncation marker", got)
	}
}

func TestRenderCommandTextEmptyArgv(t *testing.T) {
	if got := renderCommandText(nil); got != "" {
		t.Errorf("renderCommandText(nil) = %q, want empty", got)
	}
}

func TestNewIssueBannerFormat(t *testing.T) {
	line := NewIssueBanner(newIssueEvent{
		ShortID: "5C1D", Level: "handled", Title: "Error: flakyParse: malformed payload",
		File: "js/workload.js", Line: 31, Func: "flakyParse",
	})
	for _, want := range []string{"NEW", "5C1D", "handled", "js/workload.js:31", "flakyParse()"} {
		if !strings.Contains(line, want) {
			t.Errorf("NewIssueBanner = %q, missing %q", line, want)
		}
	}
}

func TestNewIssueBannerNoFuncOmitsParens(t *testing.T) {
	line := NewIssueBanner(newIssueEvent{ShortID: "C4E0", Level: "error", Title: "reconcile failed"})
	if strings.Contains(line, "()") {
		t.Errorf("NewIssueBanner with no Func = %q, should not print ()", line)
	}
}

func TestAgainBannerFormat(t *testing.T) {
	line := AgainBanner("5C1D", 4)
	if line != bannerPrefix+" 5C1D again (x4)" {
		t.Errorf("AgainBanner = %q, want %q", line, bannerPrefix+" 5C1D again (x4)")
	}
}

func TestExitSummaryNoIssuesNoDrops(t *testing.T) {
	line := ExitSummary(ExitSummaryInfo{CmdName: "node", ExitCode: 0, Duration: 3 * time.Second})
	for _, want := range []string{"node exited 0 after 3s", "0 new issues", "0 lines dropped"} {
		if !strings.Contains(line, want) {
			t.Errorf("ExitSummary = %q, missing %q", line, want)
		}
	}
	if strings.Contains(line, "next:") {
		t.Errorf("ExitSummary with no new issues = %q, should not suggest a next command", line)
	}
}

func TestExitSummaryWithNewIssuesSuggestsNext(t *testing.T) {
	line := ExitSummary(ExitSummaryInfo{
		CmdName:             "node",
		ExitCode:            1,
		Duration:            time.Second,
		NewIssueIDs:         []string{"A07E", "5C1D"},
		FirstNewIssueFullID: "ISS-A07E1234567890AB",
	})
	if !strings.Contains(line, "2 new issues (A07E, 5C1D)") {
		t.Errorf("ExitSummary = %q, want the (display-only, short) issue ids listed", line)
	}
	// The hint must be the FULL id, not the short display id: no command
	// can resolve a bare short id until E2.5's prefix resolution lands
	// (see FirstNewIssueFullID's doc comment), so a hint built from the
	// short id would be a dead end.
	if !strings.Contains(line, "next: monitor issue show ISS-A07E1234567890AB") {
		t.Errorf("ExitSummary = %q, want a next hint using the FULL id that resolves today", line)
	}
}

// TestExitSummaryNoNextHintWithoutAFullID guards the FirstNewIssueFullID ==
// "" case (should never happen alongside a non-empty NewIssueIDs in
// production, but ExitSummary must not print a broken "next: monitor issue
// show " with nothing after it if it ever does).
func TestExitSummaryNoNextHintWithoutAFullID(t *testing.T) {
	line := ExitSummary(ExitSummaryInfo{CmdName: "node", ExitCode: 1, Duration: time.Second, NewIssueIDs: []string{"A07E"}})
	if strings.Contains(line, "next:") {
		t.Errorf("ExitSummary = %q, must not print a next hint without a full id to point at", line)
	}
}

func TestExitSummaryDroppedLinesAreHonestUnderPressure(t *testing.T) {
	line := ExitSummary(ExitSummaryInfo{CmdName: "worker", ExitCode: 1, Duration: 12 * time.Minute, Dropped: 1284})
	if !strings.Contains(line, "1284 lines dropped") {
		t.Errorf("ExitSummary = %q, want the drop count reported", line)
	}
	if !strings.Contains(line, "terminal output intact") {
		t.Errorf("ExitSummary with drops = %q, want the honest-under-pressure note", line)
	}
}

// R-major-drop-label: dropped lines are a detector/channel throughput
// problem, not (necessarily) a store-contention one -- the label must not
// claim a specific cause it has no evidence for.
func TestExitSummaryDropLabelNamesDetectorNotStore(t *testing.T) {
	line := ExitSummary(ExitSummaryInfo{CmdName: "worker", ExitCode: 1, Duration: time.Second, Dropped: 5})
	if strings.Contains(line, "store busy; terminal") {
		t.Errorf("ExitSummary = %q, drop label must not blame the store without evidence", line)
	}
	if !strings.Contains(line, "detector behind; terminal output intact") {
		t.Errorf("ExitSummary = %q, want the detector-behind wording", line)
	}
}

// R-major-flush-budget: a failed store write (the store genuinely busy,
// per detector.flushShutdownBudget) is a DIFFERENT, separately reported
// fact from a dropped line.
func TestExitSummaryReportsFailedWrites(t *testing.T) {
	line := ExitSummary(ExitSummaryInfo{CmdName: "worker", ExitCode: 0, Duration: time.Second, FailedWrites: 2})
	if !strings.Contains(line, "2 issues not recorded (store busy)") {
		t.Errorf("ExitSummary = %q, want the failed-write count reported", line)
	}
}

func TestExitSummaryFailedWritesSingularNoun(t *testing.T) {
	line := ExitSummary(ExitSummaryInfo{CmdName: "worker", ExitCode: 0, Duration: time.Second, FailedWrites: 1})
	if !strings.Contains(line, "1 issue not recorded (store busy)") {
		t.Errorf("ExitSummary = %q, want the singular noun for exactly 1", line)
	}
}

func TestFormatDurationSubMinuteAndOver(t *testing.T) {
	if got := formatDuration(40100 * time.Millisecond); got != "40.1s" {
		t.Errorf("formatDuration(40.1s) = %q, want 40.1s", got)
	}
	if got := formatDuration(3*time.Minute + 2*time.Second); got != "3m2s" {
		t.Errorf("formatDuration(3m2s) = %q, want 3m2s", got)
	}
}
