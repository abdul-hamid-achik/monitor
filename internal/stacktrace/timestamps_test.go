package stacktrace

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseTimestamp(t *testing.T) {
	mx := time.FixedZone("UTC-6", -6*3600)
	cases := []struct {
		name string
		line string
		want time.Time
		ok   bool
	}{
		{"zap rfc3339nano utc", "2026-09-22T10:04:37.123456789Z\terror\trequest failed\t{}",
			time.Date(2026, 9, 22, 10, 4, 37, 123456789, time.UTC), true},
		{"rfc3339 colon offset", "2026-09-22T10:04:37-07:00 ERROR [worker] boom",
			time.Date(2026, 9, 22, 17, 4, 37, 0, time.UTC), true},
		{"zap iso8601 offset without colon", "2026-09-22T20:56:06.254-0600\tERROR\tapp/main.go:19\tboom",
			time.Date(2026, 9, 23, 2, 56, 6, 254000000, time.UTC), true},
		{"python asctime is local time", "2026-09-22 10:04:37,123 ERROR root: flaky_parse failed",
			time.Date(2026, 9, 22, 16, 4, 37, 123000000, time.UTC), true},
		{"ruby logger is local time", "E, [2026-09-22T10:04:37.123456 #123] ERROR -- : boom",
			time.Date(2026, 9, 22, 16, 4, 37, 123456000, time.UTC), true},
		{"go log package is local time", "2026/09/22 10:04:37 server: request failed",
			time.Date(2026, 9, 22, 16, 4, 37, 0, time.UTC), true},
		{"bracketed stamp", "[2026-09-22T10:04:37Z] worker crashed",
			time.Date(2026, 9, 22, 10, 4, 37, 0, time.UTC), true},
		{"stamp inside the message is not the line's", `Error: job failed at 2026-09-22T10:04:37Z`, time.Time{}, false},
		{"no timestamp", `Error: flakyParse: malformed payload`, time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseTimestamp(tc.line, mx)
			if ok != tc.ok {
				t.Fatalf("parseTimestamp(%q) ok = %v, want %v (got %v)", tc.line, ok, tc.ok, got)
			}
			if ok && !got.Equal(tc.want) {
				t.Errorf("parseTimestamp(%q) = %v, want %v", tc.line, got.UTC(), tc.want)
			}
		})
	}
}

// Zone-less stamps follow the injected zone, not UTC (R0-9/R1-36).
func TestParseTimestampZoneLessUsesLocation(t *testing.T) {
	line := "2026-09-22 10:04:37,123 ERROR root: x"
	utc, _ := parseTimestamp(line, time.UTC)
	mx, _ := parseTimestamp(line, time.FixedZone("UTC-6", -6*3600))
	if mx.Sub(utc) != 6*time.Hour {
		t.Errorf("UTC-6 reading %v minus UTC reading %v = %v, want 6h", mx, utc, mx.Sub(utc))
	}
	local, _ := parseTimestamp(line, nil)
	if local.Location() != time.Local {
		t.Errorf("nil location parsed in %v, want time.Local", local.Location())
	}
}

func TestParseEpoch(t *testing.T) {
	want := time.Date(2026, 9, 23, 4, 30, 26, 333802000, time.UTC)
	for _, s := range []string{"1790137826.333802", "1.790137826333802e+09"} {
		got, ok := parseEpoch(s)
		if !ok || got.Sub(want).Abs() > time.Microsecond {
			t.Errorf("parseEpoch(%q) = %v, %v; want %v", s, got.UTC(), ok, want)
		}
	}
	ms, _ := parseEpoch("1790137826333")
	us, _ := parseEpoch("1790137826333802")
	ns, _ := parseEpoch("1790137826333802000")
	for name, got := range map[string]time.Time{"millis": ms, "micros": us, "nanos": ns} {
		if got.Sub(want).Abs() > time.Millisecond {
			t.Errorf("%s epoch = %v, want %v", name, got.UTC(), want)
		}
	}
	for _, bad := range []string{"", "abc", "-5", "2026-09-22"} {
		if _, ok := parseEpoch(bad); ok {
			t.Errorf("parseEpoch(%q) ok, want failure", bad)
		}
	}
}

// ObservedAt end to end, per format, including the logger line just before
// a trace (R0-9/R1-36).
func TestObservedAtEndToEnd(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string // UTC, "" for zero
	}{
		{"python asctime line before the traceback",
			"2026-09-22 10:04:37,123 ERROR app: request failed\n" + pyTraceText, "2026-09-22T16:04:37.123Z"},
		{"python basicConfig prefix has no stamp", "ERROR:root:request failed\n" + pyTraceText, ""},
		{"ruby logger with a backtrace",
			"E, [2026-09-22T10:04:37.123456 #9] ERROR -- : boom (RuntimeError)\napp.rb:3:in 'Object#run'\n", "2026-09-22T16:04:37.123Z"},
		{"zap dev console", "2026-09-22T20:56:06.254-0600\tERROR\tapp/main.go:19\tboom", "2026-09-23T02:56:06.254Z"},
		{"zap prod console epoch", "1.790137826326494e+09\terror\tapp/main.go:91\tboom\t{}", "2026-09-23T04:30:26.326Z"},
		{"zap json float ts", `{"level":"error","ts":1790137826.3338308,"msg":"boom"}`, "2026-09-23T04:30:26.333Z"},
		{"zap json iso ts", `{"level":"error","ts":"2026-09-22T22:30:26.339-0600","msg":"boom"}`, "2026-09-23T04:30:26.339Z"},
		{"tslog", "2026-09-22T10:05:00.000Z ERROR [wf] ApplicationFailure: x\n    at f() @ /repo/a.js:1:1", "2026-09-22T10:05:00.000Z"},
		{"go log line before a panic", "2026/09/22 10:04:37 fatal: shutting down\n" + goCrashText, "2026-09-22T16:04:37.000Z"},
		{"js error without any stamp", jsTypeErrorText, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exs := detectIn(tc.text, testZone)
			if len(exs) != 1 {
				t.Fatalf("got %d events, want 1: %s", len(exs), strings.Join(summarizeAll(exs), " | "))
			}
			got := ""
			if !exs[0].ObservedAt.IsZero() {
				got = exs[0].ObservedAt.UTC().Format("2006-01-02T15:04:05.000Z")
			}
			if got != tc.want {
				t.Errorf("ObservedAt = %q, want %q", got, tc.want)
			}
		})
	}
}

// R0-17/R1-42: a zero ObservedAt is omitted from JSON instead of
// serializing as 0001-01-01T00:00:00Z.
func TestObservedAtOmittedWhenZero(t *testing.T) {
	b, err := json.Marshal(Exception{Parser: "js", Level: LevelError})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "observed_at") {
		t.Errorf("zero ObservedAt serialized: %s", b)
	}
	b, _ = json.Marshal(Exception{Parser: "js", ObservedAt: time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)})
	if !strings.Contains(string(b), `"observed_at":"2026-09-22T10:00:00Z"`) {
		t.Errorf("non-zero ObservedAt missing: %s", b)
	}
}
