package explain

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
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

// quotedRun matches a single- or double-quoted span -- the OTHER common
// shape of an interpolated runtime value a source line's format string
// would never contain verbatim: Python's `f"...{token!r}"` and `%r` render
// the value already wrapped in quotes, a Go %q does the same, and many
// loggers quote a bare string field. Without stripping these, a fragment
// like "malformed payload near token 'bad-payl'" only ever exact-matches
// wherever that specific interpolated value happens to appear verbatim
// (typically nowhere in source, but often in a test/golden fixture that
// merely asserts against a recorded example of the rendered message --
// giving the WRONG culprit for the right reason).
var quotedRun = regexp.MustCompile(`'[^']*'|"[^"]*"`)

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
	// Strip quoted spans BEFORE the digit/UUID split (both are "dynamic":
	// see quotedRun's doc comment) using a marker byte no source message
	// legitimately contains, then split on either.
	const marker = "\x00"
	normalized := quotedRun.ReplaceAllString(message, marker)
	normalized = dynamicRun.ReplaceAllString(normalized, marker)
	parts := strings.Split(normalized, marker)
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
			if r, ok := bestVecgrepResult(hits, fragment); ok {
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

// testyPathSegment matches a path component that marks a file as tests,
// fixtures, golden data, or specs -- the kind of file whose CONTENT commonly
// reproduces a real runtime message verbatim (a golden fixture, a recorded
// log sample used as expected output) without being the line that actually
// PRINTED it. Deliberately does not exclude "examples" or "docs": monitor's
// own dogfood fixtures live under examples/polyglot and ARE legitimate
// culprit source there.
var testyPathSegment = regexp.MustCompile(`(?i)(^|/)(test|tests|testdata|__tests__|__mocks__|spec|specs|fixtures?|golden)(/|$)`)

// testyFileSuffix matches a filename shape that marks it as a test, spec, or
// plain-text/markdown document rather than source: Go's `_test.go`,
// JS/TS's `.test.*`/`.spec.*`, Python's `test_*.py`, and `.md`/`.txt`/`.rst`.
var testyFileSuffix = regexp.MustCompile(`(?i)(_test|\.test|\.spec|_spec)\.[a-zA-Z0-9]+$|(^|/)test_[^/]+\.[a-zA-Z0-9]+$|\.(md|txt|rst|adoc)$`)

// isLikelySourceNoise reports whether path looks like a test, fixture, spec,
// or doc file rather than the application source E2.8's message-search
// culprit is supposed to point at (see testyPathSegment/testyFileSuffix).
func isLikelySourceNoise(path string) bool {
	p := filepath.ToSlash(path)
	return testyPathSegment.MatchString(p) || testyFileSuffix.MatchString(p)
}

// bestVecgrepResult turns hits into a messageSearchResult, returning the
// first hit whose file does not look like test/fixture noise (see
// isLikelySourceNoise) -- vecgrep's own ranking has no notion of "is
// this actually source", so a golden fixture that scores well on the
// query text can otherwise outrank the real source line. When EVERY hit
// looks noisy it returns no result at all (LUX-2): a message fragment
// only a test or golden fixture reproduces is not evidence about the
// code that printed it, and the caller falls through to git grep (and
// then to an honest degraded note) instead of blaming the fixture.
func bestVecgrepResult(hits []ecosystem.VecgrepHit, fragment string) (*messageSearchResult, bool) {
	for _, hit := range hits {
		r, ok := resultFromVecgrepHit(hit, fragment)
		if !ok {
			continue
		}
		if !isLikelySourceNoise(r.File) {
			return r, true
		}
	}
	return nil, false
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
// and returns the best match as file:line: the first hit (git grep's own
// output order, stable across otherwise-identical trees) that does not look
// like a test/fixture/spec file (see isLikelySourceNoise). When every match
// looks noisy it returns an error rather than a result (LUX-2): blaming a
// golden fixture that merely reproduces the rendered message misleads more
// than reporting no culprit, and the error text names the noise so the
// degraded note a caller builds from it says the search ran and found only
// fixtures.
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
	matches, ok := parseGitGrepMatches(stdout.String())
	if !ok {
		return nil, fmt.Errorf("git grep produced no parseable match")
	}
	match, ok := preferSourceMatch(matches)
	if !ok {
		return nil, fmt.Errorf("git grep matched only test/fixture/spec noise for the message fragment")
	}
	return &messageSearchResult{File: match.File, Line: match.Line, Via: "git_grep"}, nil
}

// gitGrepMatch is one "path:lineno" pair parsed from `git grep -n`'s output.
type gitGrepMatch struct {
	File string
	Line int
}

// parseGitGrepMatches reads every "path:lineno:content" line from `git
// grep -n`'s output, in the order git printed them. A matched path
// containing a literal ':' (rare, but legal on POSIX filesystems) is
// handled by only ever splitting on the FIRST two colons, since git grep's
// own -n format is exactly path:lineno:rest.
func parseGitGrepMatches(out string) ([]gitGrepMatch, bool) {
	var matches []gitGrepMatch
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
		matches = append(matches, gitGrepMatch{File: l[:first], Line: n})
	}
	return matches, len(matches) > 0
}

// preferSourceMatch returns the first match whose file does not look like
// test/fixture/spec noise (see isLikelySourceNoise), and NO match when
// every one of them looks noisy (LUX-2): a message fragment that only a
// test or golden fixture reproduces is not evidence about the code that
// PRINTED it, and blaming the fixture misleads more than reporting no
// culprit -- Source "message_search" is already confidence "low", and
// gitGrepFragment's error text says the search ran and found only noise.
func preferSourceMatch(matches []gitGrepMatch) (gitGrepMatch, bool) {
	for _, m := range matches {
		if !isLikelySourceNoise(m.File) {
			return m, true
		}
	}
	return gitGrepMatch{}, false
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
