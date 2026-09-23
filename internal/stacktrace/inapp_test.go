package stacktrace

import "testing"

func TestInApp(t *testing.T) {
	const gitRoot = "/repo"
	cases := []struct {
		name string
		f    Frame
		want bool
	}{
		{"app file under root", Frame{AbsPath: "/repo/examples/polyglot/js/workload.js"}, true},
		{"relative ruby filename", Frame{Filename: "workload.rb"}, true},
		{"node_modules", Frame{AbsPath: "/repo/node_modules/foo/index.js"}, false},
		{"site-packages", Frame{AbsPath: "/repo/.venv/lib/python3.14/site-packages/foo/bar.py"}, false},
		{"dist-packages", Frame{AbsPath: "/usr/lib/python3/dist-packages/foo.py"}, false},
		{"vendor", Frame{AbsPath: "/repo/vendor/github.com/foo/bar.go"}, false},
		{"gems", Frame{AbsPath: "/repo/.bundle/gems/foo-1.0/lib/foo.rb"}, false},
		{"usr lib ruby", Frame{AbsPath: "/usr/lib/ruby/3.4.0/foo.rb"}, false},
		{"goroot src", Frame{AbsPath: "/opt/homebrew/Cellar/go/1.26/libexec/src/runtime/panic.go"}, false},
		{"node internal scheme", Frame{Filename: "node:internal/timers"}, false},
		{"node legacy internal", Frame{AbsPath: "internal/timers.js"}, false},
		{"bun scheme", Frame{Filename: "bun:main"}, false},
		{"deno scheme", Frame{Filename: "deno:core"}, false},
		{"ext scheme", Frame{Filename: "ext:deno_node/internal/timers.mjs"}, false},
		{"anonymous", Frame{Filename: "<anonymous>"}, false},
		{"outside git root", Frame{AbsPath: "/Users/dev/other/project/main.go"}, false},
		{"empty frame", Frame{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := InApp(tc.f, gitRoot); got != tc.want {
				t.Errorf("InApp(%+v, %q) = %v, want %v", tc.f, gitRoot, got, tc.want)
			}
		})
	}
}

func TestInAppNoGitRoot(t *testing.T) {
	f := Frame{AbsPath: "/repo/examples/polyglot/js/workload.js"}
	if InApp(f, "") {
		t.Error("InApp with empty gitRoot should be false for an absolute path")
	}
}
