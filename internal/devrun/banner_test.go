package devrun

import (
	"strings"
	"testing"
	"time"
)

func TestStartBannerContainsKeyFields(t *testing.T) {
	line := StartBanner([]string{"node", "workload.js"}, 51002, "polyglot", "workload", ScanStderr)
	for _, want := range []string{bannerPrefix, "node workload.js", "pid 51002", "polyglot/workload", "scanning stderr"} {
		if !strings.Contains(line, want) {
			t.Errorf("StartBanner = %q, missing %q", line, want)
		}
	}
}

func TestStartBannerBothNotesStdoutIsAPipe(t *testing.T) {
	line := StartBanner([]string{"go-zap-stdout"}, 1, "polyglot", "ingest", ScanBoth)
	if !strings.Contains(line, "stdout is a pipe now") {
		t.Errorf("StartBanner(scan=both) = %q, want it to note stdout became a pipe", line)
	}
}

func TestStartBannerNoServiceOmitsSlash(t *testing.T) {
	line := StartBanner([]string{"node"}, 1, "polyglot", "", ScanStderr)
	if strings.Contains(line, "polyglot/") {
		t.Errorf("StartBanner with no service = %q, should not print a trailing slash", line)
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
	line := ExitSummary(ExitSummaryInfo{CmdName: "node", ExitCode: 1, Duration: time.Second, NewIssueIDs: []string{"A07E", "5C1D"}})
	if !strings.Contains(line, "2 new issues (A07E, 5C1D)") {
		t.Errorf("ExitSummary = %q, want the issue ids listed", line)
	}
	if !strings.Contains(line, "next: monitor issue a07e") {
		t.Errorf("ExitSummary = %q, want a lowercase next hint for the first new issue", line)
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

func TestFormatDurationSubMinuteAndOver(t *testing.T) {
	if got := formatDuration(40100 * time.Millisecond); got != "40.1s" {
		t.Errorf("formatDuration(40.1s) = %q, want 40.1s", got)
	}
	if got := formatDuration(3*time.Minute + 2*time.Second); got != "3m2s" {
		t.Errorf("formatDuration(3m2s) = %q, want 3m2s", got)
	}
}
