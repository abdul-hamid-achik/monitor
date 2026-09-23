package explain

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// gitTimeout bounds every git subprocess this package runs (roadmap: "git
// blame -L n,n (2s timeout; skipped on shallow clones)").
const gitTimeout = 2 * time.Second

// lastTouchedFor runs `git blame -L line,line -- relFile` in root and turns
// the porcelain header into LastTouched. It never suggests a fix or blames a
// person for the bug (see LastTouched's doc comment: "last touched", never
// "suspect") -- it reports the fact and nothing more.
func lastTouchedFor(ctx context.Context, root, relFile string, line int) LastTouched {
	if root == "" {
		return LastTouched{Status: SectionSkipped, Detail: "no git root resolved"}
	}
	if relFile == "" || line <= 0 {
		return LastTouched{Status: SectionSkipped, Detail: "culprit has no file:line"}
	}
	if _, err := exec.LookPath("git"); err != nil {
		return LastTouched{Status: SectionSkipped, Detail: "git not on PATH", Recovery: "install git"}
	}

	if shallow, detail := isShallowClone(ctx, root); shallow {
		return LastTouched{Status: SectionSkipped, Detail: detail}
	}

	cctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	spec := fmt.Sprintf("%d,%d", line, line)
	cmd := exec.CommandContext(cctx, "git", "blame", "--porcelain", "-L", spec, "--", relFile)
	cmd.Dir = root
	cmd.WaitDelay = 500 * time.Millisecond
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if cctx.Err() == context.DeadlineExceeded {
			return LastTouched{Status: SectionSkipped, Detail: "git blame timed out after 2s"}
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return LastTouched{Status: SectionSkipped, Detail: "git blame: " + detail}
	}
	touched, ok := parseBlamePorcelain(stdout.String())
	if !ok {
		return LastTouched{Status: SectionSkipped, Detail: "git blame produced no output for this line"}
	}
	touched.Status = SectionOK
	return touched
}

// isShallowClone reports whether root is a shallow git clone -- git blame's
// history is truncated there, so a "last touched" answer would silently
// point at the shallow boundary commit instead of the real one.
func isShallowClone(ctx context.Context, root string) (bool, string) {
	cctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", "rev-parse", "--is-shallow-repository")
	cmd.Dir = root
	cmd.WaitDelay = 500 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		// Not a git repo, or git itself failed -- let the caller's own
		// `git blame` attempt (which will fail identically) produce the
		// user-facing detail instead of duplicating it here.
		return false, ""
	}
	if strings.TrimSpace(string(out)) == "true" {
		// Not "skipped: ..." -- the caller (lastTouchedFor) already prefixes
		// every skip Detail with "skipped: " when it renders (see internal/
		// cli/issues.go's writeSkippedLine); baking that prefix in here too
		// produced a literal "skipped: skipped: shallow clone ..." line.
		return true, "shallow clone (git rev-parse --is-shallow-repository = true)"
	}
	return false, ""
}

// parseBlamePorcelain extracts the SHA, author time, author email, and
// summary from `git blame --porcelain -L n,n`'s header. The porcelain
// format's first line is "<sha> <origline> <finalline> [<numlines>]",
// followed by tagged header lines ("author ...", "author-mail ...",
// "author-time ...", "summary ...") until the first line starting with a
// tab (the source line itself). ok is false when out doesn't even contain a
// commit line (an empty blame -- should not normally happen once the caller
// above already validated line count via readSnippet's own bound, but git
// itself is the final authority here).
func parseBlamePorcelain(out string) (LastTouched, bool) {
	lines := strings.Split(out, "\n")
	if len(lines) == 0 {
		return LastTouched{}, false
	}
	fields := strings.Fields(lines[0])
	if len(fields) == 0 || len(fields[0]) < 7 {
		return LastTouched{}, false
	}
	result := LastTouched{SHA: fields[0]}
	var authorTimeUnix int64
	var authorEmail string
	for _, line := range lines[1:] {
		if strings.HasPrefix(line, "\t") {
			break
		}
		switch {
		case strings.HasPrefix(line, "author-mail "):
			authorEmail = strings.Trim(strings.TrimPrefix(line, "author-mail "), "<>")
		case strings.HasPrefix(line, "author-time "):
			if v, err := strconv.ParseInt(strings.TrimPrefix(line, "author-time "), 10, 64); err == nil {
				authorTimeUnix = v
			}
		case strings.HasPrefix(line, "summary "):
			result.Subject = strings.TrimPrefix(line, "summary ")
		}
	}
	if authorTimeUnix > 0 {
		t := time.Unix(authorTimeUnix, 0).UTC()
		result.AuthorTime = &t
	}
	result.AuthorEmail = authorEmail
	if isZeroGitSHA(result.SHA) {
		// git's own porcelain fields for an uncommitted line are a
		// placeholder, not real commit metadata ("Version of <file> from
		// <file>" as the summary, "not.committed.yet" as the author-mail) --
		// reporting them as if SHA were a real commit is misleading (see
		// isStale, which treats this SHA as always-stale). Replace the
		// summary with an honest label instead; author identity for an
		// uncommitted line is never meaningful, so it is dropped rather
		// than surfaced as "not.committed.yet".
		result.Subject = "uncommitted change (not yet committed)"
		result.AuthorEmail = ""
	}
	return result, true
}

// stripAuthorEmail returns a copy of lt with AuthorEmail cleared -- applied
// at brief budget (docs/contracts/issue-context-v1.md: "no author email in
// brief").
func stripAuthorEmail(lt LastTouched) LastTouched {
	lt.AuthorEmail = ""
	return lt
}

// zeroGitSHA is `git blame`'s own placeholder commit for a line that has not
// been committed yet (see `git help blame`'s porcelain format) -- porcelain
// output always prints the full 40 hex characters, so this is length-exact
// rather than a prefix check.
const zeroGitSHA = "0000000000000000000000000000000000000000"

// isZeroGitSHA reports whether sha is git's all-zero "uncommitted change"
// placeholder (see isStale's doc comment).
func isZeroGitSHA(sha string) bool {
	return sha == zeroGitSHA
}

// gitIsAncestor reports whether ancestor is an ancestor of (or equal to)
// descendant in root, via `git merge-base --is-ancestor`. Any failure --
// timeout, a SHA git doesn't have (a shallow clone missing history, an
// invalid FirstGitSHA), or git itself erroring -- reports false (not an
// ancestor) rather than panicking or blocking isStale's caller: an
// inconclusive check degrades to "possibly stale", never a hang.
func gitIsAncestor(ctx context.Context, root, ancestor, descendant string) bool {
	if root == "" || ancestor == "" || descendant == "" {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", "merge-base", "--is-ancestor", ancestor, descendant)
	cmd.Dir = root
	cmd.WaitDelay = 500 * time.Millisecond
	return cmd.Run() == nil
}

// gitFileDiffers reports whether relFile's current working-tree content
// differs from its content at rev, via `git diff --quiet`. Any failure
// (timeout, rev unknown to this clone, relFile not present at rev) reports
// true (treat it as differing / possibly stale) rather than false: an
// inconclusive check must never suppress a real staleness signal.
func gitFileDiffers(ctx context.Context, root, rev, relFile string) bool {
	if root == "" || rev == "" || relFile == "" {
		return true
	}
	cctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", "diff", "--quiet", rev, "--", relFile)
	cmd.Dir = root
	cmd.WaitDelay = 500 * time.Millisecond
	err := cmd.Run()
	if err == nil {
		return false // `git diff --quiet` exits 0 when there is no difference
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return true // exit 1: a real difference, not a failure
	}
	return true // timeout, rev/path unknown to this clone, or another git error
}
