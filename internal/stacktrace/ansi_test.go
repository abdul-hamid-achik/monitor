package stacktrace

import "testing"

func TestStripANSI(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello world", "hello world"},
		{"empty", "", ""},
		{
			"sgr color",
			"\x1b[33mWarning\x1b[0m: something",
			"Warning: something",
		},
		{
			"deno error banner",
			"\x1b[1m\x1b[31merror\x1b[0m: Uncaught Error: boom",
			"error: Uncaught Error: boom",
		},
		{
			"nested colors mid-line",
			"    at \x1b[1m\x1b[3mdetonate\x1b[0m (\x1b[2m\x1b[38;5;245mfile.js\x1b[0m\x1b[0m:\x1b[33m49\x1b[0m:\x1b[33m9\x1b[0m)",
			"    at detonate (file.js:49:9)",
		},
		{"trailing lone esc", "abc\x1b", "abc"},
		{"osc terminated by bel", "\x1b]0;title\ax", "x"},
		{"osc terminated by st", "\x1b]0;title\x1b\\x", "x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StripANSI(tc.in)
			if got != tc.want {
				t.Errorf("StripANSI(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestStripANSIIdempotent(t *testing.T) {
	s := "clean line with no escapes at all"
	if got := StripANSI(s); got != s {
		t.Errorf("StripANSI on clean text changed it: %q", got)
	}
}
