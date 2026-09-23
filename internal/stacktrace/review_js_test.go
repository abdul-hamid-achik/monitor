package stacktrace

import (
	"testing"
	"time"
)

// JS regressions from the adversarial review of this rework. The fixtures
// are real Node 26 / Deno 2.9 / Bun 1.4 output captured by the reviewer
// (paths rewritten); the inline inputs are modeled on real formats.
var reviewJSCases = []struct {
	fixture string
	want    []string
}{
	// A process warning or a lone "Error: x" header must not hold a later
	// prefixed error as its message continuation.
	{"real/node/depwarn-then-prefixed.txt", []string{
		`js/node error handled TypeError: Cannot read properties of undefined (reading 'id') @handler@app.js:8(4)`,
	}},
	{"real/node/lone-then-prefixed.txt", []string{
		`js/node error handled TypeError: Cannot read properties of undefined (reading 'id') @handler@app.js:8(9)`,
	}},
	// Messages longer than 16 lines keep their stack; a message that opens
	// with a lone "[" is read as the whole JSON report.
	{"real/node/longmsg-handled.txt", []string{
		`js/node error handled Error: [ { "code": "invalid_type", "expected": "string", "received": "undefined", "path": [ "field0" ], "message": "Required" }, { "code": "invalid_type", "expected": "string", "received": "undefined", "path": [ "field1" ], "message": "Required" }, { "code": "invalid_type", "expected": "string", "received": "undefined", "path": [ "field2" ], "message": "Required" } ] @Object.<anonymous>@app.js:29(8)`,
	}},
	{"real/node/longmsg-uncaught.txt", []string{
		`js/node fatal unhandled Error: [ { "code": "invalid_type", "expected": "string", "received": "undefined", "path": [ "field0" ], "message": "Required" }, { "code": "invalid_type", "expected": "string", "received": "undefined", "path": [ "field1" ], "message": "Required" }, { "code": "invalid_type", "expected": "string", "received": "undefined", "path": [ "field2" ], "message": "Required" } ] @Object.<anonymous>@app.js:37(8)`,
	}},
	{"real/bun/longmsg-uncaught.txt", []string{
		`js/bun fatal unhandled ZodError: [ { "code": "invalid_type", "expected": "string", "received": "undefined", "path": [ "field0" ], "message": "Required" }, { "code": "invalid_type", "expected": "string", "received": "undefined", "path": [ "field1" ], "message": "Required" }, { "code": "invalid_type", "expected": "string", "received": "undefined", "path": [ "field2" ], "message": "Required" } ] @@app.js:37(1)`,
	}},
	{"real/deno/longmsg-uncaught.txt", []string{
		`js/deno fatal unhandled ZodError: [ { "code": "invalid_type", "expected": "string", "received": "undefined", "path": [ "field0" ], "message": "Required" }, { "code": "invalid_type", "expected": "string", "received": "undefined", "path": [ "field1" ], "message": "Required" }, { "code": "invalid_type", "expected": "string", "received": "undefined", "path": [ "field2" ], "message": "Required" } ] @@app.js:37(1)`,
	}},
	{"real/node/assertdiff-handled.txt", []string{
		`js/node error handled AssertionError: Expected values to be strictly deep-equal: @Object.<anonymous>@assertdiff.js:7(8)`,
	}},
	{"real/node/assertdiff-uncaught.txt", []string{
		`js/node fatal unhandled AssertionError: Expected values to be strictly deep-equal: @Object.<anonymous>@assertdiff.js:10(8)`,
	}},
	// "error: TypeError: x" is a printed report (deno test, a logger's
	// "${level}: ${stack}"): typed from the header and handled.
	{"real/node/level-prefixed-stack.txt", []string{
		`js/node error handled TypeError: Cannot read properties of undefined (reading 'id') @handler@app.js:8(9)`,
	}},
	{"real/deno/test-failure.txt", []string{
		`js/ error handled Error: expected 3 @@deno_test.ts:2(1)`,
		`js/ error handled TypeError: Cannot read properties of undefined (reading 'x') @@deno_test.ts:6(1)`,
	}},
	// AggregateError, which has no stack of its own.
	{"real/node/aggregate-handled.txt", []string{
		`js/ error handled AggregateError: All promises were rejected @-(0)`,
	}},
	{"real/node/aggregate-uncaught.txt", []string{
		`js/node fatal unhandled AggregateError: All promises were rejected @-(0)`,
	}},
	{"real/bun/aggregate-uncaught.txt", []string{
		`js/bun fatal unhandled Error: a @@app.js:57(1) <= TypeError: b @@app.js:57(1)`,
	}},
}

func TestReviewJSRegressions(t *testing.T) {
	for _, tc := range reviewJSCases {
		t.Run(tc.fixture, func(t *testing.T) {
			assertSummaries(t, detectIn(readFixture(t, tc.fixture), testZone), tc.want)
		})
	}
}

func TestReviewJSInline(t *testing.T) {
	cases := []struct {
		name, text string
		want       []string
	}{
		{
			// Table rows are not a Bun code frame (no caret) and must
			// not swallow the prefixed error after them.
			name: "psql table then prefixed error",
			text: " id | name  | role\n----+-------+-------\n  1 | alice | admin\n  2 | bob   | user\n(2 rows)\n\n" +
				"query failed: TypeError: Cannot read properties of undefined (reading 'rows')\n" +
				"    at runQuery (/repo/app/src/db.js:22:17)\n    at async main (/repo/app/src/index.js:8:3)\n",
			want: []string{`js/ error handled TypeError: Cannot read properties of undefined (reading 'rows') @runQuery@db.js:22(2)`},
		},
		{
			name: "numbered list then error at column 0",
			text: "Migrations applied:\n  1 | 20260901_init\n  2 | 20260915_users\n" +
				"TypeError: Cannot read properties of undefined (reading 'rows')\n" +
				"    at runQuery (/repo/app/src/db.js:22:17)\n",
			want: []string{`js/ error handled TypeError: Cannot read properties of undefined (reading 'rows') @runQuery@db.js:22(1)`},
		},
		{
			// Next.js 15 dev output: an error, its source frame, then a
			// second error.
			name: "next.js 15 two errors",
			text: " ⨯ Error: boom\n    at Page (app/page.tsx:9:9)\n" +
				"   7 | export default function Page() {\n   8 |   const user = null;\n>  9 |   throw new Error('boom');\n" +
				"     |         ^\n  10 | }\n  digest: '2451434040'\n}\n GET / 500 in 1712ms\n" +
				" ⨯ TypeError: Cannot read properties of null (reading 'name')\n" +
				"    at Profile (/repo/app/app/profile/page.tsx:5:21)\n" +
				"    at renderWithHooks (/repo/app/node_modules/next/dist/compiled/react-dom/cjs/react-dom-server.edge.development.js:4209:18)\n" +
				" GET /profile 500 in 90ms\n",
			want: []string{
				`js/ error handled Error: boom @Page@page.tsx:9(1)`,
				`js/ error handled TypeError: Cannot read properties of null (reading 'name') @Profile@page.tsx:5(2)`,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertSummaries(t, detectIn(tc.text, time.UTC), tc.want)
		})
	}
}
