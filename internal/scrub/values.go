package scrub

import (
	"regexp"
	"strings"
)

// secretNameRE matches environment variable NAMEs that conventionally carry
// a secret value: token, secret, password/passwd (and the *_PASS/*_PWD
// shorthand SEC-6 added: DB_PASS, REDIS_PASS, SMTP_PASS, MYSQL_PWD),
// api_key/apikey (and the generic *_KEY suffix: STRIPE_KEY, OPENAI_KEY,
// ENCRYPTION_KEY, SIGNING_KEY), private_key/privatekey, access_key/
// accesskey, dsn, credential, or a standalone "pat" segment (e.g.
// GITHUB_PAT, MY_PAT_TOKEN), all case-insensitive. The 8-rune minimum in
// WithValues, not this regex, is what keeps short false positives out.
var secretNameRE = regexp.MustCompile(`(?i)(token|secret|passw(or)?d|api_?key|private_?key|access_?key|dsn|credential|(^|_)(pass|pwd)(_|$)|_key$|(^|_)pat(_|$))`)

// alwaysExcludedNames are never treated as secrets even if their name would
// otherwise match secretNameRE (it never does, in practice) or is listed in
// extraNames — redacting them would corrupt ordinary output rather than
// protect anything.
var alwaysExcludedNames = map[string]struct{}{
	"PATH":  {},
	"HOME":  {},
	"PWD":   {},
	"SHELL": {},
	"TERM":  {},
}

// SecretEnvValues scans environ (entries shaped like os.Environ(), i.e.
// "NAME=value") and returns the values of every variable whose name matches
// secretNameRE, plus any variable named in extraNames (case-insensitive),
// regardless of whether monitor run's own process needs it. PATH, HOME,
// PWD, SHELL and TERM are always excluded. Empty values are skipped. The
// result is meant to be passed to WithValues, which additionally drops
// values shorter than 8 characters.
func SecretEnvValues(environ []string, extraNames []string) []string {
	extra := make(map[string]struct{}, len(extraNames))
	for _, n := range extraNames {
		extra[strings.ToUpper(n)] = struct{}{}
	}

	var out []string
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || value == "" {
			continue
		}
		upper := strings.ToUpper(name)
		if _, excluded := alwaysExcludedNames[upper]; excluded {
			continue
		}
		_, named := extra[upper]
		if !named && !secretNameRE.MatchString(name) {
			continue
		}
		out = append(out, value)
	}
	return out
}
