package stacktrace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseJSFrameLineVariants(t *testing.T) {
	cases := []struct {
		name, line    string
		fn, file, abs string
		lineno, colno int
	}{
		{"plain", "    at flakyParse (/repo/app/workload.js:31:11)", "flakyParse", "/repo/app/workload.js", "/repo/app/workload.js", 31, 11},
		{"async named", "    at async fetchThing (/repo/app/net.js:12:3)", "async fetchThing", "/repo/app/net.js", "/repo/app/net.js", 12, 3},
		{"async anonymous", "    at async file:///repo/app/a.mjs:81:5", "", "/repo/app/a.mjs", "/repo/app/a.mjs", 81, 5},
		{"new", "    at new Client (/repo/app/client.js:5:9)", "new Client", "/repo/app/client.js", "/repo/app/client.js", 5, 9},
		{"method alias", "    at Timeout.tickErrors [as _onTimeout] (/repo/app/w.js:40:7)", "Timeout.tickErrors [as _onTimeout]", "/repo/app/w.js", "/repo/app/w.js", 40, 7},
		{"bare location", "    at /repo/app/index.js:1:1", "", "/repo/app/index.js", "/repo/app/index.js", 1, 1},
		{"file url", "    at f (file:///repo/app/w.js:31:11)", "f", "/repo/app/w.js", "/repo/app/w.js", 31, 11},
		{"file url escaped", "    at f (file:///repo/my%20app/w.js:3:1)", "f", "/repo/my app/w.js", "/repo/my app/w.js", 3, 1},
		{"windows file url", "    at f (file:///C:/work/app/w.js:3:1)", "f", "C:/work/app/w.js", "C:/work/app/w.js", 3, 1},
		{"windows path", `    at f (C:\work\app\w.js:3:1)`, "f", `C:\work\app\w.js`, `C:\work\app\w.js`, 3, 1},
		{"node internal", "    at listOnTimeout (node:internal/timers:685:17)", "listOnTimeout", "node:internal/timers", "", 685, 17},
		{"trailing brace", "    at Module.executeUserEntryPoint [as runMain] (node:internal/modules/run_main:154:5) {", "Module.executeUserEntryPoint [as runMain]", "node:internal/modules/run_main", "", 154, 5},
		{"bare trailing brace", "    at node:internal/main/run_main_module:33:47 {", "", "node:internal/main/run_main_module", "", 33, 47},
		{"anonymous native", "    at JSON.parse (<anonymous>)", "JSON.parse", "<anonymous>", "", 0, 0},
		{"promise all index", "    at async Promise.all (index 0)", "async Promise.all", "index 0", "", 0, 0},
		{"eval", "    at eval (eval at <anonymous> (/repo/app/index.js:10:5), <anonymous>:3:9)", "eval", "<anonymous>", "", 3, 9},
		{"node eval script", "    at f ([eval]:1:21)", "f", "[eval]", "", 1, 21},
		{"deno ext", "    at Object.readFileSync (ext:deno_node/fs.ts:423:15)", "Object.readFileSync", "ext:deno_node/fs.ts", "", 423, 15},
		{"bun six-space indent", "      at detonate (/repo/app/w.js:49:99)", "detonate", "/repo/app/w.js", "/repo/app/w.js", 49, 99},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, ok := parseJSFrameLine(tc.line)
			if !ok {
				t.Fatalf("parseJSFrameLine(%q) did not match", tc.line)
			}
			if f.Function != tc.fn || f.Filename != tc.file || f.AbsPath != tc.abs || f.Lineno != tc.lineno || f.Colno != tc.colno {
				t.Errorf("got {fn:%q file:%q abs:%q %d:%d}, want {fn:%q file:%q abs:%q %d:%d}",
					f.Function, f.Filename, f.AbsPath, f.Lineno, f.Colno, tc.fn, tc.file, tc.abs, tc.lineno, tc.colno)
			}
		})
	}
	if _, ok := parseJSFrameLine("Error: boom"); ok {
		t.Error("parseJSFrameLine matched a non-frame line")
	}
}

func TestSplitJSTypeValue(t *testing.T) {
	cases := map[string][2]string{
		"TypeError [ERR_INVALID_ARG_TYPE]: bad path": {"TypeError", "bad path"},
		"AssertionError [ERR_ASSERTION]: 1 !== 2":    {"AssertionError", "1 !== 2"},
		"Error: ENOENT: no such file":                {"Error", "ENOENT: no such file"},
		"ENOENT: no such file":                       {"ENOENT", "no such file"},
		"Error":                                      {"Error", ""},
		`"plain string failure"`:                     {"", "plain string failure"},
	}
	for in, want := range cases {
		typ, val := splitJSTypeValue(in)
		if typ != want[0] || val != want[1] {
			t.Errorf("splitJSTypeValue(%q) = %q, %q; want %q, %q", in, typ, val, want[0], want[1])
		}
	}
}

// Property blocks nested with [cause] at any depth and AggregateError's
// [errors] list (R0-5/R1-26/R1-28).
func TestJSNestedCausesAndErrorsList(t *testing.T) {
	text := `AggregateError: all failed
    at main (/repo/app/a.js:9:3) {
  [errors]: [
    Error: first
        at one (/repo/app/a.js:2:9),
    Error: second
        at two (/repo/app/a.js:3:9)
  ],
  [cause]: Error: level one
      at l1 (/repo/app/a.js:4:9) {
    code: 'E1',
    details: {
      retries: 3
    },
    [cause]: Error: level two
        at l2 (/repo/app/a.js:5:9) {
      [cause]: Error: level three
          at l3 (/repo/app/a.js:6:9)
    }
  }
}
still running`
	assertSummaries(t, Detect(text), []string{
		`js/ error handled AggregateError: all failed @main@a.js:9(1) <= Error: level one @l1@a.js:4(1) <= Error: level two @l2@a.js:5(1) <= Error: level three @l3@a.js:6(1)`,
	})
}

func TestGoFuncCallLines(t *testing.T) {
	text := `panic: boom [recovered]
	panic: again

goroutine 7 [running]:
example.com/app/internal/svc.(*Server).handle(0x140000a2000, {0x100b2c1a8, 0x5})
	/repo/app/internal/svc/server.go:42 +0x1c
example.com/app/internal/svc.Map[...](...)
	/repo/app/internal/svc/map.go:9
main.main.func1()
	/repo/app/main.go:12 +0x30
panic({0x1007e5460?, 0x100834e08?})
	/usr/local/go/src/runtime/panic.go:860 +0x12c
gopkg.in/yaml%2ev3.(*decoder).unmarshal(0x1400012c000)
	/home/dev/go/pkg/mod/gopkg.in/yaml.v3@v3.0.1/decode.go:500 +0x88
created by main.main in goroutine 1
	/repo/app/main.go:10 +0x70
exit status 2
`
	exs := Detect(text)
	if len(exs) != 1 {
		t.Fatalf("got %d events, want 1", len(exs))
	}
	var got []string
	for _, f := range exs[0].Frames {
		got = append(got, f.Function+"|"+f.Module+"|"+frameRef(f))
	}
	want := []string{
		"gopkg.in/yaml%2ev3.(*decoder).unmarshal|gopkg.in/yaml%2ev3|gopkg.in/yaml%2ev3.(*decoder).unmarshal@decode.go:500",
		"panic||panic@panic.go:860",
		"main.main.func1|main|main.main.func1@main.go:12",
		"example.com/app/internal/svc.Map[...]|example.com/app/internal/svc|example.com/app/internal/svc.Map[...]@map.go:9",
		"example.com/app/internal/svc.(*Server).handle|example.com/app/internal/svc|example.com/app/internal/svc.(*Server).handle@server.go:42",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("frames:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if exs[0].Value != "again" || len(exs[0].Chained) != 1 || exs[0].Chained[0].Value != "boom" {
		t.Errorf("value/chain = %q / %+v, want again <= boom", exs[0].Value, exs[0].Chained)
	}
}

func TestRecoveredPanicDump(t *testing.T) {
	stanza := "goroutine 9 [running]:\nmain.recoverer.func1()\n\t/repo/app/mw.go:20 +0x40\n" +
		"panic({0x1, 0x2})\n\t/usr/local/go/src/runtime/panic.go:860 +0x12c\n" +
		"main.handler(...)\n\t/repo/app/h.go:7\nmain.main()\n\t/repo/app/main.go:3 +0x10\n"
	assertSummaries(t, Detect("[Recovery] panic recovered: boom\n"+stanza), []string{
		`gopanic/go error handled panic: boom @main.handler@h.go:7(2)`,
	})
	for _, prev := range []string{"", "worker 3 stack:\n", "SIGQUIT: quit\n"} {
		if exs := Detect(prev + stanza); len(exs) != 0 {
			t.Errorf("goroutine dump after %q produced events: %s", prev, strings.Join(summarizeAll(exs), " | "))
		}
	}
}

func TestGopanicStopsAtFirstGoroutine(t *testing.T) {
	text := `fatal error: all goroutines are asleep - deadlock!

goroutine 1 [chan receive]:
main.main()
	/repo/app/main.go:10 +0x20

goroutine 2 [chan send]:
main.worker()
	/repo/app/main.go:20 +0x18
created by main.main in goroutine 1
	/repo/app/main.go:8 +0x30
`
	assertSummaries(t, Detect(text), []string{
		`gopanic/go fatal unhandled fatal error: all goroutines are asleep - deadlock! @main.main@main.go:10(1)`,
	})
}

// pkg/errors frames with receivers, generics and closures are all kept
// (R0-3/R1-24).
func TestPkgErrorsFramesWithReceiversAndGenerics(t *testing.T) {
	text := "2026-09-22T10:04:37.123Z\terror\tsvc/handler.go:20\trequest failed\t{}\n" +
		"db timeout\n" +
		"example.com/app/internal/svc.(*Server).handle\n\t/repo/app/internal/svc/server.go:13\n" +
		"example.com/app/internal/svc.Map[...]\n\t/repo/app/internal/svc/map.go:9\n" +
		"main.main.func1\n\t/repo/app/main.go:17\n" +
		"runtime.goexit\n\t/usr/local/go/src/runtime/asm_arm64.s:1447\n"
	assertSummaries(t, Detect(text), []string{
		`zap/go error handled "request failed" @example.com/app/internal/svc.(*Server).handle@server.go:13(4) [2026-09-22T10:04:37.123Z]`,
	})
}

func TestZapConsoleColumns(t *testing.T) {
	cases := []struct{ name, line, want string }{
		{"dev: upper-case level, caller", "2026-09-22T20:56:06.254-0600\tERROR\tapp/main.go:19\trequest failed\t{\"error\": \"x\"}",
			`message/go error handled "request failed" @-(0) [2026-09-23T02:56:06.254Z]`},
		{"named logger + caller", "2026-09-22T20:56:06.254-0600\tWARN\tbilling\tapp/main.go:19\tslow charge",
			`message/go warning handled "slow charge" @-(0) [2026-09-23T02:56:06.254Z]`},
		{"prod: epoch + lower-case level", "1.790137826326494e+09\terror\tapp/main.go:91\trequest failed\t{}",
			`message/go error handled "request failed" @-(0) [2026-09-23T04:30:26.326Z]`},
		{"no caller column", "2026-09-22T10:04:37.123Z\terror\trequest failed\t{}",
			`message/go error handled "request failed" @-(0) [2026-09-22T10:04:37.123Z]`},
		{"fatal", "2026-09-22T10:04:37.123Z\tfatal\tunrecoverable",
			`message/go fatal unhandled "unrecoverable" @-(0) [2026-09-22T10:04:37.123Z]`},
		{"dpanic", "2026-09-22T10:04:37.123Z\tDPANIC\tbad state",
			`message/go fatal unhandled "bad state" @-(0) [2026-09-22T10:04:37.123Z]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertSummaries(t, Detect(tc.line), []string{tc.want})
		})
	}
	for _, info := range []string{
		"2026-09-22T10:04:37.123Z\tinfo\tserver started\t{}",
		"2026-09-22T10:04:37.123-0600\tDEBUG\tapp/main.go:3\tcache warm",
		`{"level":"info","ts":1790137826.3,"msg":"server started"}`,
	} {
		if exs := Detect(info); len(exs) != 0 {
			t.Errorf("info-level zap entry produced events: %s", strings.Join(summarizeAll(exs), " | "))
		}
	}
}

func TestZapJSONWithEmbeddedStacktrace(t *testing.T) {
	line := `{"level":"error","ts":"2026-09-22T10:04:37.123Z","msg":"request failed","request_id":"abc123","stacktrace":"main.doWork\n\t/repo/app/main.go:20\nmain.main\n\t/repo/app/main.go:10"}`
	assertSummaries(t, Detect(line), []string{
		`zap/go error handled "request failed" @main.doWork@main.go:20(2) [2026-09-22T10:04:37.123Z]`,
	})
	// logrus' JSON formatter has zap's shape (string level, msg, a time
	// key) -- a Go logger too -- but only "ts"/"T" count as zap's time key.
	if exs := Detect(`{"level":"error","msg":"x","time":"2026-09-22T10:00:00Z"}`); len(exs) != 0 {
		t.Errorf("JSON without a zap ts key produced events: %s", strings.Join(summarizeAll(exs), " | "))
	}
}

// tslog frames are crash-first as printed and must come out oldest ->
// newest with the crash frame last (R0-10/R1-31).
func TestTslogFrameOrder(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "synthetic", "tslog.txt"))
	if err != nil {
		t.Fatal(err)
	}
	exs := Detect(string(b))
	if len(exs) != 1 {
		t.Fatalf("got %d events, want 1", len(exs))
	}
	ex := exs[0]
	if len(ex.Frames) != 2 || ex.Frames[0].Function != "Timeout._onTimeout" || frameRef(ex.Frames[1]) != "runActivity@workload.js:80" {
		t.Errorf("frames = %+v, want [Timeout._onTimeout, runActivity:80] (crash last)", ex.Frames)
	}
	if len(ex.Chained) != 1 || frameRef(ex.Chained[0].Frames[len(ex.Chained[0].Frames)-1]) != "doWork@workload.js:90" {
		t.Errorf("cause = %+v, want crash frame doWork:90 last", ex.Chained)
	}
	// A tslog error line with no frames is a message-only event.
	assertSummaries(t, Detect("2026-09-22T10:05:00.000Z ERROR [wf] activity task failed"), []string{
		`message/node error handled "activity task failed" @-(0) [2026-09-22T10:05:00.000Z]`,
	})
}

// Python disposition matrix (R0-7/R1-30).
func TestPythonHandledSignals(t *testing.T) {
	moduleTrace := "Traceback (most recent call last):\n" +
		`  File "/repo/app/main.py", line 3, in <module>` + "\n    run()\n" +
		"ValueError: bad\n"
	cases := []struct{ name, text, want string }{
		{"module level uncaught", moduleTrace,
			`python/python fatal unhandled ValueError: bad @<module>@main.py:3(1)`},
		{"module level logging.exception", "ERROR:root:parse failed\n" + moduleTrace,
			`python/python error handled ValueError: bad @<module>@main.py:3(1)`},
		{"asctime logger line before", "2026-09-22 10:04:37,123 ERROR app: parse failed\n" + moduleTrace,
			`python/python error handled ValueError: bad @<module>@main.py:3(1) [2026-09-22T16:04:37.123Z]`},
		{"flask-style logger line before", "ERROR in app: Exception on /items [GET]\n" + moduleTrace,
			`python/python error handled ValueError: bad @<module>@main.py:3(1)`},
		{"thread crash", "Exception in thread Thread-1 (worker):\n" + strings.Replace(moduleTrace, "<module>", "run", 1),
			`python/python fatal unhandled ValueError: bad @run@main.py:3(1)`},
		{"caught below the top level", strings.Replace(moduleTrace, "<module>", "handler", 1),
			`python/python error handled ValueError: bad @handler@main.py:3(1)`},
		{"python -m", "Traceback (most recent call last):\n" +
			`  File "<frozen runpy>", line 198, in _run_module_as_main` + "\n" +
			`  File "/repo/app/pkg/w.py", line 2, in load` + "\n" +
			"RuntimeError: x\n",
			`python/python fatal unhandled RuntimeError: x @load@w.py:2(2)`},
		{"message only warning", "WARNING:root:disk almost full",
			`message/python warning handled "disk almost full" @-(0)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertSummaries(t, detectIn(tc.text, testZone), []string{tc.want})
		})
	}
}

// Ruby 3.4 error_highlight snippets do not end the block, and causes are
// linked (R1-32).
func TestRubyHighlightAndCauses(t *testing.T) {
	assertSummaries(t, Detect(readFixture(t, "real/ruby/nomethod.txt")), []string{
		`ruby/ruby fatal unhandled NoMethodError: undefined method 'baz' for nil @Foo#bar@scenarios.rb:5(3)`,
	})
	// Older Ruby quoting (backtick-quote) is still recognized.
	old := "app.rb:3:in `run': boom (RuntimeError)\n\tfrom app.rb:9:in `<main>'\n"
	assertSummaries(t, Detect(old), []string{`ruby/ruby fatal unhandled RuntimeError: boom @run@app.rb:3(2)`})
	// A highlight snippet with nothing after it keeps the header frame.
	top := "app.rb:1:in '<main>': undefined method 'baz' for nil (NoMethodError)\n\nnil.baz\n   ^^^^\n"
	assertSummaries(t, Detect(top), []string{`ruby/ruby fatal unhandled NoMethodError: undefined method 'baz' for nil @<main>@app.rb:1(1)`})
}

func TestLevelFromLogPrefix(t *testing.T) {
	cases := map[string]string{
		"ERROR": LevelError, "error": LevelError, "ERR": LevelError,
		"WARN": LevelWarning, "warning": LevelWarning,
		"CRITICAL": LevelFatal, "fatal": LevelFatal, "DPANIC": LevelFatal, "panic": LevelFatal,
		"info": "", "DEBUG": "", "": "", "nonsense": "",
	}
	for tok, want := range cases {
		got, ok := levelFromLogPrefix(tok)
		if got != want || ok != (want != "") {
			t.Errorf("levelFromLogPrefix(%q) = %q, %v; want %q", tok, got, ok, want)
		}
	}
}

func TestChainedCausesInheritDisposition(t *testing.T) {
	exs := Detect(readFixture(t, "real/python/chain3.txt"))
	if len(exs) != 1 {
		t.Fatalf("got %d events", len(exs))
	}
	for _, c := range exs[0].Chained {
		if c.Runtime != "python" || c.Parser != "python" || c.Level != LevelFatal || c.Handled == nil || *c.Handled {
			t.Errorf("cause %+v does not share the event's runtime/parser/level/handled", c)
		}
	}
}
