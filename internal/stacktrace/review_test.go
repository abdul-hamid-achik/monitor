package stacktrace

import (
	"testing"
	"time"
)

// Python, Ruby, logger-format, timestamp and in-app regressions from the
// adversarial review of this rework. The fixtures are real Python 3.14 and
// Ruby 3.4 output captured by the reviewer (paths rewritten); the inline
// inputs are modeled on real formats.
var reviewCases = []struct {
	fixture string
	want    []string
}{
	// A class defined in a function; an ExceptionGroup as the cause of a
	// chain.
	{"real/python/local-class.txt", []string{
		`python/python fatal unhandled make_local_exc.<locals>.LocalErr: from a local class @make_local_exc@app.py:14(2)`,
	}},
	{"real/python/group-in-chain.txt", []string{
		`python/python fatal unhandled RuntimeError: outer @<module>@app.py:79(1) <= ExceptionGroup: many (2 sub-exceptions) @<module>@app.py:77(1)`,
	}},
	// Ruby "#{e.class}: #{e.message}" before a backtrace, printed or
	// logged.
	{"real/ruby/class-message-backtrace.txt", []string{
		`ruby/ruby error handled ArgumentError: bad config @Object#load_cfg@app.rb:9(2)`,
	}},
	{"real/ruby/logger-class-message.txt", []string{
		`ruby/ruby error handled ArgumentError: bad config @Object#load_cfg@app.rb:9(2) [2026-09-23T05:22:15.293Z]`,
	}},
}

func TestReviewRegressions(t *testing.T) {
	for _, tc := range reviewCases {
		t.Run(tc.fixture, func(t *testing.T) {
			assertSummaries(t, detectIn(readFixture(t, tc.fixture), testZone), tc.want)
		})
	}
}

func TestReviewInline(t *testing.T) {
	cases := []struct {
		name, text string
		want       []string
	}{
		{
			// A logger header carrying "msg: Type: x", and a logger
			// line followed by err.stack on the next line.
			name: "tslog header with prefixed type",
			text: "2026-09-22T10:04:37.123Z ERROR [api] request failed: TypeError: Cannot read properties of undefined (reading 'id')\n" +
				"    at handler (/repo/app/src/api.js:10:5)\n" +
				"2026-09-22T10:04:38.000Z INFO [api] GET /health 200\n" +
				"2026-09-22T10:04:39.123Z ERROR [api] request failed\n" +
				"TypeError: Cannot read properties of undefined (reading 'id')\n" +
				"    at handler (/repo/app/src/api.js:10:5)\n",
			want: []string{
				`tslog/node error handled TypeError: Cannot read properties of undefined (reading 'id') @handler@api.js:10(1) [2026-09-22T10:04:37.123Z]`,
				`tslog/node error handled TypeError: Cannot read properties of undefined (reading 'id') @handler@api.js:10(1) [2026-09-22T10:04:39.123Z]`,
			},
		},
		{
			// A space before the zone keeps the offset (gunicorn).
			name: "gunicorn zoned timestamp",
			text: "[2026-09-22 10:04:37 +0000] [4242] [ERROR] Error handling request /boom\n" +
				"Traceback (most recent call last):\n" +
				"  File \"/repo/app/app/views.py\", line 12, in boom\n    return req[\"user\"][\"id\"]\n" +
				"KeyError: 'user'\n",
			want: []string{`python/python error handled KeyError: 'user' @boom@views.py:12(1) [2026-09-22T10:04:37.000Z]`},
		},
		{
			// A logged traceback takes its logger's level.
			name: "python logging levels",
			text: "WARNING:root:cache miss, retrying\nTraceback (most recent call last):\n" +
				"  File \"/repo/app/app.py\", line 5, in get\n    return cache[key]\nKeyError: 'k'\n" +
				"CRITICAL:root:cannot start\nTraceback (most recent call last):\n" +
				"  File \"/repo/app/app.py\", line 9, in boot\n    connect()\nConnectionRefusedError: [Errno 61] Connection refused\n",
			want: []string{
				`python/python warning handled KeyError: 'k' @get@app.py:5(1)`,
				`python/python fatal handled ConnectionRefusedError: [Errno 61] Connection refused @boot@app.py:9(1)`,
			},
		},
		{
			// zap Named logger without a caller column.
			name: "zap named logger without caller",
			text: "2026-09-22T10:04:37.123Z\terror\tapi\trequest failed\t{\"status\": 500}\n" +
				"2026-09-22T10:04:38.123Z\tERROR\tbilling\tcharge declined\n",
			want: []string{
				`message/go error handled "request failed" @-(0) [2026-09-22T10:04:37.123Z]`,
				`message/go error handled "charge declined" @-(0) [2026-09-22T10:04:38.123Z]`,
			},
		},
		{
			// A zap entry with a generic pkg/errors frame.
			name: "zap with generic frame",
			text: "2026-09-22T10:04:37.123Z\terror\trequest failed\t{}\nboom\nmain.Map[...]\n\t/repo/main.go:13\nmain.main\n\t/repo/main.go:17\n",
			want: []string{`zap/go error handled "request failed" @main.Map[...]@main.go:13(2) [2026-09-22T10:04:37.123Z]`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertSummaries(t, detectIn(tc.text, time.UTC), tc.want)
		})
	}
}

// "evalmachine.<anonymous>" is a pseudo path, not a file with an
// extension; webpack dev-server paths of app code are in-app.
func TestReviewInApp(t *testing.T) {
	cases := map[string]bool{
		"evalmachine.<anonymous>":                            false,
		"webpack-internal:///(rsc)/./app/page.tsx":           true,
		"webpack:///./src/index.js":                          true,
		"webpack-internal:///(ssr)/./node_modules/next/x.js": false,
	}
	for file, want := range cases {
		if got := InApp(Frame{Filename: file}, "/repo/app"); got != want {
			t.Errorf("InApp(%q) = %v, want %v", file, got, want)
		}
	}
}
