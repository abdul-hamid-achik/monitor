package issues

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/monitor/internal/stacktrace"
)

// writeSource writes content to a file named name under a fresh temp dir
// and returns the file's absolute path. Fingerprint tests use it to pin an
// anonymous frame's context line to real, readable source.
func writeSource(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// anonException builds the minimal shape an anonymous JS callback crash
// produces after ApplyGitRoot: one in-app frame with an empty Function, a
// git-root-relative Filename and the absolute path it came from.
func anonException(absPath string, line int) stacktrace.Exception {
	return stacktrace.Exception{
		Type:    "TypeError",
		Value:   "Cannot read properties of null",
		Runtime: "node",
		Frames: []stacktrace.Frame{{
			Function: "",
			Filename: filepath.Base(absPath),
			AbsPath:  absPath,
			Lineno:   line,
			InApp:    true,
		}},
	}
}

// TestFingerprintV2ExceptionSeparatesAnonymousFramesInOneFile is CC-5's
// core property: two truly anonymous callbacks (Express/Koa-style, V8
// prints "at /path/routes.js:3:50" with no symbol at all) in ONE file must
// not share an issue just because their Function is empty. The
// context-line hash separates them, and the same callback keeps a stable
// fingerprint across crashes.
func TestFingerprintV2ExceptionSeparatesAnonymousFramesInOneFile(t *testing.T) {
	src := writeSource(t, "routes.js", "const r = new Map()\n"+
		"r.set('a', () => u.name) // reading name\n"+
		"r.set('b', () => u.id)   // reading id\n")
	a := FingerprintV2Exception(anonException(src, 2), "app")
	b := FingerprintV2Exception(anonException(src, 3), "app")
	if a == b {
		t.Errorf("two anonymous callbacks on different lines share fingerprint %s; they must group apart (CC-5)", a)
	}
	// The same callback, seen twice (a retry, a second crash), groups.
	if a != FingerprintV2Exception(anonException(src, 2), "app") {
		t.Errorf("the same anonymous callback changed fingerprint across crashes; it must stay stable (CC-5)")
	}
	// The context hash normalizes whitespace: reindenting the callback's
	// own line keeps the issue, it is still the same code.
	shifted := writeSource(t, "routes.js", "const r = new Map()\n"+
		"r.set('a', ()    => u.name) // reading name\n"+
		"r.set('b', () => u.id)      // reading id\n")
	if a != FingerprintV2Exception(anonException(shifted, 2), "app") {
		t.Errorf("whitespace-only reindent of the context line changed the anonymous fingerprint; the hash must normalize whitespace (CC-5)")
	}
}

// TestFingerprintV2ExceptionAnonymousFallsBackToLineNumber pins the
// fallback half of CC-5: when the source is not readable (AbsPath empty,
// file gone), the line number itself separates two anonymous frames in one
// file.
func TestFingerprintV2ExceptionAnonymousFallsBackToLineNumber(t *testing.T) {
	at2 := anonException("/definitely/not/readable/routes.js", 2)
	at3 := anonException("/definitely/not/readable/routes.js", 3)
	if FingerprintV2Exception(at2, "app") == FingerprintV2Exception(at3, "app") {
		t.Errorf("unreadable source merged two anonymous frames on different lines; the line-number fallback must separate them (CC-5)")
	}
	if FingerprintV2Exception(at2, "app") != FingerprintV2Exception(at2, "app") {
		t.Errorf("the same unreadable anonymous frame is not deterministic")
	}
}

// TestFingerprintV2ExceptionPythonGenericMarkers covers the Python shapes
// named in CC-5: "<lambda>" and "<module>" are generic markers, so they get
// the context-line disambiguator too instead of merging every lambda (or
// every module-level crash) in a file.
func TestFingerprintV2ExceptionPythonGenericMarkers(t *testing.T) {
	src := writeSource(t, "app.py", "import json\n"+
		"first = lambda n: n.missing\n"+
		"second = lambda n: n.other\n"+
		"json.loads('{oops')\n")
	ex := func(fn string, line int) stacktrace.Exception {
		return stacktrace.Exception{
			Type:    "AttributeError",
			Value:   "'NoneType' object has no attribute",
			Runtime: "python",
			Frames: []stacktrace.Frame{{
				Function: fn,
				Filename: "app.py",
				AbsPath:  src,
				Lineno:   line,
				InApp:    true,
			}},
		}
	}
	if FingerprintV2Exception(ex("<lambda>", 2), "app") == FingerprintV2Exception(ex("<lambda>", 3), "app") {
		t.Errorf("two '<lambda>' frames on different lines share a fingerprint; the marker must not collapse them (CC-5)")
	}
	if FingerprintV2Exception(ex("<module>", 4), "app") == FingerprintV2Exception(ex("<module>", 2), "app") {
		t.Errorf("'<module>' at different lines shares a fingerprint; the marker must not collapse them (CC-5)")
	}
	// Qualified V8 anonymity ("Object.<anonymous>", "async <anonymous>")
	// is anonymous too.
	if FingerprintV2Exception(ex("Object.<anonymous>", 2), "app") == FingerprintV2Exception(ex("Object.<anonymous>", 3), "app") {
		t.Errorf("'Object.<anonymous>' at different lines shares a fingerprint; qualified markers are anonymous frames too (CC-5)")
	}
}

// TestFingerprintV2ExceptionNamedFramesKeepIgnoringLineNumbers guards the
// other side of the revision: a frame with a real symbol keeps the plain
// "func@relfile" token with no context hash, so moving the crash line (an
// inserted log, a refactor) never forks a named-function issue.
func TestFingerprintV2ExceptionNamedFramesKeepIgnoringLineNumbers(t *testing.T) {
	src := writeSource(t, "users.js", "function loadUser(id) {\n  throw new Error('missing ' + id)\n}\n")
	at := func(line int) stacktrace.Exception {
		ex := anonException(src, line)
		ex.Frames[0].Function = "loadUser"
		return ex
	}
	if FingerprintV2Exception(at(2), "app") != FingerprintV2Exception(at(10), "app") {
		t.Errorf("a named frame's fingerprint changed with its line number; named tokens must stay line-free (naming ADR §6)")
	}
}

// TestFingerprintV2ExceptionGoClosureRenumberingStable is CC-8's property:
// adding an unrelated closure above the crashing one renumbers
// main.main.func1 to main.main.func2 and moves its line, and the
// fingerprint must not fork the issue. The context line of the crashing
// closure itself is unchanged, so the normalized token "main.main.func*"
// plus context hash stays equal.
func TestFingerprintV2ExceptionGoClosureRenumberingStable(t *testing.T) {
	v1 := writeSource(t, "main.go", "package main\n"+
		"func main() {\n"+
		"	handler := func(d int) int { return 10 / d }\n"+
		"	_ = handler(0)\n"+
		"}\n")
	before := stacktrace.Exception{
		Type:    "panic",
		Value:   "runtime error: integer divide by zero",
		Runtime: "go",
		Frames: []stacktrace.Frame{{
			Function: "main.main.func1",
			Filename: "main.go",
			AbsPath:  v1,
			Lineno:   3,
			InApp:    true,
		}},
	}
	v2 := writeSource(t, "main.go", "package main\n"+
		"func main() {\n"+
		"	logf := func(s string) { println(s) }\n"+
		"	logf(\"unrelated\")\n"+
		"	handler := func(d int) int { return 10 / d }\n"+
		"	_ = handler(0)\n"+
		"}\n")
	after := stacktrace.Exception{
		Type:    "panic",
		Value:   "runtime error: integer divide by zero",
		Runtime: "go",
		Frames: []stacktrace.Frame{{
			Function: "main.main.func2", // renumbered by the added logf closure
			Filename: "main.go",
			AbsPath:  v2,
			Lineno:   5, // and moved down two lines
			InApp:    true,
		}},
	}
	got := FingerprintV2Exception(before, "app")
	want := FingerprintV2Exception(after, "app")
	if got != want {
		t.Errorf("closure renumbering forked the issue: %s (func1@line3) != %s (func2@line5); the suffix must normalize (CC-8)", got, want)
	}
}

// TestFingerprintV2ExceptionTwoClosuresInOneFunctionDiffer pins why the
// closure token carries a context hash at all: normalizing "func1" and
// "func2" to "func*" alone would merge two DIFFERENT closures in one
// function, which CC-8's fix must not do.
func TestFingerprintV2ExceptionTwoClosuresInOneFunctionDiffer(t *testing.T) {
	src := writeSource(t, "main.go", "package main\n"+
		"func main() {\n"+
		"	a := func() int { return 1 / 0 }\n"+
		"	b := func() int { return 2 / 0 }\n"+
		"	_ = a\n"+
		"	_ = b\n"+
		"}\n")
	ex := func(fn string, line int) stacktrace.Exception {
		return stacktrace.Exception{
			Type:    "panic",
			Value:   "runtime error: integer divide by zero",
			Runtime: "go",
			Frames: []stacktrace.Frame{{
				Function: fn,
				Filename: "main.go",
				AbsPath:  src,
				Lineno:   line,
				InApp:    true,
			}},
		}
	}
	if FingerprintV2Exception(ex("main.main.func1", 3), "app") == FingerprintV2Exception(ex("main.main.func2", 4), "app") {
		t.Errorf("two different closures in one function share a fingerprint after normalization; the context hash must keep them apart (CC-8)")
	}
}

// TestFingerprintFrameTokenForms pins the exact token grammar on the forms
// the revision names: anonymous, qualified anonymous, every Go closure
// suffix shape (func, gowrap, nested .N, glob..funcN), and the untouched
// named-function and non-Go "func1" cases.
func TestFingerprintFrameTokenForms(t *testing.T) {
	src := writeSource(t, "main.go", "package main\nfunc boom() { panic(1) }\n")
	cache := make(map[string][]byte)
	anon := stacktrace.Frame{Filename: "main.go", AbsPath: src, Lineno: 2, InApp: true}
	named := anon
	named.Function = "main.boom"

	got := fingerprintFrameToken(anon, false, cache)
	if !strings.HasPrefix(got, "<anon>:") || !strings.HasSuffix(got, "@main.go") {
		t.Errorf("anonymous token = %q, want \"<anon>:<ctx>@main.go\"", got)
	}

	got = fingerprintFrameToken(named, false, cache)
	if got != "main.boom@main.go" {
		t.Errorf("named non-Go token = %q, want plain \"main.boom@main.go\"", got)
	}

	// A JS function literally named "Object.load" (or "x.func1") is NOT a
	// Go closure: no closure normalization outside Runtime "go".
	js := stacktrace.Frame{Function: "Object.func1", Filename: "app.js", AbsPath: src, Lineno: 2, InApp: true}
	if got := fingerprintFrameToken(js, false, cache); got != "Object.func1@app.js" {
		t.Errorf("non-Go funcN token = %q, want it untouched outside Go", got)
	}

	for _, fn := range []string{"main.main.func1", "main.main.gowrap1", "main.main.func1.2", "glob..func1"} {
		f := stacktrace.Frame{Function: fn, Filename: "main.go", AbsPath: src, Lineno: 2, InApp: true}
		got := fingerprintFrameToken(f, true, cache)
		wantSuffix := map[string]string{
			"main.main.func1":   "main.main.func*:",
			"main.main.gowrap1": "main.main.gowrap*:",
			"main.main.func1.2": "main.main.func*:",
			"glob..func1":       "glob..func*:",
		}[fn]
		if !strings.HasPrefix(got, wantSuffix) {
			t.Errorf("closure token for %s = %q, want prefix %q", fn, got, wantSuffix)
		}
	}
}

// TestCulpritUnchangedForAnonymousFrames pins that the CC-5/CC-8 revision
// touches only the fingerprint token: the display culprit keeps the
// function name exactly as the runtime printed it (empty for a truly
// anonymous frame) and the exact crash line.
func TestCulpritUnchangedForAnonymousFrames(t *testing.T) {
	ex := anonException("/tmp/app/routes.js", 3)
	c := culpritFor(ex)
	if c == nil {
		t.Fatal("culpritFor returned nil for an in-app anonymous frame")
	}
	if c.Function != "" {
		t.Errorf("culprit.Function = %q, want \"\" exactly as printed (the fingerprint revision must not touch display)", c.Function)
	}
	if c.File != "routes.js" || c.Line != 3 {
		t.Errorf("culprit = %s:%d, want routes.js:3", c.File, c.Line)
	}
}
