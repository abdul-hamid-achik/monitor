package scrub

import (
	"regexp"
	"strings"
	"sync/atomic"
)

// Stable replacement tokens. These never change shape so redacted output
// stays diffable across reprocessing runs.
const (
	tokenEmail      = "[email]"
	tokenToken      = "[token]"
	tokenJWT        = "[jwt]"
	tokenAWSKey     = "[aws-key]"
	tokenCard       = "[card]"
	tokenPrivateKey = "[private-key]"
	tokenURLCreds   = "[url-credentials]"
	// tokenValue replaces an exact WithValues secret. It is deliberately
	// distinct from the shape-based tokens above: an opaque secrets-manager
	// value has no "kind" a detector could name.
	tokenValue = "[redacted]"
)

// Detector patterns are anchored with \b so they only match at a
// word/non-word boundary. Because Go's RE2 treats letters, digits and
// underscore as "word" characters, a boundary never appears in the middle of
// a contiguous hex run — this is what keeps the card detector below from
// tripping on git SHAs and UUIDs, whose hex digits and letters share a
// single run with no boundary between them, and what keeps a URL with a
// port, like https://host:8080/@scope/pkg, from being misread as
// credentials: the character class used for the userinfo scan stops at the
// first '/', so a path segment after the authority is never consumed.
//
// A leading \b only recognizes a transition between an ASCII word character
// ([0-9A-Za-z_]) and anything else. That means it does NOT fire between two
// word characters even when one of them came from something else entirely —
// an ANSI SGR reset ("\x1b[0m", which ends in the word character 'm') or a
// JSON/C escape pair ("\n", "\t", whose second character is a word letter).
// Scrubber.String handles that by splitting the input around those
// sequences (splitTokenRE, in scrub.go) before running any detector, so each
// segment handed to the functions below always starts at a genuine
// left-hand boundary. The prefix-anchored detectors (AWS/GitHub/Slack/
// Stripe) additionally drop the leading \b outright: their prefixes are
// distinctive enough that a preceding word character (as in "KEY_AKIA...")
// should not hide them.
//
// Every detector below is guarded by a cheap literal substring check (see
// the redact* functions) before its regexp ever runs. A regexp scan of a
// ~1KB line costs low tens of microseconds even for a pattern that never
// matches, because \b forces Go's regexp engine onto its general-purpose
// machine instead of a literal-prefix fast path; a plain strings.Contains
// pre-check is a tight byte scan that costs a few dozen nanoseconds. Since
// the overwhelmingly common case for any single detector on any single line
// is "this secret shape is not present at all", the guard is what keeps
// Scrubber.String well under the 50µs/op budget BenchmarkScrubberString1KB
// checks — not the regexes themselves, which are unavoidably not free once
// their literal anchor is actually present. The JWT guard checks for the
// literal "eyJ" (the base64url encoding of the JSON header's leading '{"')
// rather than "two dots somewhere", which both fixes the false positives
// below and means most lines (no "eyJ" substring at all) skip the regexp
// entirely.
var (
	// PEM private key blocks. DOTALL (?s) lets '.' cross newlines; the body
	// is matched non-greedily so back-to-back blocks are redacted
	// separately rather than swallowed into one match. The optional " BLOCK"
	// covers PGP's "-----BEGIN PGP PRIVATE KEY BLOCK-----" framing in
	// addition to PKCS1/PKCS8's "-----BEGIN RSA PRIVATE KEY-----" etc.
	pemBlockRE = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----.*?-----END [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----`)

	// A lone BEGIN header with no matching END — a private key truncated by
	// a log's line limit or a capture that stopped mid-write. There is no
	// safe amount of a private key body to leave unredacted, so
	// redactPrivateKeys treats this as "redact from here to the end of the
	// input" rather than leaving the body exposed because no closing marker
	// was found.
	pemBeginOnlyRE = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----`)

	// Bearer <token>, case-insensitive scheme name (RFC 6750 doesn't require
	// "Bearer" to be cased a particular way in the wild). Captures the
	// original "Bearer"/"bearer"/"BEARER" spelling and separating
	// whitespace so only the token itself is replaced. The token requires at
	// least 16 characters, which is what keeps this from matching ordinary
	// English ("the bearer of bad news") or a WWW-Authenticate challenge
	// parameter ("Bearer realm=\"x\"") whose first token-shaped word is
	// short.
	bearerRE = regexp.MustCompile(`(?i)\b(Bearer)(\s+)([A-Za-z0-9._~+/=-]{16,})`)

	// A JWT's header and payload are always the base64url encoding of JSON
	// starting with '{"', which always encodes to a leading "eyJ". Anchoring
	// on that literal (rather than "any three long dot-separated runs") is
	// what keeps this from matching fully-qualified Java/Kotlin/.NET/Python
	// names like org.springframework...TransactionInterceptor.invoke, which
	// are exactly as dot-separated and long but never start a segment with
	// "eyJ".
	jwtRE = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{7,}\.eyJ[A-Za-z0-9_-]{7,}\.[A-Za-z0-9_-]{10,}\b`)

	// AWS access key ids: AKIA (long-term) or ASIA (STS temporary) followed
	// by 16 uppercase alphanumerics. No leading \b: AKIA/ASIA is specific
	// enough that a preceding word character (KEY_AKIA...) shouldn't hide
	// it. The trailing \b is kept so a longer alphanumeric run isn't cut
	// off mid-token.
	awsKeyRE = regexp.MustCompile(`(?:AKIA|ASIA)[A-Z0-9]{16}\b`)

	// GitHub fine-grained/classic tokens (ghp_, gho_, ghu_, ghs_, ghr_) and
	// github_pat_ tokens. No finite upper bound on the alnum run: a bounded
	// repeat like {20,255} forces Go's regexp compiler to replicate that
	// many copies of the character-class instruction, which is its own,
	// separate source of needless cost. No leading \b, for the same reason
	// as the AWS key above.
	githubTokenRE = regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}\b|github_pat_[A-Za-z0-9_]{20,}\b`)

	// Slack tokens: xoxb-, xoxp-, xoxa-, xoxr-, ... followed by dash-joined
	// alphanumeric segments. No leading \b.
	slackTokenRE = regexp.MustCompile(`xox[abpr]-[A-Za-z0-9-]{10,}\b`)

	// Stripe secret/restricted keys: sk_live_, sk_test_, rk_live_, rk_test_.
	// No leading \b.
	stripeKeyRE = regexp.MustCompile(`(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{10,}\b`)

	// Candidate credit-card runs: a leading digit then 12-18 more digits,
	// each optionally preceded by a single space or dash (so both grouped
	// "4111 1111 1111 1111" and ungrouped "4111111111111111" match). This
	// covers 13-19 total digits, the ISO/IEC 7812 PAN range. Every match is
	// still Luhn-checked and network-prefix-checked in redactCards before
	// being treated as a card — see cardNetworkPrefix and cardGroupingValid.
	cardCandidateRE = regexp.MustCompile(`\b\d(?:[ -]?\d){12,18}\b`)

	// A conventional local@domain.tld shape. \p{L} (any Unicode letter, not
	// just ASCII) in the local part and the domain labels means an accented
	// address like josé@example.com or müller@example.de redacts as one
	// whole match, rather than a \b splitting the run mid-address and
	// leaving a fragment of the local part unredacted (see the package
	// tests for why a plain [A-Za-z...] class does that). It still doesn't
	// attempt full RFC 5322, so it also matches some things that are not
	// emails: an SSH remote (git@github.com:org/repo, whose domain+TLD
	// shape is indistinguishable from a real one) and a dotted attribute
	// chain that happens to end in a short trailing segment
	// (x@self.weight.T redacts as x@self.weight, since "T" alone is too
	// short to be the final TLD segment and the preceding "weight" is not).
	// Both are accepted trade-offs of a shape-only detector, not something
	// this pattern tries to special-case.
	emailRE = regexp.MustCompile(`\b[\p{L}0-9._%+-]+@[\p{L}0-9-]+(?:\.[\p{L}0-9-]+)*\.[\p{L}]{2,}\b`)
)

// replaceToken redacts every match of re in s with token, incrementing
// counter once per match.
func replaceToken(re *regexp.Regexp, token string, s string, counter *int64) string {
	return re.ReplaceAllStringFunc(s, func(string) string {
		atomic.AddInt64(counter, 1)
		return token
	})
}

func redactPrivateKeys(s string, counter *int64) string {
	if !strings.Contains(s, "-----BEGIN") {
		return s
	}
	out := replaceToken(pemBlockRE, tokenPrivateKey, s, counter)
	// Any BEGIN marker that survived the full-block pass has no matching
	// END in this input — most likely a private key truncated by a log's
	// line/byte limit. Redact from there to the end rather than leave the
	// (partial) key body exposed.
	if loc := pemBeginOnlyRE.FindStringIndex(out); loc != nil {
		atomic.AddInt64(counter, 1)
		out = out[:loc[0]] + tokenPrivateKey
	}
	return out
}

// redactURLCredentials finds scheme://userinfo@ and replaces the userinfo
// with tokenURLCreds, preserving the scheme. It is a hand-scan rather than a
// regexp: for every "://" in s, it walks backward for a valid scheme
// ([A-Za-z][A-Za-z0-9+.-]*, at a genuine left boundary — not preceded by
// another word character) and, if one is found, walks forward to the first
// character that would end an authority (whitespace, '/', '?' or '#'),
// then takes the LAST '@' before that point as the end of the userinfo.
// Taking the last '@' (not the first) is what makes a password containing
// '@' (user:p@ssw0rd@host) redact as a whole instead of leaving the tail of
// the password behind, and allowing an empty userinfo before the ':' is what
// catches redis://:pw@host and a bare token used as the user
// (http://token@host/path).
func redactURLCredentials(s string, counter *int64) string {
	if !strings.Contains(s, "://") {
		return s
	}
	var b strings.Builder
	matched := false
	last := 0
	pos := 0
	for {
		i := strings.Index(s[pos:], "://")
		if i < 0 {
			break
		}
		schemeEnd := pos + i
		start := schemeEnd
		for start > 0 && isSchemeByte(s[start-1]) {
			start--
		}
		if start == schemeEnd || !isAlphaByte(s[start]) || (start > 0 && isWordByte(s[start-1])) {
			// No valid scheme immediately before this "://" (either no
			// scheme characters at all, the run doesn't start with a
			// letter, or it isn't at a left boundary) — move past it and
			// keep looking.
			pos = schemeEnd + 3
			continue
		}
		authStart := schemeEnd + 3
		end := authStart
		for end < len(s) && !isURLAuthorityStop(s[end]) {
			end++
		}
		at := strings.LastIndexByte(s[authStart:end], '@')
		if at < 0 {
			pos = end
			continue
		}
		atPos := authStart + at
		if !matched {
			b.Grow(len(s))
			matched = true
		}
		b.WriteString(s[last:schemeEnd])
		b.WriteString("://")
		b.WriteString(tokenURLCreds)
		b.WriteByte('@')
		atomic.AddInt64(counter, 1)
		last = atPos + 1
		pos = atPos + 1
	}
	if !matched {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// isSchemeByte reports whether c can appear after the first character of a
// URL scheme ([A-Za-z0-9+.-]).
func isSchemeByte(c byte) bool {
	return isAlphaByte(c) || (c >= '0' && c <= '9') || c == '+' || c == '.' || c == '-'
}

func isAlphaByte(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// isWordByte reports whether c is an RE2 "word" byte ([0-9A-Za-z_]) — used
// to replicate \b's left-boundary check by hand.
func isWordByte(c byte) bool {
	return isAlphaByte(c) || (c >= '0' && c <= '9') || c == '_'
}

// isURLAuthorityStop reports whether c ends a URL authority component:
// whitespace, or one of '/', '?', '#'.
func isURLAuthorityStop(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '/', '?', '#':
		return true
	}
	return false
}

func redactBearerTokens(s string, counter *int64) string {
	// Reject the overwhelming majority of lines (no "bearer" substring in
	// any casing) with a single alloc-free scan before ever considering the
	// regexp. containsFoldASCII, unlike a two-literal Contains check, covers
	// every casing (bEaReR, BeArEr, ...) RFC 6750 allows in the wild, not
	// only the two extremes.
	if !containsFoldASCII(s, "earer") {
		return s
	}
	return bearerRE.ReplaceAllStringFunc(s, func(m string) string {
		groups := bearerRE.FindStringSubmatch(m)
		atomic.AddInt64(counter, 1)
		return groups[1] + groups[2] + tokenToken
	})
}

func redactJWTs(s string, counter *int64) string {
	if !strings.Contains(s, "eyJ") {
		return s
	}
	return replaceToken(jwtRE, tokenJWT, s, counter)
}

func redactAWSKeys(s string, counter *int64) string {
	if !strings.Contains(s, "AKIA") && !strings.Contains(s, "ASIA") {
		return s
	}
	return replaceToken(awsKeyRE, tokenAWSKey, s, counter)
}

func redactGitHubTokens(s string, counter *int64) string {
	if !containsAny(s, "ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_") {
		return s
	}
	return replaceToken(githubTokenRE, tokenToken, s, counter)
}

func redactSlackTokens(s string, counter *int64) string {
	if !strings.Contains(s, "xox") {
		return s
	}
	return replaceToken(slackTokenRE, tokenToken, s, counter)
}

func redactStripeKeys(s string, counter *int64) string {
	if !strings.Contains(s, "sk_") && !strings.Contains(s, "rk_") {
		return s
	}
	return replaceToken(stripeKeyRE, tokenToken, s, counter)
}

// emailWindowBefore/After bound how far redactEmails looks around each '@'
// for a local-part/domain match, instead of running emailRE over the whole
// line. They are generous relative to any realistic email address (RFC
// 5321's own limits are 64 bytes for the local part and 255 for the
// domain), so this changes performance, not which addresses get matched, on
// any input built from real email-shaped text.
const (
	emailWindowBefore = 96
	emailWindowAfter  = 320
)

func redactEmails(s string, counter *int64) string {
	if !strings.Contains(s, "@") {
		return s
	}
	var b strings.Builder
	matched := false
	last := 0
	pos := 0
	for {
		rel := strings.IndexByte(s[pos:], '@')
		if rel < 0 {
			break
		}
		atPos := pos + rel
		winStart := atPos - emailWindowBefore
		if winStart < last {
			winStart = last
		}
		if winStart < 0 {
			winStart = 0
		}
		winEnd := atPos + emailWindowAfter
		if winEnd > len(s) {
			winEnd = len(s)
		}
		loc := emailRE.FindStringIndex(s[winStart:winEnd])
		if loc == nil {
			pos = atPos + 1
			continue
		}
		matchStart, matchEnd := winStart+loc[0], winStart+loc[1]
		if matchStart > atPos || matchEnd <= atPos {
			// The nearest match in this window doesn't actually cover the
			// '@' we're looking at (e.g. "@scope/pkg", which never forms a
			// full match at all) — move past it and keep looking.
			pos = atPos + 1
			continue
		}
		if !matched {
			b.Grow(len(s))
			matched = true
		}
		b.WriteString(s[last:matchStart])
		b.WriteString(tokenEmail)
		atomic.AddInt64(counter, 1)
		last = matchEnd
		pos = matchEnd
	}
	if !matched {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// redactCards replaces Luhn-valid digit runs (13-19 digits, optional space/
// dash separators) with tokenCard. A candidate that fails Luhn, doesn't
// start with a real card-network prefix, is every digit the same, or (when
// separators are present) isn't grouped the way real cards are printed, is
// left untouched — see cardNetworkPrefix and cardGroupingValid. That is what
// keeps this from firing on a pino-style epoch-ms timestamp, a Go-printed
// zero byte slice, a zero trace/span id, or a UUID's dash-separated tail,
// all of which can satisfy "13-19 digits, passes Luhn" by coincidence but
// none of which start with a prefix any card network actually issues.
func redactCards(s string, counter *int64) string {
	if countDigits(s) < 13 {
		return s
	}
	return cardCandidateRE.ReplaceAllStringFunc(s, func(m string) string {
		digits := stripCardSeparators(m)
		if !luhnValid(digits) {
			return m
		}
		if !cardNetworkPrefix(digits) {
			return m
		}
		if allSameDigit(digits) {
			return m
		}
		if strings.ContainsAny(m, " -") && !cardGroupingValid(m) {
			return m
		}
		atomic.AddInt64(counter, 1)
		return tokenCard
	})
}

// containsAny reports whether s contains any of subs, short-circuiting on
// the first hit.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// containsFoldASCII reports whether s contains sub, ASCII-case-insensitive,
// without allocating. sub must already be lowercase. This exists so a
// guard like the Bearer one can reject a line in a single pass regardless
// of how "bearer" happens to be cased (Bearer, bearer, BEARER, BeArEr, ...)
// instead of only recognizing the two extremes a strings.Contains pair
// would catch.
func containsFoldASCII(s, sub string) bool {
	n := len(sub)
	if n == 0 {
		return true
	}
	for i := 0; i+n <= len(s); i++ {
		j := 0
		for ; j < n; j++ {
			c := s[i+j]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != sub[j] {
				break
			}
		}
		if j == n {
			return true
		}
	}
	return false
}

// countDigits returns how many ASCII digit bytes are in s. It's a plain
// byte scan — much cheaper than running cardCandidateRE — used to skip that
// regexp entirely on lines that could not possibly contain a 13-19-digit
// card number.
func countDigits(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			n++
		}
	}
	return n
}

func stripCardSeparators(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == ' ' || r == '-' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// allSameDigit reports whether every character in digits is identical — a
// run of all zeros (a placeholder id) or all of any other digit is never a
// real card number, whatever its Luhn checksum says.
func allSameDigit(digits string) bool {
	for i := 1; i < len(digits); i++ {
		if digits[i] != digits[0] {
			return false
		}
	}
	return true
}

// cardNetworkPrefix reports whether digits starts with a real card-network
// IIN (issuer identification number) prefix: Visa (4), Mastercard (51-55 or
// the newer 2221-2720 range), Amex (34, 37), Diners Club (300-305, 36, 38),
// JCB (35), or Discover (6011, 65, 644-649). This is what rejects the
// overwhelming majority of unrelated 13-19-digit numbers that happen to
// pass Luhn — epoch-ms timestamps, zero ids, and so on — none of which
// start with one of these.
func cardNetworkPrefix(digits string) bool {
	if len(digits) < 2 {
		return false
	}
	d := func(i int) byte { return digits[i] - '0' }
	switch digits[0] {
	case '4':
		return true // Visa
	case '3':
		switch digits[1] {
		case '4', '7':
			return true // Amex
		case '0':
			if len(digits) >= 3 && digits[2] >= '0' && digits[2] <= '5' {
				return true // Diners Club 300-305
			}
		case '6', '8':
			return true // Diners Club 36, 38
		case '5':
			return true // JCB 35
		}
		return false
	case '5':
		if len(digits) >= 2 && digits[1] >= '1' && digits[1] <= '5' {
			return true // Mastercard 51-55
		}
		return false
	case '2':
		if len(digits) < 4 {
			return false
		}
		n := int(d(0))*1000 + int(d(1))*100 + int(d(2))*10 + int(d(3))
		return n >= 2221 && n <= 2720 // Mastercard 2-series
	case '6':
		if strings.HasPrefix(digits, "6011") {
			return true
		}
		if strings.HasPrefix(digits, "65") {
			return true
		}
		if len(digits) >= 3 {
			n := int(d(0))*100 + int(d(1))*10 + int(d(2))
			if n >= 644 && n <= 649 {
				return true // Discover
			}
		}
		return false
	}
	return false
}

// cardGroupingValid reports whether a candidate that contains separators
// (spaces or dashes) is grouped the way real card numbers are conventionally
// printed: 4-4-4-4[-3] (16 or 19 digits) or 4-6-5 (15-digit Amex). This is
// what keeps a dash-separated UUID tail — whose groups are 4-12 digits, not
// a real card grouping — from qualifying even when it happens to be
// Luhn-valid and (rarely) starts with a shared prefix digit.
func cardGroupingValid(m string) bool {
	groups := strings.FieldsFunc(m, func(r rune) bool { return r == ' ' || r == '-' })
	lens := make([]int, len(groups))
	for i, g := range groups {
		lens[i] = len(g)
	}
	equal := func(want []int) bool {
		if len(lens) != len(want) {
			return false
		}
		for i := range want {
			if lens[i] != want[i] {
				return false
			}
		}
		return true
	}
	return equal([]int{4, 4, 4, 4}) || equal([]int{4, 4, 4, 4, 3}) || equal([]int{4, 6, 5})
}

// luhnValid reports whether digits (an ASCII string of '0'-'9') passes the
// Luhn checksum used by every major card network.
func luhnValid(digits string) bool {
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		c := digits[i]
		if c < '0' || c > '9' {
			return false
		}
		d := int(c - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}
