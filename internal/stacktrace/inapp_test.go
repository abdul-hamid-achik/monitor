package stacktrace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInApp(t *testing.T) {
	const root = "/work/app"
	cases := []struct {
		name string
		f    Frame
		want bool
	}{
		// Application code.
		{"app file under root", Frame{AbsPath: "/work/app/src/server.js"}, true},
		{"project's own go internal package", Frame{AbsPath: "/work/app/internal/svc/svc.go"}, true},
		{"gopath-mode project inside the root", Frame{AbsPath: "/work/app/go/src/example.com/tool/main.go"}, true},
		{"root with a trailing slash", Frame{AbsPath: "/work/app/main.go"}, true},
		{"relative ruby filename", Frame{Filename: "workload.rb"}, true},
		{"relative src/ python path", Frame{Filename: "src/app.py"}, true},
		{"relative src/ ts path", Frame{Filename: "src/index.ts"}, true},
		{"relative ./ path", Frame{Filename: "./lib/worker.rb"}, true},
		{"go -trimpath module path", Frame{Filename: "example.com/app/internal/svc/svc.go"}, true},
		{"file url under root", Frame{Filename: "file:///work/app/src/a.mjs"}, true},
		{"absolute path with spaces", Frame{AbsPath: "/work/app/My Scripts/run.js"}, true},

		// Third-party code, even inside the root.
		{"node_modules", Frame{AbsPath: "/work/app/node_modules/express/lib/router.js"}, false},
		{"venv site-packages", Frame{AbsPath: "/work/app/.venv/lib/python3.14/site-packages/requests/api.py"}, false},
		{"go vendor", Frame{AbsPath: "/work/app/vendor/github.com/pkg/errors/errors.go"}, false},
		{"bundler gems", Frame{AbsPath: "/work/app/vendor/bundle/ruby/3.4.0/gems/rack-3.0/lib/rack.rb"}, false},
		{"relative node_modules", Frame{Filename: "node_modules/x/index.js"}, false},
		{"go -trimpath dependency", Frame{Filename: "github.com/pkg/errors@v0.9.1/errors.go"}, false},

		// Outside the root: stdlib, runtimes, caches.
		{"goroot", Frame{AbsPath: "/usr/local/go/src/runtime/proc.go"}, false},
		{"gopath module cache", Frame{AbsPath: "/home/dev/go/pkg/mod/github.com/pkg/errors@v0.9.1/errors.go"}, false},
		{"python stdlib", Frame{AbsPath: "/usr/local/lib/python3.14/threading.py"}, false},
		{"ruby stdlib", Frame{AbsPath: "/usr/lib/ruby/3.4.0/logger.rb"}, false},
		{"sibling directory with the root as prefix", Frame{AbsPath: "/work/app2/main.go"}, false},
		{"the root itself", Frame{AbsPath: "/work/app"}, false},
		{"go -trimpath stdlib", Frame{Filename: "runtime/proc.go"}, false},
		{"relative parent path", Frame{Filename: "../other/x.rb"}, false},

		// Pseudo paths.
		{"node scheme events", Frame{Filename: "node:events"}, false},
		{"node scheme fs", Frame{Filename: "node:fs"}, false},
		{"node internal", Frame{Filename: "node:internal/timers"}, false},
		{"legacy node internal", Frame{Filename: "internal/timers.js"}, false},
		{"bun scheme", Frame{Filename: "bun:main"}, false},
		{"deno scheme", Frame{Filename: "deno:core"}, false},
		{"deno ext", Frame{Filename: "ext:deno_node/internal/timers.mjs"}, false},
		{"deno remote module", Frame{Filename: "https://deno.land/std@0.224.0/http/server.ts"}, false},
		{"anonymous", Frame{Filename: "<anonymous>"}, false},
		{"frozen runpy", Frame{Filename: "<frozen runpy>"}, false},
		{"ruby internal", Frame{Filename: "<internal:kernel>"}, false},
		{"python string", Frame{Filename: "<string>"}, false},
		{"node eval", Frame{Filename: "[eval]"}, false},
		{"node eval wrapper", Frame{Filename: "[eval]-wrapper"}, false},
		{"promise.all index", Frame{Filename: "index 0"}, false},
		{"native", Frame{Filename: "native"}, false},
		{"no extension", Frame{Filename: "bin/server"}, false},
		{"empty frame", Frame{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := root
			if tc.name == "root with a trailing slash" {
				r = root + "/"
			}
			if got := InApp(tc.f, r); got != tc.want {
				t.Errorf("InApp(%+v, %q) = %v, want %v", tc.f, r, got, tc.want)
			}
		})
	}
}

func TestInAppWindowsPaths(t *testing.T) {
	cases := []struct {
		name, root string
		f          Frame
		want       bool
	}{
		{"under root", `C:\work\app`, Frame{AbsPath: `C:\work\app\src\x.js`}, true},
		{"under root, other case and slashes", `C:\Work\App`, Frame{AbsPath: `c:/work/app/src/x.js`}, true},
		{"other directory", `C:\work\app`, Frame{AbsPath: `C:\other\lib\x.js`}, false},
		{"node_modules", `C:\work\app`, Frame{AbsPath: `C:\work\app\node_modules\x\i.js`}, false},
		{"posix root, windows frame", "/work/app", Frame{AbsPath: `C:\other\lib\x.js`}, false},
		{"windows root, posix frame", `C:\work\app`, Frame{AbsPath: "/work/app/x.js"}, false},
		{"unc path", `C:\work\app`, Frame{AbsPath: `\\server\share\x.js`}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := InApp(tc.f, tc.root); got != tc.want {
				t.Errorf("InApp(%+v, %q) = %v, want %v", tc.f, tc.root, got, tc.want)
			}
		})
	}
}

func TestInAppNoGitRoot(t *testing.T) {
	for _, f := range []Frame{{AbsPath: "/repo/app/main.js"}, {Filename: "workload.rb"}} {
		if InApp(f, "") {
			t.Errorf("InApp(%+v, \"\") = true, want false without a git root", f)
		}
	}
}

// ApplyGitRoot relativizes Filename for frames under the root (outer and
// chained), keeps AbsPath, and marks InApp (R0-12/R1-37, R0-6/R1-29).
func TestApplyGitRoot(t *testing.T) {
	exs := detectIn(readFixture(t, "real/node/enoent-handled.txt"), testZone)
	if len(exs) != 1 {
		t.Fatalf("got %d events, want 1", len(exs))
	}
	ex := exs[0]
	ApplyGitRoot(ex, "/repo/app")
	var got []string
	for _, f := range ex.Frames {
		got = append(got, frameSummary(f))
	}
	want := []string{
		"async asyncRunEntryPointWithESMLoader|node:internal/modules/run_main||101:5|false",
		"|node:internal/modules/esm/loader||650:26|false",
		"ModuleJob.run|node:internal/modules/esm/module_job||569:25|false",
		"|src/scenarios.mjs|/repo/app/src/scenarios.mjs|68:7|true",
		"readConfig|src/scenarios.mjs|/repo/app/src/scenarios.mjs|24:13|true",
		"Object.readFileSync|node:fs||539:20|false",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("frames after ApplyGitRoot:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// Chained causes are resolved too.
	exs = detectIn(readFixture(t, "real/node/uncaught.txt"), testZone)
	ApplyGitRoot(exs[0], "/repo/app")
	root := exs[0].Chained[len(exs[0].Chained)-1]
	crash := root.Frames[len(root.Frames)-1]
	if crash.Filename != "src/scenarios.mjs" || crash.AbsPath != "/repo/app/src/scenarios.mjs" || !crash.InApp || crash.Function != "root" {
		t.Errorf("innermost cause crash frame = %+v, want in-app root@src/scenarios.mjs", crash)
	}

	// Go: the project's own internal/ package is in-app and relative;
	// GOROOT is not.
	exs = detectIn(readFixture(t, "real/go/recovered.txt"), testZone)
	ApplyGitRoot(exs[0], "/repo/app")
	for _, f := range exs[0].Frames {
		wantIn := !strings.HasPrefix(f.AbsPath, "/usr/local/go/")
		if f.InApp != wantIn {
			t.Errorf("go frame %+v: in_app = %v, want %v", f, f.InApp, wantIn)
		}
	}
	exs = detectIn(readFixture(t, "real/go/nil.txt"), testZone)
	ApplyGitRoot(exs[0], "/repo/app")
	top := exs[0].Frames[len(exs[0].Frames)-1]
	if top.Filename != "internal/svc/svc.go" || !top.InApp || top.Module != "example.com/app/internal/svc" {
		t.Errorf("go nil-deref crash frame = %+v, want in-app internal/svc/svc.go in module example.com/app/internal/svc", top)
	}

	// Python -m: runpy's frozen frames are not in-app.
	exs = detectIn(readFixture(t, "real/python/dash-m.txt"), testZone)
	ApplyGitRoot(exs[0], "/repo/app")
	for _, f := range exs[0].Frames {
		if f.Filename == "<frozen runpy>" && f.InApp {
			t.Errorf("runpy frame marked in-app: %+v", f)
		}
	}
	if f := exs[0].Frames[len(exs[0].Frames)-1]; f.Filename != "pkg/worker.py" || !f.InApp {
		t.Errorf("python -m crash frame = %+v, want in-app pkg/worker.py", f)
	}

	// Ruby: relative filenames are in-app, <internal:kernel> is not.
	exs = detectIn(readFixture(t, "dogfood/ruby.stderr.txt"), testZone)
	ApplyGitRoot(exs[0], "/repo")
	for _, f := range exs[0].Frames {
		if (f.Filename == "<internal:kernel>") == f.InApp {
			t.Errorf("ruby frame %+v: in_app = %v", f, f.InApp)
		}
	}

	// No root: nothing is in-app and nothing is rewritten.
	exs = detectIn(readFixture(t, "real/node/enoent-handled.txt"), testZone)
	ApplyGitRoot(exs[0], "")
	for _, f := range exs[0].Frames {
		if f.InApp || strings.HasPrefix(f.Filename, "src/") {
			t.Errorf("frame %+v changed without a git root", f)
		}
	}
	ApplyGitRoot(nil, "/repo") // must not panic
}

func frameSummary(f Frame) string {
	return strings.Join([]string{f.Function, f.Filename, f.AbsPath, itoa(f.Lineno) + ":" + itoa(f.Colno), map[bool]string{true: "true", false: "false"}[f.InApp]}, "|")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestInAppMatchesEitherRootSpelling is CC-4's regression: a repo reached
// through a symlink (macOS /tmp -> /private/tmp, a symlinked ~/code) must
// still classify frames in-app no matter which spelling the root was
// resolved from and which the runtime printed. A logical root with
// physical frame paths (Node realpaths, Python's os.getcwd) and the
// reverse must both match, and ApplyGitRoot must store the SAME relative
// Filename either way, so the fingerprint never depends on the spelling.
func TestInAppMatchesEitherRootSpelling(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "app.js"), []byte("boom\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	physFile := filepath.Join(real, "app.js")
	logFile := filepath.Join(link, "app.js")
	// The TRUE physical spelling (on macOS t.TempDir itself sits under
	// /var -> /private/var, so `real` above is still logical).
	physBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatalf("EvalSymlinks(base): %v", err)
	}
	truePhysFile := filepath.Join(physBase, "real", "app.js")

	// Logical root + physical frame (the review's /tmp vs /private/tmp
	// evidence shape).
	if !InApp(Frame{AbsPath: physFile}, link) {
		t.Errorf("InApp(physical frame, logical root) = false, want true")
	}
	// Fully-resolved frame against the logical root: the root's own
	// physical spelling must match it with no frame resolution.
	if !InApp(Frame{AbsPath: truePhysFile}, link) {
		t.Errorf("InApp(resolved frame, logical root) = false, want true")
	}
	// Physical root + logical frame.
	if !InApp(Frame{AbsPath: logFile}, real) {
		t.Errorf("InApp(logical frame, physical root) = false, want true")
	}
	// A frame genuinely outside either spelling stays out.
	outside := filepath.Join(base, "elsewhere", "x.js")
	if InApp(Frame{AbsPath: outside}, link) || InApp(Frame{AbsPath: outside}, real) {
		t.Errorf("InApp(outside frame) = true, want false in both spellings")
	}
	// ApplyGitRoot stores the identical relative Filename either way.
	for _, root := range []string{link, real} {
		ex := &Exception{Frames: []Frame{{AbsPath: physFile}, {AbsPath: logFile}}}
		ApplyGitRoot(ex, root)
		for _, f := range ex.Frames {
			if !f.InApp || f.Filename != "app.js" {
				t.Errorf("root %s: frame %+v, want in-app app.js", root, f)
			}
		}
	}
}
