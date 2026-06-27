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

func TestStripParsedFlags(t *testing.T) {
	valueTaking := map[string]bool{"reload-addr": true}
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{
			// Regression: the boolean --reload-server must NOT consume the
			// following subcommand token.
			name: "bool flag keeps following subcommand",
			args: []string{"--reload-server", "snapshot"},
			want: []string{"snapshot"},
		},
		{
			name: "value flag consumes its value",
			args: []string{"--reload-addr", "127.0.0.1:9000", "snapshot"},
			want: []string{"snapshot"},
		},
		{
			name: "value flag with = is a single token",
			args: []string{"--reload-addr=127.0.0.1:9000", "snapshot"},
			want: []string{"snapshot"},
		},
		{
			name: "bool then value flag then subcommand",
			args: []string{"--reload-server", "--reload-addr", "x", "watch"},
			want: []string{"watch"},
		},
		{
			name: "-- terminator is dropped",
			args: []string{"--", "kill", "1"},
			want: []string{"kill", "1"},
		},
		{
			name: "unrelated flags pass through",
			args: []string{"snapshot", "--json"},
			want: []string{"snapshot", "--json"},
		},
	}
	for _, tc := range cases {
		// stripParsedFlags reuses the backing array (args[:0]); copy so a
		// case can't clobber the next.
		in := append([]string(nil), tc.args...)
		got := stripParsedFlags(in, valueTaking, "reload-server", "reload-addr")
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: stripParsedFlags(%v) = %v, want %v", tc.name, tc.args, got, tc.want)
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
