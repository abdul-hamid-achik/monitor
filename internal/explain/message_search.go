package explain

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
)

// minSearchFragmentLen is the shortest literal fragment worth searching for
// (roadmap E2.8: "el fragmento literal más largo"). A shorter one is too
// generic to trust a single hit from (e.g. a bare "failed").
const minSearchFragmentLen = 6

// maxSearchFragmentLen caps the fragment handed to vecgrep/git grep -- a
// pathologically long message must never blow up the search command line or
// dominate the match on formatting noise instead of the actual words.
const maxSearchFragmentLen = 200

// dynamicRun matches a run of digits or a UUID -- the "template" parts of a
// message a source line's own literal format string would never contain
// verbatim (the interpolated batch id, IP, count, ...). Splitting a message
// on these runs isolates its literal, searchable fragments.
var dynamicRun = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|0x[0-9a-fA-F]+|[0-9]+`)

// fragmentTrim is punctuation left dangling at a fragment's edge once its
// neighboring dynamic run is cut away (a trailing ':' before a batch id, a
// leading '.' after a port number, quote marks around an interpolated
// value).
const fragmentTrim = " \t:,.\"'`()[]{}"

// longestLiteralFragment normalizes message to its "template" by cutting out
// every dynamic run, then returns the longest surviving literal piece
// (trimmed of stray punctuation), bounded to maxSearchFragmentLen. Returns
// "" when nothing survives that clears minSearchFragmentLen.
func longestLiteralFragment(message string) string {
	message = strings.Join(strings.Fields(message), " ") // collapse whitespace/newlines
	parts := dynamicRun.Split(message, -1)
	best := ""
	for _, p := range parts {
		p = strings.Trim(p, fragmentTrim)
		if len(p) > len(best) {
			best = p
		}
	}
	if len(best) > maxSearchFragmentLen {
		best = best[:maxSearchFragmentLen]
	}
	if len(best) < minSearchFragmentLen {
		return ""
	}
	return best
}

// messageSearchResult is culpritForMessage's internal finding before it is
// turned into a CulpritInfo (which also needs a snippet read + optional
// codemap symbol lookup, both done by the caller so this stays a pure
// "find a file:line" step).
type messageSearchResult struct {
	File string
	Line int
	Via  string // vecgrep | git_grep
}

// culpritForMessage implements E2.8: when an exception has no in_app frame
// anywhere in its chain, normalize its message to a template, take the
// longest literal fragment, and search for it -- vecgrep's keyword (BM25)
// mode when the project's branch index is ready, otherwise `git grep -F`.
// Returns (nil, degraded) when the message was too generic to search, no
// search tool produced a hit, or vecgrep/git themselves were unavailable;
// degraded is nil only alongside a non-nil result.
func culpritForMessage(ctx context.Context, root, message string) (*messageSearchResult, *Degraded) {
	fragment := longestLiteralFragment(message)
	if fragment == "" {
		return nil, &Degraded{
			Component: "message_search",
			State:     "skipped",
			Detail:    "message has no literal fragment long enough to search (all-dynamic or too short)",
		}
	}
	if root == "" {
		return nil, &Degraded{Component: "message_search", State: "skipped", Detail: "no git root resolved"}
	}

	vecgrepHealth := ecosystem.ProbeVecgrep(ctx, root)
	// vecgrepDegraded is informational, not blocking: reported alongside a
	// successful git_grep fallback too, per the roadmap ("en una rama sin
	// índice, degraded incluye la recuperación") -- an agent reading a
	// perfectly good message_search culprit should still learn that vecgrep
	// itself isn't ready, and how to fix that, rather than that fact being
	// silently swallowed by git_grep's success.
	var vecgrepDegraded *Degraded
	if vecgrepHealth.State != ecosystem.HealthOK {
		vecgrepDegraded = &Degraded{
			Component: "vecgrep", State: vecgrepHealth.State,
			Detail: vecgrepHealth.Detail, Recovery: vecgrepHealth.Recovery,
		}
	} else {
		hits, err := ecosystem.KeywordSearch(ctx, root, fragment, 3)
		if err == nil && len(hits) > 0 {
			if r, ok := resultFromVecgrepHit(hits[0], fragment); ok {
				return r, nil
			}
		}
		// A healthy index that produced no usable hit still falls through to
		// git grep below rather than giving up -- vecgrep's chunk-level
		// index can legitimately miss a match git's exact line search finds
		// (a comment-only occurrence, a just-edited line not yet reindexed).
	}

	result, gitErr := gitGrepFragment(ctx, root, fragment)
	if gitErr == nil {
		return result, vecgrepDegraded
	}

	// Neither tool produced a hit: report whichever is the more informative
	// degradation -- vecgrep's own health when it isn't ready, else git's
	// failure.
	if vecgrepDegraded != nil {
		return nil, vecgrepDegraded
	}
	return nil, &Degraded{Component: "message_search", State: "skipped", Detail: gitErr.Error()}
}

// resultFromVecgrepHit turns a vecgrep hit into a messageSearchResult,
// refining StartLine to the exact line inside the hit's chunk that actually
// contains fragment when Content is available (a chunk commonly spans
// several lines; the fragment is rarely on its first one).
func resultFromVecgrepHit(hit ecosystem.VecgrepHit, fragment string) (*messageSearchResult, bool) {
	file := firstNonEmptyString(hit.RelativePath, hit.FilePath)
	if file == "" || hit.StartLine <= 0 {
		return nil, false
	}
	line := hit.StartLine
	if hit.Content != "" {
		for i, l := range strings.Split(hit.Content, "\n") {
			if strings.Contains(l, fragment) {
				line = hit.StartLine + i
				break
			}
		}
	}
	return &messageSearchResult{File: file, Line: line, Via: "vecgrep"}, true
}

// gitGrepFragment runs `git grep -n -F -e <fragment>` in root, capped at 2s,
// and returns the FIRST match as file:line (git grep's own output order,
// stable across otherwise-identical trees).
func gitGrepFragment(ctx context.Context, root, fragment string) (*messageSearchResult, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git not on PATH")
	}
	cctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", "grep", "-n", "-F", "-e", fragment)
	cmd.Dir = root
	cmd.WaitDelay = 500 * time.Millisecond
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if cctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("git grep timed out after 2s")
	}
	if err != nil {
		// git grep exits 1 for "no matches" (not a failure worth its own
		// message) and >1 for a real error.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil, fmt.Errorf("git grep found no match for the message fragment")
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("git grep: %s", detail)
	}
	file, line, ok := parseGitGrepFirstMatch(stdout.String())
	if !ok {
		return nil, fmt.Errorf("git grep produced no parseable match")
	}
	return &messageSearchResult{File: file, Line: line, Via: "git_grep"}, nil
}

// parseGitGrepFirstMatch reads the first "path:lineno:content" line from
// `git grep -n`'s output. A matched path containing a literal ':' (rare, but
// legal on POSIX filesystems) is handled by only ever splitting on the FIRST
// two colons, since git grep's own -n format is exactly path:lineno:rest.
func parseGitGrepFirstMatch(out string) (file string, line int, ok bool) {
	scanner := bufio.NewScanner(strings.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		l := scanner.Text()
		first := strings.IndexByte(l, ':')
		if first < 0 {
			continue
		}
		rest := l[first+1:]
		second := strings.IndexByte(rest, ':')
		if second < 0 {
			continue
		}
		n, err := strconv.Atoi(rest[:second])
		if err != nil || n <= 0 {
			continue
		}
		return l[:first], n, true
	}
	return "", 0, false
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
