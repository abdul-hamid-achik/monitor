package stacktrace

import (
	"testing"
	"time"
)

func TestParseTimestamp(t *testing.T) {
	cases := []struct {
		name string
		line string
		want time.Time
		ok   bool
	}{
		{
			name: "zap rfc3339nano",
			line: `2026-09-22T10:04:37.123456789Z	error	request failed	{}`,
			want: time.Date(2026, 9, 22, 10, 4, 37, 123456789, time.UTC),
			ok:   true,
		},
		{
			name: "rfc3339 with offset",
			line: `2026-09-22T10:04:37-07:00 ERROR [worker] boom`,
			want: time.Date(2026, 9, 22, 17, 4, 37, 0, time.UTC),
			ok:   true,
		},
		{
			name: "python asctime",
			line: `2026-09-22 10:04:37,123 ERROR root: flaky_parse failed`,
			want: time.Date(2026, 9, 22, 10, 4, 37, 123000000, time.UTC),
			ok:   true,
		},
		{
			name: "ruby logger",
			line: `I, [2026-09-22T10:04:37.123456 #123]  INFO -- : starting`,
			want: time.Date(2026, 9, 22, 10, 4, 37, 123456000, time.UTC),
			ok:   true,
		},
		{
			name: "no timestamp",
			line: `Error: flakyParse: malformed payload near token "bad-payl"`,
			ok:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseTimestamp(tc.line)
			if ok != tc.ok {
				t.Fatalf("parseTimestamp(%q) ok = %v, want %v (got %v)", tc.line, ok, tc.ok, got)
			}
			if ok && !got.Equal(tc.want) {
				t.Errorf("parseTimestamp(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}
