package scrub

import (
	"reflect"
	"sort"
	"testing"
)

func TestSecretEnvValuesSelectsByName(t *testing.T) {
	environ := []string{
		"API_TOKEN=abc123",
		"DB_PASSWORD=hunter2",
		"DB_PASSWD=hunter3",
		"STRIPE_API_KEY=sk_" + "live_x",
		"PRIVATE_KEY=pemdata",
		"ACCESS_KEY_ID=akid",
		"SENTRY_DSN=https://x@y/1",
		"AWS_CREDENTIAL_PROFILE=default",
		"GITHUB_PAT=ghp_x",
		"MY_PAT_VALUE=v1",
		"PAT_ONLY=v2",
		"FEATURE_FLAG=plainvalue",
		"PATH=/usr/bin:/bin",
		"HOME=/home/dev",
		"PWD=/repo",
		"SHELL=/bin/zsh",
		"TERM=xterm-256color",
		"EMPTY_TOKEN=",
	}

	got := SecretEnvValues(environ, nil)
	want := []string{
		"abc123", "hunter2", "hunter3", "sk_" + "live_x", "pemdata", "akid",
		"https://x@y/1", "default", "ghp_x", "v1", "v2",
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SecretEnvValues() = %#v, want %#v", got, want)
	}
}

func TestSecretEnvValuesExtraNames(t *testing.T) {
	environ := []string{
		"FAKE_API_TOKEN=covered-by-default-regex",
		"CUSTOM_FLAG=extra-value-here",
		"OTHER=untouched",
	}

	got := SecretEnvValues(environ, []string{"custom_flag"})
	want := []string{"covered-by-default-regex", "extra-value-here"}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SecretEnvValues() = %#v, want %#v", got, want)
	}
}

func TestSecretEnvValuesAlwaysExcludesReservedNames(t *testing.T) {
	environ := []string{"PATH=/usr/bin"}
	// Even if the caller explicitly names PATH via --redact-env, it must
	// never be selected.
	got := SecretEnvValues(environ, []string{"PATH"})
	if len(got) != 0 {
		t.Fatalf("PATH must never be selected, got %#v", got)
	}
}

func TestSecretEnvValuesMalformedEntriesIgnored(t *testing.T) {
	got := SecretEnvValues([]string{"NOEQUALSIGN", "TOKEN="}, nil)
	if len(got) != 0 {
		t.Fatalf("malformed/empty entries should yield nothing, got %#v", got)
	}
}
