// Package scrub redacts secrets from text before it is persisted, rendered
// with --md, or returned over MCP.
//
// Two independent mechanisms feed a Scrubber:
//
//   - Shape-based detectors (detectors.go) recognize secrets by their
//     structure — a Bearer header, a JWT, an AWS access key id, a PEM
//     private key block, a Luhn-valid card number, an email address, and so
//     on — regardless of where the text came from.
//   - Exact-value redaction (WithValues) additionally strips opaque secrets
//     that have no recognizable shape, such as a password value injected by
//     a secrets manager (tvault run -- monitor run -- cmd). The Scrubber is
//     given those literal values up front; it never inspects the process
//     environment itself.
//
// A Scrubber never stores or prints the secret values it was given, or the
// text it redacted from — it only counts how many redactions it performed,
// via Count. Detector tokens are stable strings like "[email]" or "[jwt]"
// so redacted output stays diffable across runs.
package scrub

import (
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"unicode/utf8"
)

// minValueLen is the shortest exact value WithValues will redact, counted
// in runes (see utf8.RuneCountInString in WithValues), not bytes. Shorter
// values (e.g. "no", "1234") are common substrings of ordinary text and
// would cause more harm as false positives than they prevent as secrets.
const minValueLen = 8

// Scrubber redacts secrets from text. The zero value is not usable; build
// one with New. A *Scrubber is safe for concurrent use: String and Count may
// be called from multiple goroutines (monitor run's terminal-copy and
// stacktrace-detector goroutines both see the same stream).
type Scrubber struct {
	// values holds exact secret values to redact, sorted longest-first so a
	// value that is itself a substring of another known secret never shadows
	// the longer redaction.
	values []string
	count  int64
}

// Option configures a Scrubber built by New.
type Option func(*Scrubber)

// New builds a Scrubber with the given options applied in order.
func New(opts ...Option) *Scrubber {
	s := &Scrubber{}
	for _, opt := range opts {
		opt(s)
	}
	sort.Slice(s.values, func(i, j int) bool { return len(s.values[i]) > len(s.values[j]) })
	return s
}

// allTokens lists every stable redaction token String can emit. WithValues
// skips a value that is a substring of one of these: without this check, a
// value that happens to spell part of its own replacement token (e.g. the
// literal word "redacted") would still match inside the token text it was
// just replaced with on a second pass, growing "[redacted]" into
// "[[redacted]]" instead of leaving already-redacted output alone.
var allTokens = []string{
	tokenEmail, tokenToken, tokenJWT, tokenAWSKey, tokenCard,
	tokenPrivateKey, tokenURLCreds, tokenValue,
}

// WithValues adds exact secret values to redact by literal substring match,
// independent of the shape-based detectors. A value is ignored when it is
// shorter than 8 characters (counted in runes, not bytes, so a short
// multi-byte value like a 4-character CJK password is still ignored) or
// when it is itself a substring of a stable redaction token, since either
// would do more harm as a false-positive/self-collision than it prevents as
// a secret. Callers typically pass the result of SecretEnvValues.
func WithValues(vals []string) Option {
	return func(s *Scrubber) {
		for _, v := range vals {
			if utf8.RuneCountInString(v) < minValueLen {
				continue
			}
			if isSubstringOfAnyToken(v) {
				continue
			}
			s.values = append(s.values, v)
		}
	}
}

// isSubstringOfAnyToken reports whether v appears inside one of the stable
// redaction tokens (see allTokens).
func isSubstringOfAnyToken(v string) bool {
	for _, tok := range allTokens {
		if strings.Contains(tok, v) {
			return true
		}
	}
	return false
}

// String returns s with every recognized secret replaced by a stable token.
// It never returns or logs the original secret text; Count reports how many
// redactions were made across every call on this Scrubber.
func (s *Scrubber) String(input string) string {
	out := s.redactKnownValues(input)
	out = scrubSegments(out, s.redactShapes)
	return out
}

// redactShapes runs every shape-based detector, in order, over one segment
// of text. It is applied per-segment by scrubSegments rather than once over
// the whole input — see splitTokenRE's doc comment for why.
func (s *Scrubber) redactShapes(seg string) string {
	seg = redactPrivateKeys(seg, &s.count)
	seg = redactURLCredentials(seg, &s.count)
	seg = redactBearerTokens(seg, &s.count)
	seg = redactJWTs(seg, &s.count)
	seg = redactAWSKeys(seg, &s.count)
	seg = redactGitHubTokens(seg, &s.count)
	seg = redactSlackTokens(seg, &s.count)
	seg = redactStripeKeys(seg, &s.count)
	seg = redactCards(seg, &s.count)
	seg = redactEmails(seg, &s.count)
	return seg
}

// splitTokenRE matches the two kinds of sequence that glue a "word"
// character (Go regexp's \b only knows [0-9A-Za-z_]) onto whatever text
// follows them, hiding the left-hand boundary every detector above needs:
//
//   - an ANSI CSI sequence, such as the color code "\x1b[31m" a captured
//     stderr line commonly wraps a secret in — the final byte of a CSI
//     sequence (here 'm') is itself a word character, so "\x1b[31mAKIA..."
//     has no boundary between the 'm' and the 'A'.
//   - a JSON/C string escape pair, such as "\n" or "\t" (the two literal
//     characters backslash-then-letter, not an actual control byte) — the
//     escape's second character is a word letter, so
//     `"msg":"failed\nAKIA..."` has no boundary between the 'n' and the 'A'
//     either, and additionally (for the email detector, whose local-part
//     class overlaps ordinary letters) can pull that 'n' into the match and
//     leave a dangling, unparseable "\[" behind.
//
// scrubSegments splits the input on these sequences, redacts only the plain
// text in between (where a genuine boundary exists at position 0 of every
// segment), and reassembles the escape/CSI sequences byte-for-byte
// untouched.
var splitTokenRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]` + `|` + `\\[nrt"\\]`)

// scrubSegments calls fn on the plain-text portions of input, leaving any
// ANSI CSI sequence or JSON/C escape pair (see splitTokenRE) untouched and
// reassembling the result. When input contains neither (the overwhelming
// common case for a line with no secret at all), it skips straight to
// fn(input) without ever running splitTokenRE, so this costs two cheap
// IndexByte scans on the hot path.
func scrubSegments(input string, fn func(string) string) string {
	if strings.IndexByte(input, 0x1b) < 0 && strings.IndexByte(input, '\\') < 0 {
		return fn(input)
	}
	matches := splitTokenRE.FindAllStringIndex(input, -1)
	if len(matches) == 0 {
		return fn(input)
	}
	var b strings.Builder
	b.Grow(len(input))
	last := 0
	for _, m := range matches {
		if m[0] > last {
			b.WriteString(fn(input[last:m[0]]))
		}
		b.WriteString(input[m[0]:m[1]])
		last = m[1]
	}
	if last < len(input) {
		b.WriteString(fn(input[last:]))
	}
	return b.String()
}

// Count returns the number of redactions performed so far by this Scrubber,
// across every call to String. It never exposes the redacted values.
func (s *Scrubber) Count() int {
	return int(atomic.LoadInt64(&s.count))
}

// redactKnownValues replaces every exact occurrence of a WithValues secret
// with tokenValue. It runs before the shape-based detectors so a known
// secret is always caught even when its shape would otherwise confuse a
// detector (e.g. a password that happens to look like part of a URL).
func (s *Scrubber) redactKnownValues(input string) string {
	if len(s.values) == 0 {
		return input
	}
	out := input
	for _, v := range s.values {
		n := 0
		out = replaceAllExceptURLScheme(out, v, tokenValue, &n)
		if n > 0 {
			atomic.AddInt64(&s.count, int64(n))
		}
	}
	return out
}

// schemeSep is the delimiter whose presence after a value marks that value
// as a URL scheme rather than a secret occurrence (SEC-1).
const schemeSep = "://"

// replaceAllExceptURLScheme replaces every occurrence of v in s with repl,
// counting replacements into *n -- except an occurrence immediately
// followed by "://", which is a URL scheme the value happens to equal
// (SEC-1: with POSTGRES_PASSWORD=postgres, the "postgres" in
// "postgres://u:pw@host/db" is the scheme, not the password). Redacting it
// would destroy the scheme redactURLCredentials needs to see in the next
// pass, leaving the URL's userinfo -- the actual password -- in plain text.
// Keeping the scheme lets redactURLCredentials replace the whole userinfo.
func replaceAllExceptURLScheme(s, v, repl string, n *int) string {
	var b strings.Builder
	// last marks s[:last] as already emitted (once started); from is the
	// search position. A skipped scheme advances from but NOT last, so
	// its own bytes are still emitted with the next replacement's
	// prefix -- otherwise a scheme before a real secret occurrence
	// would silently swallow the scheme text.
	last := 0
	from := 0
	count := 0
	started := false
	for {
		i := strings.Index(s[from:], v)
		if i < 0 {
			break
		}
		i += from
		end := i + len(v)
		if strings.HasPrefix(s[end:], schemeSep) {
			// A scheme-shaped occurrence: leave it for the prefix
			// of the next replacement (or the tail below).
			from = end
			continue
		}
		if !started {
			started = true
			b.Grow(len(s))
		}
		b.WriteString(s[last:i])
		b.WriteString(repl)
		last = end
		from = end
		count++
	}
	*n = count
	if !started {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}
