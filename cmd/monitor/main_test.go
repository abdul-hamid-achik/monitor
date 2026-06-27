package main

import (
	"reflect"
	"testing"
)

func TestExtractGlobalFlags(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		want      []string
		wantPprof string
	}{
		{
			name:      "pprof with separate value",
			args:      []string{"--pprof", "localhost:6060", "studio"},
			want:      []string{"studio"},
			wantPprof: "localhost:6060",
		},
		{
			name:      "pprof with = is a single token",
			args:      []string{"--pprof=localhost:6060", "snapshot"},
			want:      []string{"snapshot"},
			wantPprof: "localhost:6060",
		},
		{
			// Regression: a global flag AFTER a subcommand is honored, not
			// silently dropped (flag.Parse used to stop at "watch").
			name:      "pprof after subcommand is honored",
			args:      []string{"watch", "--pprof", ":6060"},
			want:      []string{"watch"},
			wantPprof: ":6060",
		},
		{
			name: "-- passes through verbatim for cobra",
			args: []string{"vault", "--", "mycommand", "--pprof", "x"},
			want: []string{"vault", "--", "mycommand", "--pprof", "x"},
		},
		{
			// Non-global flags (now incl. studio's --reload-server) pass through
			// to cobra untouched.
			name: "unrelated flags pass through",
			args: []string{"studio", "--reload-server", "--json"},
			want: []string{"studio", "--reload-server", "--json"},
		},
	}
	for _, tc := range cases {
		pprofAddr = ""
		got := extractGlobalFlags(append([]string(nil), tc.args...))
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: remainder = %v, want %v", tc.name, got, tc.want)
		}
		if pprofAddr != tc.wantPprof {
			t.Errorf("%s: pprofAddr = %q, want %q", tc.name, pprofAddr, tc.wantPprof)
		}
	}
}

func TestIsLoopback(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"", false}, // ":6060" wildcard bind
		{"0.0.0.0", false},
		{"192.168.1.5", false},
	}
	for _, tc := range cases {
		if got := isLoopback(tc.host); got != tc.want {
			t.Errorf("isLoopback(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

func TestSplitFlag(t *testing.T) {
	cases := []struct {
		arg       string
		wantName  string
		wantValue string
		wantHas   bool
	}{
		{"--reload-addr=x", "reload-addr", "x", true},
		{"--reload-server", "reload-server", "", false},
		{"--", "", "", false},
		{"snapshot", "", "", false},
		{"-h", "", "", false},
	}
	for _, tc := range cases {
		name, value, has := splitFlag(tc.arg)
		if name != tc.wantName || value != tc.wantValue || has != tc.wantHas {
			t.Errorf("splitFlag(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.arg, name, value, has, tc.wantName, tc.wantValue, tc.wantHas)
		}
	}
}
