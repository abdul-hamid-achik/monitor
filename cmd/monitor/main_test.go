package main

import (
	"reflect"
	"testing"
)

func TestShouldLaunchTUI(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"no args launches TUI", nil, true},
		{"empty slice launches TUI", []string{}, true},
		{"--help is CLI", []string{"--help"}, false},
		{"-h is CLI", []string{"-h"}, false},
		{"help is CLI", []string{"help"}, false},
		{"--version is CLI", []string{"--version"}, false},
		{"version is CLI", []string{"version"}, false},
		{"snapshot is CLI", []string{"snapshot"}, false},
		{"watch is CLI", []string{"watch", "--json"}, false},
		{"kill is CLI", []string{"kill", "123"}, false},
		{"mcp is CLI", []string{"mcp", "serve"}, false},
		{"v2 is CLI", []string{"v2"}, false},
		{"leading dash flag is CLI", []string{"--reload-server"}, false},
		{"unknown bareword launches TUI", []string{"wat"}, true},
	}
	for _, tc := range cases {
		if got := shouldLaunchTUI(tc.args); got != tc.want {
			t.Errorf("%s: shouldLaunchTUI(%v) = %v, want %v", tc.name, tc.args, got, tc.want)
		}
	}
}

func TestExtractGlobalFlags(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		want       []string
		wantPprof  string
		wantReload bool
		wantAddr   string
	}{
		{
			// The boolean --reload-server must NOT consume the next token.
			name:       "bool flag keeps following subcommand",
			args:       []string{"--reload-server", "snapshot"},
			want:       []string{"snapshot"},
			wantReload: true,
		},
		{
			name:     "value flag consumes its value",
			args:     []string{"--reload-addr", "127.0.0.1:9000", "snapshot"},
			want:     []string{"snapshot"},
			wantAddr: "127.0.0.1:9000",
		},
		{
			name:      "value flag with = is a single token",
			args:      []string{"--pprof=localhost:6060", "snapshot"},
			want:      []string{"snapshot"},
			wantPprof: "localhost:6060",
		},
		{
			// Regression: a global flag AFTER a subcommand is honored, not
			// silently dropped (flag.Parse used to stop at "watch").
			name:      "global flag after subcommand is honored",
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
			name: "unrelated flags pass through",
			args: []string{"snapshot", "--json"},
			want: []string{"snapshot", "--json"},
		},
		{
			name: "reload-server=false disables",
			args: []string{"--reload-server=false", "snapshot"},
			want: []string{"snapshot"},
		},
	}
	for _, tc := range cases {
		pprofAddr = ""
		reloadServer.enabled = false
		reloadServer.addr = ""
		got := extractGlobalFlags(append([]string(nil), tc.args...))
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: remainder = %v, want %v", tc.name, got, tc.want)
		}
		if pprofAddr != tc.wantPprof {
			t.Errorf("%s: pprofAddr = %q, want %q", tc.name, pprofAddr, tc.wantPprof)
		}
		if reloadServer.enabled != tc.wantReload {
			t.Errorf("%s: reloadServer.enabled = %v, want %v", tc.name, reloadServer.enabled, tc.wantReload)
		}
		if reloadServer.addr != tc.wantAddr {
			t.Errorf("%s: reloadServer.addr = %q, want %q", tc.name, reloadServer.addr, tc.wantAddr)
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
