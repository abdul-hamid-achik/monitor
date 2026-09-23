package devrun

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// bannerPrefix is exactly the roadmap's UX mockups' one-line banner prefix
// ("monitor > ..."), always written to stderr, never mixed into the
// monitored child's own stdout/stderr passthrough.
const bannerPrefix = "monitor >"

// commandTextMaxLen bounds the rendered command text in the start banner
// (renderCommandText): the roadmap's own mockup shows a short "node
// workload.js", not a full absolute path plus every argument, and an
// unbounded argv (a long --header "Authorization: Bearer ..." on the
// command line, say) has no business filling the whole banner line -- it
// still goes through the caller's scrubber (see devrun.go's Run) before
// this ever renders, but truncating keeps the common case matching the UX
// mockup regardless.
const commandTextMaxLen = 72

// renderCommandText renders argv for the start banner: argv[0] as its base
// name (not a full, possibly-absolute path -- the mockup shows "node
// workload.js", not "/usr/local/bin/node .../workload.js"), the rest
// joined as typed, the whole thing capped at commandTextMaxLen. The caller
// (devrun.go's Run) scrubs the result for secret-shaped values before it is
// ever printed; this function only formats.
func renderCommandText(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	parts := append([]string{filepath.Base(argv[0])}, argv[1:]...)
	text := strings.Join(parts, " ")
	if len(text) <= commandTextMaxLen {
		return text
	}
	const ellipsis = "…"
	return text[:commandTextMaxLen-len(ellipsis)] + ellipsis
}

// StartBanner is the one line printed once the child has started (after
// its PID is known): "monitor > <cmd> · pid <p> · <project>[/<service>] ·
// scanning <streams> · crashes -> issues (ctrl-c stops both)". cmdText is
// already rendered and scrubbed by the caller (see renderCommandText and
// devrun.go's Run).
func StartBanner(cmdText string, pid int, projectSlug, service, scan string) string {
	ident := projectSlug
	if service != "" {
		ident = projectSlug + "/" + service
	}
	return fmt.Sprintf("%s %s · pid %d · %s · scanning %s · crashes -> issues (ctrl-c stops both)",
		bannerPrefix, cmdText, pid, ident, scanDescription(scan))
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
	// FirstNewIssueFullID is the first NEW issue's full "ISS-..." id this
	// run (Result.FirstNewIssueFullID). The "next:" hint below uses this,
	// not NewIssueIDs[0]: that display id is only the first 4 hex
	// characters (docs/contracts/issue-context-v1.md's short_id), and no
	// command can resolve a bare short id until E2.5's prefix resolution
	// lands -- `monitor issue <shortid>` today just prints the `issues`
	// group's help and exits 0, a silent dead end. The full id resolves
	// today via `monitor issue show <fullid>`.
	FirstNewIssueFullID string
	Dropped             int64
	// FailedWrites is how many detected exceptions could not be recorded
	// (Result.FailedWrites) -- a different failure than Dropped: a drop
	// means the raw line never reached the detector at all (see below), a
	// failed write means the detector saw and parsed it but the store
	// itself could not be reached in time (almost always another process
	// holding its writer lock past detector.flushShutdownBudget).
	FailedWrites int64
}

// ExitSummary is the one line printed once the child has exited: "monitor
// > <cmd> exited <code> after <dur> · <n> new issues (<short ids>) · <d>
// lines dropped · next: monitor issue show <full id>" -- honest under
// pressure: when lines were actually dropped, it says so and notes the
// terminal output stayed intact (the golden rule this whole package exists
// to keep). The short ids in the "new issues (...)" part are
// display-only; the "next:" hint deliberately uses the FULL id instead
// (see FirstNewIssueFullID) so copy-pasting it actually resolves today.
func ExitSummary(info ExitSummaryInfo) string {
	issuesPart := pluralCount(len(info.NewIssueIDs), "new issue")
	if len(info.NewIssueIDs) > 0 {
		issuesPart += " (" + strings.Join(info.NewIssueIDs, ", ") + ")"
	}
	dropPart := fmt.Sprintf("%d lines dropped", info.Dropped)
	if info.Dropped > 0 {
		// Line drops happen upstream of any store interaction (pump.go's
		// bounded channel overflowing under raw throughput, not a
		// contended writer lock -- that failure mode is FailedWrites
		// below), so the parenthetical names the actual bottleneck instead
		// of guessing at the store.
		dropPart += " (detector behind; terminal output intact)"
	}
	line := fmt.Sprintf("%s %s exited %d after %s · %s · %s",
		bannerPrefix, info.CmdName, info.ExitCode, formatDuration(info.Duration), issuesPart, dropPart)
	if info.FailedWrites > 0 {
		noun := "issue"
		if info.FailedWrites != 1 {
			noun = "issues"
		}
		line += fmt.Sprintf(" · %d %s not recorded (store busy)", info.FailedWrites, noun)
	}
	if info.FirstNewIssueFullID != "" {
		line += " · next: monitor issue show " + info.FirstNewIssueFullID
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
