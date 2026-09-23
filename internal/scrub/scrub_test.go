package scrub

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
	"time"
)

// goldenPositive is the golden corpus of synthetic secrets: one entry per
// detector (plus the adversarial shapes from code review — ANSI-wrapped and
// JSON-escaped secrets, uppercase Bearer, empty/token-only URL userinfo, an
// '@' inside a password, non-ASCII emails, short and long card lengths, and
// a truncated/PGP private key), proving 100% redaction. Every "contains"
// fragment is a distinguishing substring of the secret (not the whole
// thing, since some secrets span multiple lines) that must be gone from the
// output. When wantOutput is set, the ENTIRE output is asserted verbatim
// instead of only checking for a token/fragment — this is what actually
// proves nothing besides the secret changed (surrounding text, a preserved
// scheme, an ANSI sequence or a JSON escape pair staying byte-for-byte
// intact).
var goldenPositive = []struct {
	name       string
	input      string
	contains   string // must NOT appear in output (ignored when wantOutput is set)
	wantToken  string // must appear in output (ignored when wantOutput is set)
	wantOutput string // when non-empty, the full output asserted verbatim
	wantCount  int
}{
	{
		name:      "url_userinfo",
		input:     "connecting to postgres://demo_user:s3cr3tPass@db.internal.example:5432/appdb",
		contains:  "demo_user:s3cr3tPass",
		wantToken: "postgres://" + tokenURLCreds + "@db.internal.example:5432/appdb",
		wantCount: 1,
	},
	{
		name:       "url_userinfo_empty_user",
		input:      "REDIS_URL=redis://:s3cretRedisPassw0rd@localhost:6379/0",
		wantOutput: "REDIS_URL=redis://" + tokenURLCreds + "@localhost:6379/0",
		wantCount:  1,
	},
	{
		name:       "url_userinfo_token_only_user",
		input:      "webhook http://opaqueTokenValue123456@127.0.0.1:8080/hook",
		wantOutput: "webhook http://" + tokenURLCreds + "@127.0.0.1:8080/hook",
		wantCount:  1,
	},
	{
		name:       "url_userinfo_at_in_password",
		input:      "postgres://user:p@ssw0rd@localhost:5432/db",
		wantOutput: "postgres://" + tokenURLCreds + "@localhost:5432/db",
		wantCount:  1,
	},
	{
		name:       "url_userinfo_rediss_empty_user",
		input:      "rediss://:pw@cache.example.com:6380",
		wantOutput: "rediss://" + tokenURLCreds + "@cache.example.com:6380",
		wantCount:  1,
	},
	{
		name:      "bearer_token",
		input:     `Authorization: Bearer abc123.def456-ghi789_JKL`,
		contains:  "abc123.def456-ghi789_JKL",
		wantToken: "Bearer " + tokenToken,
		wantCount: 1,
	},
	{
		name:      "bearer_token_lowercase_scheme",
		input:     `authorization: bearer sometoken1234567890`,
		contains:  "sometoken1234567890",
		wantToken: "bearer " + tokenToken,
		wantCount: 1,
	},
	{
		name:       "bearer_token_uppercase_scheme",
		input:      "Authorization: BEARER abcdefghijklmnop1234",
		wantOutput: "Authorization: BEARER " + tokenToken,
		wantCount:  1,
	},
	{
		// Arbitrary mixed casing, not just the all-lower/all-upper extremes:
		// RFC 6750 doesn't require a particular casing, and a case-sensitive
		// guard (even one covering both extremes) misses this.
		name:       "bearer_token_mixedcase_scheme",
		input:      "AUTHORIZATION: BeArEr abcdefghijklmnopqrstuvwxyz0123",
		wantOutput: "AUTHORIZATION: BeArEr " + tokenToken,
		wantCount:  1,
	},
	{
		name: "jwt",
		// jwt.io's published example token (synthetic sample data).
		input:     "session=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c; path=/",
		contains:  "SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
		wantToken: tokenJWT,
		wantCount: 1,
	},
	{
		name:      "aws_akia_key",
		input:     "AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE",
		contains:  "AKIAIOSFODNN7EXAMPLE",
		wantToken: tokenAWSKey,
		wantCount: 1,
	},
	{
		name:      "aws_asia_key",
		input:     "temp creds ASIAIOSFODNN7EXAMPLE expired",
		contains:  "ASIAIOSFODNN7EXAMPLE",
		wantToken: tokenAWSKey,
		wantCount: 1,
	},
	{
		name:       "aws_key_glued_to_preceding_word",
		input:      "KEY_AKIAIOSFODNN7EXAMPLE",
		wantOutput: "KEY_" + tokenAWSKey,
		wantCount:  1,
	},
	{
		name:      "github_classic_token",
		input:     "remote: gh" + "p_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 rejected",
		contains:  "gh" + "p_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789",
		wantToken: tokenToken,
		wantCount: 1,
	},
	{
		name:      "github_" + "pat_token",
		input:     "using github_" + "pat_11ABCDEFG0abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOP for clone",
		contains:  "github_" + "pat_11ABCDEFG0abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOP",
		wantToken: tokenToken,
		wantCount: 1,
	},
	{
		name:      "slack_token",
		input:     "SLACK_BOT_TOKEN=xox" + "b-1234567890-1234567890123-abcdefghijklmnopqrstuvwx",
		contains:  "xox" + "b-1234567890-1234567890123-abcdefghijklmnopqrstuvwx",
		wantToken: tokenToken,
		wantCount: 1,
	},
	{
		name:      "stripe_live_key",
		input:     "STRIPE_SECRET_KEY=sk_" + "live_FAKEabcdefghijklmnopqrstuvwxyz0123456789",
		contains:  "sk_" + "live_FAKEabcdefghijklmnopqrstuvwxyz0123456789",
		wantToken: tokenToken,
		wantCount: 1,
	},
	{
		name:      "stripe_restricted_key",
		input:     "STRIPE_RESTRICTED_KEY=rk_" + "test_FAKEabcdefghijklmnopqrstuvwxyz0123456789",
		contains:  "rk_" + "test_FAKEabcdefghijklmnopqrstuvwxyz0123456789",
		wantToken: tokenToken,
		wantCount: 1,
	},
	{
		name:      "pem_private_key",
		input:     "dumping key:\n" + fakePEMBlock + "\ndone",
		contains:  "MIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEr8SU3JEhSMTa8I3XVIe4L",
		wantToken: tokenPrivateKey,
		wantCount: 1,
	},
	{
		name:       "pem_truncated_no_matching_end",
		input:      "dumping key:\n-----BEGIN RSA " + "PRIVATE KEY-----\nMIIBOgIBAAJBAKj34Gk\n(truncated by log line limit)",
		wantOutput: "dumping key:\n" + tokenPrivateKey,
		wantCount:  1,
	},
	{
		name:       "pgp_private_key_block",
		input:      fakePGPBlock,
		wantOutput: tokenPrivateKey,
		wantCount:  1,
	},
	{
		name:      "card_plain",
		input:     "charged card 4111111111111111 for $10",
		contains:  "4111111111111111",
		wantToken: tokenCard,
		wantCount: 1,
	},
	{
		name:      "card_spaced",
		input:     "charged card 4111 1111 1111 1111 for $10",
		contains:  "4111 1111 1111 1111",
		wantToken: tokenCard,
		wantCount: 1,
	},
	{
		name:       "card_13_digit_visa",
		input:      "test card 4222222222222 on file",
		wantOutput: "test card " + tokenCard + " on file",
		wantCount:  1,
	},
	{
		name:       "card_dashed_amex",
		input:      "amex on file: 3782-822463-10005",
		wantOutput: "amex on file: " + tokenCard,
		wantCount:  1,
	},
	{
		name:       "card_19_digit_dashed",
		input:      "card 4111-1111-1111-1111-110 charged",
		wantOutput: "card " + tokenCard + " charged",
		wantCount:  1,
	},
	{
		name:      "email",
		input:     "notify alice@example.com on failure",
		contains:  "alice@example.com",
		wantToken: tokenEmail,
		wantCount: 1,
	},
	{
		name:       "email_unicode_local_part",
		input:      "contact josé@example.com please",
		wantOutput: "contact " + tokenEmail + " please",
		wantCount:  1,
	},
	{
		name:       "email_unicode_domain",
		input:      "müller@example.de",
		wantOutput: tokenEmail,
		wantCount:  1,
	},
	{
		name:      "bearer_and_freestanding_jwt_together",
		input:     "Authorization: Bearer abcdefghij1234567890 and cached=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.4pcPyMD09olPSyXnrXCjTwXyr4BsezdI1AVTmud2fu4",
		contains:  "abcdefghij1234567890",
		wantToken: tokenJWT,
		wantCount: 2,
	},
	{
		name:       "ansi_wrapped_aws_key",
		input:      "\x1b[31mAKIAIOSFODNN7EXAMPLE\x1b[0m",
		wantOutput: "\x1b[31m" + tokenAWSKey + "\x1b[0m",
		wantCount:  1,
	},
	{
		name:       "ansi_wrapped_card",
		input:      "\x1b[32m4111111111111111\x1b[0m",
		wantOutput: "\x1b[32m" + tokenCard + "\x1b[0m",
		wantCount:  1,
	},
	{
		name:       "ansi_wrapped_github_token",
		input:      "\x1b[33mgh" + "p_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789\x1b[0m",
		wantOutput: "\x1b[33m" + tokenToken + "\x1b[0m",
		wantCount:  1,
	},
	{
		name:       "ansi_wrapped_bearer",
		input:      "\x1b[1mBearer abcdefghijklmnop1234\x1b[0m",
		wantOutput: "\x1b[1mBearer " + tokenToken + "\x1b[0m",
		wantCount:  1,
	},
	{
		name:       "ansi_wrapped_stripe_key",
		input:      "\x1b[36msk_" + "live_FAKEabcdefghijklmnopqrstuvwxyz0123456789\x1b[0m",
		wantOutput: "\x1b[36m" + tokenToken + "\x1b[0m",
		wantCount:  1,
	},
	{
		name:       "ansi_wrapped_email",
		input:      "\x1b[1malice@example.com",
		wantOutput: "\x1b[1m" + tokenEmail,
		wantCount:  1,
	},
	{
		name:       "json_escaped_newline_before_aws_key",
		input:      `{"msg":"failed\nAKIAIOSFODNN7EXAMPLE"}`,
		wantOutput: `{"msg":"failed\n` + tokenAWSKey + `"}`,
		wantCount:  1,
	},
	{
		name:       "json_escaped_newline_before_card",
		input:      `{"msg":"failed\n4111111111111111"}`,
		wantOutput: `{"msg":"failed\n` + tokenCard + `"}`,
		wantCount:  1,
	},
	{
		name:       "json_escaped_newline_before_bearer",
		input:      `{"msg":"failed\nBearer abcdefghijklmnop1234"}`,
		wantOutput: `{"msg":"failed\nBearer ` + tokenToken + `"}`,
		wantCount:  1,
	},
	{
		name: "json_escaped_newline_before_email_stays_valid_json",
		// The bug this guards: naively matching from the escaped 'n' onward
		// used to swallow it, leaving a lone backslash and breaking the
		// JSON escape pair.
		input:      `{"msg":"user\nalice@example.com not found"}`,
		wantOutput: `{"msg":"user\n` + tokenEmail + ` not found"}`,
		wantCount:  1,
	},
}

const fakePEMBlock = `-----BEGIN RSA ` + "PRIVATE" + ` KEY-----
MIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEr8SU3JEhSMTa8I3XVIe4L
9d9OqrCTn9m2gz5X0R6Ny5xxJp4dSAcYqoNwSg8fnnUCAwEAAQJAKf3lG
FAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKE
-----END RSA PRIVATE KEY-----`

const fakePGPBlock = `-----BEGIN PGP ` + "PRIVATE" + ` KEY BLOCK-----
Version: FAKE 1.0

lQOYBFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFA
KEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEF
-----END PGP PRIVATE KEY BLOCK-----`

func TestGoldenCorpusPositiveRedaction(t *testing.T) {
	for _, tc := range goldenPositive {
		t.Run(tc.name, func(t *testing.T) {
			s := New()
			out := s.String(tc.input)
			if tc.wantOutput != "" {
				if out != tc.wantOutput {
					t.Fatalf("output = %q, want %q", out, tc.wantOutput)
				}
			} else {
				if strings.Contains(out, tc.contains) {
					t.Fatalf("secret fragment survived scrubbing: output=%q", out)
				}
				if !strings.Contains(out, tc.wantToken) {
					t.Fatalf("expected token %q in output, got %q", tc.wantToken, out)
				}
			}
			if got := s.Count(); got != tc.wantCount {
				t.Fatalf("Count() = %d, want %d (output=%q)", got, tc.wantCount, out)
			}
		})
	}
}

// goldenNegative is the negative corpus: text that must survive scrubbing
// byte-for-byte, proving the detectors above have no false positives on
// commonly-confused shapes — including the adversarial ones from code
// review: fully-qualified names in four languages that are exactly as long
// and dot-separated as a JWT, an epoch-ms timestamp and a zero id that are
// Luhn-valid by coincidence, a UUID whose dash-separated tail is also
// Luhn-valid, and ordinary English/HTTP text containing "bearer" as a word
// or challenge-parameter name rather than a token.
var goldenNegative = []string{
	// git SHA-1 (40 hex) and SHA-256 (64 hex) — mixed digits/letters, no
	// run of pure digits anywhere near the 13-19 card range.
	"commit c25fb93a1e4d8b6f0c9a2d7e5f3b1a8c4d6e9f01 fixed the regression",
	"blob sha256:9f8a6c3e1b4d7f0a2c5e8b1d4f7a0c3e6b9d2f5a8c1e4b7d0a3f6c9e2b5d8f1a",
	// UUID v4 (OpenAPI/Swagger's well-known example id).
	"request_id=3fa85f64-5717-4562-b3fc-2c963f66afa6 accepted",
	// A UUID whose dash-separated tail ("8123-456789012340" -> 16 digits)
	// happens to pass Luhn — it must still be rejected because '8' is not a
	// card-network prefix.
	"request_id=3fa85f64-5717-4562-8123-456789012340 accepted",
	// ISO 8601 timestamp.
	"observed_at=2026-09-22T10:15:30Z level=info",
	// semver.
	"module github.com/abdul-hamid-achik/monitor v1.25.5",
	// npm-scoped package path under node_modules.
	"resolved from node_modules/@myorg/widgets/dist/index.js",
	// a digit run in the card size range that fails Luhn.
	"trace_id=1234567890123456",
	// a pino-style epoch-ms timestamp: 13 digits, Luhn-valid by
	// coincidence, but "1" is not a prefix any card network issues.
	`{"ts":1758535530002}`,
	// an all-zero id — never a real card regardless of Luhn or prefix.
	"span_id=0000000000000000",
	"key=[0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0]",
	// single-label host, no TLD — must not look like an email.
	"connecting to user@localhost for smoke test",
	// ordinary sentence.
	"The build finished in 12.4s with 3 warnings.",
	// contains "earer" (the Bearer guard's substring) without an actual
	// Bearer token following it.
	"the wearer of the badge signed off on the shearer's report",
	// "bearer" the ordinary English word, not a scheme: the token-length
	// floor (16 chars) rejects "of" as a captured token.
	"the bearer of bad news arrived",
	// a WWW-Authenticate challenge parameter, not a token.
	`WWW-Authenticate: Bearer realm="example"`,
	// multiple dots but no JWT-shaped run (exercises the JWT guard, which
	// checks for the literal "eyJ" header prefix, not just "two dots").
	"checking v1.25.5 against v2.0.1 for compatibility",
	// fully-qualified names from four ecosystems: as long and
	// dot-separated as a JWT, but none of their segments starts with the
	// base64url-encoded JSON header prefix "eyJ".
	"at org.springframework.transaction.interceptor.TransactionInterceptor.invoke(Unknown Source)",
	"kotlinx.coroutines.scheduling.CoroutineScheduler$Worker.run",
	"System.Runtime.CompilerServices.TaskAwaiter.ThrowForNonSuccess(Task task)",
	"Microsoft.AspNetCore.Diagnostics.DeveloperExceptionPageMiddleware.Invoke",
	"logger=sentry_sdk.integrations.sqlalchemy",
}

func TestGoldenCorpusNoFalsePositives(t *testing.T) {
	for i, text := range goldenNegative {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			s := New()
			out := s.String(text)
			if out != text {
				t.Fatalf("false positive: input=%q output=%q", text, out)
			}
			if got := s.Count(); got != 0 {
				t.Fatalf("Count() = %d, want 0 for %q", got, text)
			}
		})
	}
}

// TestRandomUUIDsNeverFlaggedAsCards is a property test standing in for the
// review's fuzz run: a UUID's hex digits sit in a single \b-delimited run
// with the surrounding dashes, so a handful of random v4 UUIDs happen to
// have a 13-19 decimal-digit dash-separated tail that passes Luhn by
// coincidence (about 1 in 8,000 in the review's 200k-UUID run). The card
// detector must reject every one of these regardless, via the IIN-prefix
// and grouping checks, not just the two fixed UUIDs in the golden corpus.
func TestRandomUUIDsNeverFlaggedAsCards(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 20000; i++ {
		var b [16]byte
		for j := range b {
			b[j] = byte(rng.IntN(256))
		}
		b[6] = (b[6] & 0x0f) | 0x40 // version 4
		b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
		uuid := fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])

		s := New()
		out := s.String(uuid)
		if out != uuid {
			t.Fatalf("random v4 UUID altered: %q -> %q (seed index %d)", uuid, out, i)
		}
	}
}

// TestRandomSHAsNeverFlaggedAsSecrets mirrors TestRandomUUIDsNeverFlaggedAsCards
// for SHA-1/SHA-256 hex digests: no detector should ever fire on a run of
// hex digits regardless of its random content.
func TestRandomSHAsNeverFlaggedAsSecrets(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	const hexDigits = "0123456789abcdef"
	for i := 0; i < 5000; i++ {
		for _, n := range []int{40, 64} { // SHA-1, SHA-256
			b := make([]byte, n)
			for j := range b {
				b[j] = hexDigits[rng.IntN(len(hexDigits))]
			}
			sha := string(b)

			s := New()
			out := s.String(sha)
			if out != sha {
				t.Fatalf("random %d-char hex SHA altered: %q -> %q (seed index %d)", n, sha, out, i)
			}
		}
	}
}

// TestEmailKnownLimitations documents two shapes that emailRE, being a
// shape-only detector rather than a full validator, redacts even though
// they are not email addresses: a git SSH remote (whose domain+TLD shape is
// indistinguishable from a real one) and a dotted attribute chain that ends
// in a too-short final segment. These are accepted trade-offs, not
// something callers should rely on being excluded — see emailRE's doc
// comment.
func TestEmailKnownLimitations(t *testing.T) {
	cases := []struct {
		name       string
		input      string
		wantOutput string
	}{
		{
			name:       "ssh_remote",
			input:      "git@github.com:org/repo.git",
			wantOutput: tokenEmail + ":org/repo.git",
		},
		{
			name:       "matmul_attribute_chain",
			input:      "return x@self.weight.T",
			wantOutput: "return " + tokenEmail + ".T",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New()
			out := s.String(tc.input)
			if out != tc.wantOutput {
				t.Fatalf("output = %q, want %q", out, tc.wantOutput)
			}
		})
	}
}

func TestWithValuesExactRedaction(t *testing.T) {
	const long = "myVeryLongInjectedSecretValue987"
	const short = "sh0rt12" // 7 chars, under the 8-char floor

	s := New(WithValues([]string{long, short}))
	out := s.String("token=" + long + " other=" + short)

	if strings.Contains(out, long) {
		t.Fatalf("long value was not redacted: %q", out)
	}
	if !strings.Contains(out, short) {
		t.Fatalf("short (<8 char) value should never be redacted: %q", out)
	}
	if !strings.Contains(out, tokenValue) {
		t.Fatalf("expected %q token in output: %q", tokenValue, out)
	}
	if got := s.Count(); got != 1 {
		t.Fatalf("Count() = %d, want 1", got)
	}
}

func TestWithValuesLongerValueWinsOverShadowing(t *testing.T) {
	// "secretAB" is a substring of "secretABCDEFGH". If the shorter value
	// were redacted first, the longer one could never match afterward.
	short := "secretAB"
	long := "secretABCDEFGH"

	s := New(WithValues([]string{short, long}))
	out := s.String("value=" + long)

	if strings.Contains(out, long) || strings.Contains(out, "CDEFGH") {
		t.Fatalf("longer overlapping value survived: %q", out)
	}
	if strings.Count(out, tokenValue) != 1 {
		t.Fatalf("expected exactly one redaction token, got %q", out)
	}
}

func TestWithValuesNeverAppearsInOutput(t *testing.T) {
	secret := "superDuperSecretValue42"
	s := New(WithValues([]string{secret}))
	out := s.String("Authorization: Bearer " + secret)

	if strings.Contains(out, secret) {
		t.Fatalf("secret leaked into scrubbed output: %q", out)
	}
}

// TestWithValuesLengthIsRuneCountNotByteCount proves the 8-character floor
// is measured in runes: a 4-character multi-byte value ("密码密码", 4 runes /
// 12 bytes) must be ignored exactly like a 4-byte ASCII value would be, not
// redacted because it happens to be >= 8 bytes.
func TestWithValuesLengthIsRuneCountNotByteCount(t *testing.T) {
	short := "密码密码" // 4 runes, 12 bytes
	s := New(WithValues([]string{short}))
	out := s.String("x " + short + " y")

	if s.Count() != 0 {
		t.Fatalf("Count() = %d, want 0: a 4-rune value should be ignored regardless of its byte length", s.Count())
	}
	want := "x " + short + " y"
	if out != want {
		t.Fatalf("output = %q, want %q (unredacted)", out, want)
	}
}

// TestWithValuesSkipsValueThatCollidesWithItsOwnToken proves a WithValues
// value that happens to be a substring of a stable redaction token (e.g.
// the literal word "redacted", which is a substring of "[redacted]") is
// skipped entirely, rather than being redacted once and then matched again
// inside its own replacement token on a second pass.
func TestWithValuesSkipsValueThatCollidesWithItsOwnToken(t *testing.T) {
	s := New(WithValues([]string{"redacted"})) // substring of tokenValue "[redacted]"
	first := s.String("x redacted y")
	if s.Count() != 0 {
		t.Fatalf("Count() = %d, want 0: value colliding with its own token should be skipped", s.Count())
	}

	second := s.String(first)
	if second != first {
		t.Fatalf("re-scrubbing changed output: %q -> %q", first, second)
	}
}

func TestCountAccumulatesAcrossCalls(t *testing.T) {
	s := New()
	s.String("contact bob@example.com")
	s.String("contact carol@example.com")

	if got := s.Count(); got != 2 {
		t.Fatalf("Count() = %d, want 2 after two String calls", got)
	}
}

func TestCountZeroForFreshScrubber(t *testing.T) {
	s := New()
	if got := s.Count(); got != 0 {
		t.Fatalf("Count() = %d, want 0 before any String call", got)
	}
}

func TestStringIdempotentOnAlreadyRedactedOutput(t *testing.T) {
	s := New()
	first := s.String("email me at dana@example.com")
	countAfterFirst := s.Count()

	second := s.String(first)
	if second != first {
		t.Fatalf("re-scrubbing redacted text changed it: %q -> %q", first, second)
	}
	if s.Count() != countAfterFirst {
		t.Fatalf("re-scrubbing already-redacted text should add no further redactions: %d -> %d", countAfterFirst, s.Count())
	}
}

// benchLine approximates the overwhelmingly common case on monitor run's
// per-line hot path: an ordinary ~1KB structured log line carrying no
// secret at all. This is what BenchmarkScrubberString1KB is measuring
// against the <50µs/op budget — every detector's cheap literal-substring
// guard (see detectors.go) should reject the line without ever running its
// regexp.
var benchLine = func() string {
	var b strings.Builder
	b.WriteString(`{"level":"info","time":"2026-09-22T10:15:30Z","service":"web-api","msg":"handled request",`)
	b.WriteString(`"trace_id":"c25fb93a1e4d8b6f0c9a2d7e5f3b1a8c4d6e9f01","request_id":"3fa85f64-5717-4562-b3fc-2c963f66afa6",`)
	b.WriteString(`"method":"GET","path":"/v2/accounts","status":200,"duration_ms":42,`)
	for b.Len() < 1024 {
		b.WriteString(" upstream responded normally, nothing further to report in this padding segment;")
	}
	return b.String()
}()

// benchLineWithSecret is the same shape of line but with one real secret
// embedded (a Bearer token), so BenchmarkScrubberString1KBWithSecret
// measures the cost of an actual redaction rather than only the guards'
// reject path.
var benchLineWithSecret = func() string {
	var b strings.Builder
	b.WriteString(`{"level":"error","time":"2026-09-22T10:15:30Z","service":"web-api","msg":"upstream request failed",`)
	b.WriteString(`"trace_id":"c25fb93a1e4d8b6f0c9a2d7e5f3b1a8c4d6e9f01","request_id":"3fa85f64-5717-4562-b3fc-2c963f66afa6",`)
	b.WriteString(`"auth":"Bearer abcdefghijklmnopqrstuvwxyz0123456789",`)
	for b.Len() < 1024 {
		b.WriteString(" retrying after backoff, no further detail available in this padding segment;")
	}
	return b.String()
}()

// benchAccessLog is a realistic ~1KB access-log-shaped line: IPs, a
// request line, a URL, a UUID and no secrets — the shape code review found
// costing 69-108µs/op against the pre-fix detectors, well past the budget,
// because it has both dots (JWT guard) and "://" (URL guard) and 13+ digits
// scattered across its two UUIDs (card guard).
var benchAccessLog = func() string {
	var b strings.Builder
	b.WriteString(`127.0.0.1 - - [22/Sep/2026:10:15:30 +0000] "GET /v2/accounts?request_id=3fa85f64-5717-4562-b3fc-2c963f66afa6 HTTP/1.1" 200 512 `)
	b.WriteString(`"https://example.com/dashboard/accounts?ref=3fa85f64-5717-4562-b3fc-2c963f66afa6" "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36"`)
	for b.Len() < 1024 {
		b.WriteString(" extra=1234567890123456789")
	}
	return b.String()
}()

// benchJSONWithEmail is a realistic ~1KB JSON log line carrying one real
// email address plus a URL and a UUID — the single most expensive shape
// code review measured (108µs/op), since it exercises the email, URL, JWT
// and card guards simultaneously.
var benchJSONWithEmail = func() string {
	var b strings.Builder
	b.WriteString(`{"level":"error","time":"2026-09-22T10:15:30Z","service":"web-api","msg":"upstream request failed for alice@example.com",`)
	b.WriteString(`"url":"https://example.com/v2/accounts","request_id":"3fa85f64-5717-4562-b3fc-2c963f66afa6"}`)
	for b.Len() < 1024 {
		b.WriteString(` "extra":"padding text with dots. and more. text."`)
	}
	return b.String()
}()

// benchStackFrame is a realistic Python traceback fragment: file paths,
// line numbers and qualified names, no secrets.
var benchStackFrame = func() string {
	var b strings.Builder
	b.WriteString("  File \"/repo/app/services/payment.py\", line 142, in charge_customer\n")
	b.WriteString("    response = stripe.Charge.create(amount=amount, currency=\"usd\", customer=customer.id)\n")
	b.WriteString("  File \"/repo/venv/lib/python3.14/site-packages/stripe/api_resources/charge.py\", line 24, in create\n")
	for b.Len() < 1024 {
		b.WriteString("    extra padding stack content, no secrets here at all.\n")
	}
	return b.String()
}()

// benchMetrics is a realistic metrics-line shape: many dotted names and
// digit runs, no secrets.
var benchMetrics = func() string {
	var b strings.Builder
	b.WriteString("cpu.load.1m 0.42 1758535530\nmem.rss.bytes 104857600 1758535530\ndisk.io.read.bytes 5242880 1758535530")
	for b.Len() < 1024 {
		b.WriteString("\nmetric.value 1234.5678 1758535530")
	}
	return b.String()
}()

func BenchmarkScrubberString1KB(b *testing.B) {
	s := New(WithValues([]string{"myVeryLongInjectedSecretValue987"}))
	b.SetBytes(int64(len(benchLine)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.String(benchLine)
	}
}

func BenchmarkScrubberString1KBWithSecret(b *testing.B) {
	s := New(WithValues([]string{"myVeryLongInjectedSecretValue987"}))
	b.SetBytes(int64(len(benchLineWithSecret)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.String(benchLineWithSecret)
	}
}

func BenchmarkScrubberString1KBAccessLog(b *testing.B) {
	s := New()
	b.SetBytes(int64(len(benchAccessLog)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.String(benchAccessLog)
	}
}

func BenchmarkScrubberString1KBJSONWithEmail(b *testing.B) {
	s := New()
	b.SetBytes(int64(len(benchJSONWithEmail)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.String(benchJSONWithEmail)
	}
}

func BenchmarkScrubberString1KBStackFrame(b *testing.B) {
	s := New()
	b.SetBytes(int64(len(benchStackFrame)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.String(benchStackFrame)
	}
}

func BenchmarkScrubberString1KBMetrics(b *testing.B) {
	s := New()
	b.SetBytes(int64(len(benchMetrics)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.String(benchMetrics)
	}
}

// TestScrubberStringStaysWellUnderBudget is a canary, not a strict SLA: it
// fails only if Scrubber.String regresses by an order of magnitude on any
// of the representative ~1KB lines above (the slowest of which measured
// ~20µs/op on the reference machine after the fixes in this package,
// against a pre-fix range of 69-108µs/op). The bound is deliberately
// generous — 500µs, 10-25x the measured cost — so it stays green on a
// loaded or slower CI runner while still catching a real regression, such
// as a detector's cheap substring guard being removed or a regexp losing
// its literal anchor.
func TestScrubberStringStaysWellUnderBudget(t *testing.T) {
	const budget = 500 * time.Microsecond
	const iterations = 200

	lines := map[string]string{
		"access_log":      benchAccessLog,
		"json_with_email": benchJSONWithEmail,
		"stack_frame":     benchStackFrame,
		"metrics":         benchMetrics,
	}
	for name, line := range lines {
		t.Run(name, func(t *testing.T) {
			s := New()
			start := time.Now()
			for i := 0; i < iterations; i++ {
				_ = s.String(line)
			}
			avg := time.Since(start) / iterations
			if avg > budget {
				t.Fatalf("Scrubber.String averaged %s/op over %d iterations on %q, want <= %s", avg, iterations, name, budget)
			}
		})
	}
}
