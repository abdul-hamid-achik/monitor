package devrun

import (
	"fmt"
	"strings"
	"time"
)

// bannerPrefix is exactly the roadmap's UX mockups' one-line banner prefix
// ("monitor > ..."), always written to stderr, never mixed into the
// monitored child's own stdout/stderr passthrough.
const bannerPrefix = "monitor >"

// StartBanner is the one line printed once the child has started (after
// its PID is known): "monitor > <cmd> · pid <p> · <project>[/<service>] ·
// scanning <streams> · crashes -> issues (ctrl-c stops both)".
func StartBanner(argv []string, pid int, projectSlug, service, scan string) string {
	ident := projectSlug
	if service != "" {
		ident = projectSlug + "/" + service
	}
	return fmt.Sprintf("%s %s · pid %d · %s · scanning %s · crashes -> issues (ctrl-c stops both)",
		bannerPrefix, strings.Join(argv, " "), pid, ident, scanDescription(scan))
}

// scanDescription renders --scan for the start banner; "both" additionally
// notes that stdout becomes a pipe (isatty changes for the child), matching
// the roadmap's "--scan both" mockup.
func scanDescription(scan string) string {
	if scan == ScanBoth {
		return "stderr+stdout (stdout is a pipe now)"
	}
	return scan
}

// NewIssueBanner is printed the first time this run records a given
// fingerprint: "monitor > NEW <short> <level> <title> <file>:<line>
// <func>()".
func NewIssueBanner(ev newIssueEvent) string {
	loc := ev.File
	if ev.Line > 0 {
		loc = fmt.Sprintf("%s:%d", ev.File, ev.Line)
	}
	fnPart := ""
	if ev.Func != "" {
		fnPart = " " + ev.Func + "()"
	}
	level := ev.Level
	if level == "" {
		level = "error"
	}
	return fmt.Sprintf("%s NEW %s %s %s %s%s", bannerPrefix, ev.ShortID, level, ev.Title, loc, fnPart)
}

// AgainBanner is printed every time a coalescing window flushes a repeat of
// a fingerprint this run has already reported: "monitor > <short> again
// (xN)".
func AgainBanner(shortID string, count int64) string {
	return fmt.Sprintf("%s %s again (x%d)", bannerPrefix, shortID, count)
}

// ExitSummaryInfo is ExitSummary's input.
type ExitSummaryInfo struct {
	CmdName     string
	ExitCode    int
	Duration    time.Duration
	NewIssueIDs []string
	Dropped     int64
}

// ExitSummary is the one line printed once the child has exited: "monitor
// > <cmd> exited <code> after <dur> · <n> new issues (<ids>) · <d> lines
// dropped · next: monitor issue <id>" -- honest under pressure: when lines
// were actually dropped, it says so and notes the terminal output stayed
// intact (the golden rule this whole package exists to keep).
func ExitSummary(info ExitSummaryInfo) string {
	issuesPart := pluralCount(len(info.NewIssueIDs), "new issue")
	if len(info.NewIssueIDs) > 0 {
		issuesPart += " (" + strings.Join(info.NewIssueIDs, ", ") + ")"
	}
	dropPart := fmt.Sprintf("%d lines dropped", info.Dropped)
	if info.Dropped > 0 {
		dropPart += " (store busy; terminal output intact)"
	}
	line := fmt.Sprintf("%s %s exited %d after %s · %s · %s",
		bannerPrefix, info.CmdName, info.ExitCode, formatDuration(info.Duration), issuesPart, dropPart)
	if len(info.NewIssueIDs) > 0 {
		line += " · next: monitor issue " + strings.ToLower(info.NewIssueIDs[0])
	}
	return line
}

func pluralCount(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// formatDuration renders a run's wall time roughly like the roadmap's
// mockups ("40.1s", "3m2s"): sub-second precision below a minute, whole
// seconds above it.
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return d.Round(100 * time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}
