package stacktrace

import (
	"strings"
	"testing"
)

const goCrashText = "panic: boom\n\ngoroutine 1 [running]:\nmain.main()\n\t/repo/main.go:11 +0x60\n"

const pyTraceText = `Traceback (most recent call last):
  File "/repo/app/main.py", line 3, in <module>
    run()
  File "/repo/app/main.py", line 2, in run
    raise ValueError("bad")
ValueError: bad
`

const jsTypeErrorText = "TypeError: cannot read x\n    at handler (/repo/app/h.js:3:9)\n    at main (/repo/app/h.js:9:1)\n"

// TestNewBlocksAreNeverSwallowed replays the reviewers' repros (R0-1,
// R1-20, R1-21): a line that clearly starts a new block closes the open one
// before any continuation rule runs, and continuation rules stay strict.
func TestNewBlocksAreNeverSwallowed(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "js message line then noise then python traceback",
			text: "Error: connection refused, retrying in 5s\nretrying...\n" + pyTraceText,
			want: []string{`python/python fatal unhandled ValueError: bad @run@main.py:2(2)`},
		},
		{
			name: "zap error, zap info, then a go panic",
			text: "2026-09-22T10:04:37.123Z\terror\trequest failed\t{}\n" +
				"2026-09-22T10:04:37.200Z\tinfo\tretrying\t{}\n" + goCrashText,
			want: []string{
				`message/go error handled "request failed" @-(0) [2026-09-22T10:04:37.123Z]`,
				`gopanic/go fatal unhandled panic: boom @main.main@main.go:11(1) [2026-09-22T10:04:37.200Z]`,
			},
		},
		{
			name: "zap error directly followed by a panic",
			text: "2026-09-22T10:04:37.123Z\terror\trequest failed\t{}\n" + goCrashText,
			want: []string{
				`message/go error handled "request failed" @-(0) [2026-09-22T10:04:37.123Z]`,
				// The logger line just before the panic dates it.
				`gopanic/go fatal unhandled panic: boom @main.main@main.go:11(1) [2026-09-22T10:04:37.123Z]`,
			},
		},
		{
			name: "winston json error line then a js error with frames",
			text: `{"level":"error","message":"payment declined","timestamp":"2026-09-22T10:00:00.000Z"}` + "\n" + jsTypeErrorText,
			want: []string{`js/ error handled TypeError: cannot read x @handler@h.js:3(2)`},
		},
		{
			name: "cobra-style error line then a go panic",
			text: "Error: config file not found\n" + goCrashText,
			want: []string{`gopanic/go fatal unhandled panic: boom @main.main@main.go:11(1)`},
		},
		{
			name: "js message, plain lines, then traceback",
			text: "Error: plugin load failed\nloading plugins\nplugin a ok\n" + pyTraceText,
			want: []string{`python/python fatal unhandled ValueError: bad @run@main.py:2(2)`},
		},
		{
			name: "python traceback directly after a js error",
			text: jsTypeErrorText + pyTraceText,
			want: []string{
				`js/ error handled TypeError: cannot read x @handler@h.js:3(2)`,
				`python/python fatal unhandled ValueError: bad @run@main.py:2(2)`,
			},
		},
		{
			name: "go panic directly after a python traceback",
			text: pyTraceText + goCrashText,
			want: []string{
				`python/python fatal unhandled ValueError: bad @run@main.py:2(2)`,
				`gopanic/go fatal unhandled panic: boom @main.main@main.go:11(1)`,
			},
		},
		{
			name: "ruby crash header directly after a go panic",
			text: goCrashText + "app.rb:3:in 'Object#run': boom (RuntimeError)\n\tfrom app.rb:9:in '<main>'\n",
			want: []string{
				`gopanic/go fatal unhandled panic: boom @main.main@main.go:11(1)`,
				`ruby/ruby fatal unhandled RuntimeError: boom @Object#run@app.rb:3(2)`,
			},
		},
		{
			name: "deno uncaught header after a handled js error",
			text: jsTypeErrorText + "error: Uncaught (in promise) Error: late\n    at f (file:///repo/app/a.js:1:1)\n",
			want: []string{
				`js/ error handled TypeError: cannot read x @handler@h.js:3(2)`,
				`js/deno fatal unhandled Error: late @f@a.js:1(1)`,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertSummaries(t, Detect(tc.text), tc.want)
		})
	}
}

// R1-20: a zap error line followed by thousands of plain lines is one short
// message event, not a 400-line block.
func TestZapEntryDoesNotAbsorbFollowingLines(t *testing.T) {
	var b strings.Builder
	b.WriteString("2026-09-22T10:04:37.123Z\terror\trequest failed\t{}\n")
	for i := 0; i < 3000; i++ {
		b.WriteString("plain output line\n")
	}
	exs := Detect(b.String())
	if len(exs) != 1 || exs[0].LineStart != 1 || exs[0].LineEnd != 1 {
		t.Fatalf("got %s, want one message event on line 1", strings.Join(summarizeAll(exs), " | "))
	}
}

// R0-16: a line from another stream interleaved between a traceback's frames
// and its closing line does not split the exception.
func TestPythonTracebackToleratesInterleavedLine(t *testing.T) {
	text := "Traceback (most recent call last):\n" +
		`  File "/repo/app/worker.py", line 7, in handle` + "\n" +
		"    parse(payload)\n" +
		"[worker-2] heartbeat ok\n" +
		"ValueError: bad payload\n"
	assertSummaries(t, Detect(text), []string{
		`python/python error handled ValueError: bad payload @handle@worker.py:7(1)`,
	})
}

// R0-15/R1-39: CRLF input parses exactly like LF input.
func TestCRLFInputMatchesLF(t *testing.T) {
	for _, fixture := range []string{
		"dogfood/python.stderr.txt",
		"dogfood/go-crash.stderr.txt",
		"dogfood/node-inspect.stderr.txt",
		"dogfood/ruby.stderr.txt",
		"real/node/uncaught.txt",
		"real/go/zap-dev.txt",
	} {
		t.Run(fixture, func(t *testing.T) {
			lf := readFixture(t, fixture)
			crlf := strings.ReplaceAll(lf, "\n", "\r\n")
			want := summarizeAll(detectIn(lf, testZone))
			if len(want) == 0 {
				t.Fatal("fixture produced no events")
			}
			assertSummaries(t, detectIn(crlf, testZone), want)
		})
	}
}

// Two runtimes' output interleaved block by block, as `monitor run --scan
// both` sees a supervisor's merged streams.
func TestInterleavedRuntimes(t *testing.T) {
	text := readFixture(t, "real/python/asctime.txt") +
		readFixture(t, "real/go/zap-dev.txt") +
		readFixture(t, "real/node/handled-cause.txt") +
		readFixture(t, "real/ruby/logger.txt") +
		readFixture(t, "real/go/nil.txt")
	assertSummaries(t, detectIn(text, testZone), []string{
		`python/python error handled ValueError: invalid literal for int() with base 10: 'x2' @parse@scenarios.py:8(2) [2026-09-23T04:35:39.909Z]`,
		`zap/go error handled "request failed" @example.com/app/internal/svc.(*Store).Load@svc.go:20(5) <= "db timeout" @example.com/app/internal/svc.fetch@svc.go:24(6) [2026-09-23T04:30:26.309Z]`,
		`js/node error handled Error: outer failure @outer@scenarios.mjs:19(3) <= Error: middle failure @middle@scenarios.mjs:12(3) <= Error: root failure @root@scenarios.mjs:6(7)`,
		`ruby/ruby error handled NoMethodError: undefined method 'baz' for nil @Foo#bar@scenarios.rb:5(3) [2026-09-23T04:35:40.285Z]`,
		`message/ruby error handled "plain error without backtrace" @-(0) [2026-09-23T04:35:40.285Z]`,
		`message/ruby warning handled "disk almost full" @-(0) [2026-09-23T04:35:40.285Z]`,
		`gopanic/go fatal unhandled panic: runtime error: invalid memory address or nil pointer dereference @example.com/app/internal/svc.(*Server).Handle@svc.go:13(2) [2026-09-23T04:35:40.285Z]`,
	})
}

// R0-11/R1-38: shapes that only resemble an exception yield nothing.
func TestLookalikesProduceNoEvents(t *testing.T) {
	cases := map[string]string{
		"table rows":           "1 | alice | admin\n2 | bob | user\n3 | carol | user\n",
		"lone exception word":  "Exception\nhandler registered\n",
		"lone error word":      "Error\n",
		"cobra error":          "Error: unknown flag: --verbose\nUsage:\n  app [flags]\n",
		"type-looking message": "ValueError: bad input (retrying)\nretry 1 ok\n",
		"rustc diagnostic": "error[E0308]: mismatched types\n --> src/main.rs:2:18\n  |\n" +
			"2 |     let x: i32 = \"a\";\n  |            ---   ^^^ expected `i32`, found `&str`\n" +
			"  |            |\n  |            expected due to this\n\nerror: aborting due to 1 previous error\n",
		"python warning":   "/repo/app/x.py:3: DeprecationWarning: foo is deprecated\n  import foo\n",
		"deno-like error":  "error: could not compile `app` (bin \"app\") due to 1 previous error\n",
		"winston json":     `{"level":"error","message":"payment declined","timestamp":"2026-09-22T10:00:00.000Z"}` + "\n",
		"pino json":        `{"level":50,"time":1790137826333,"pid":1,"hostname":"h","msg":"payment declined"}` + "\n",
		"structlog json":   `{"event": "payment declined", "level": "error", "timestamp": "2026-09-22T10:00:00Z"}` + "\n",
		"node code intro":  "/repo/app/a.js:12\nnot a stack\n",
		"caret after text": "ok\n    ^\n",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			if exs := Detect(text); len(exs) != 0 {
				t.Errorf("got %d events, want 0: %s", len(exs), strings.Join(summarizeAll(exs), " | "))
			}
		})
	}
}
