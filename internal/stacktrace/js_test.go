package stacktrace

import "testing"

func TestParseJSFrameLineVariants(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		wantFunc string
		wantFile string
		wantLine int
		wantCol  int
	}{
		{
			name:     "plain",
			line:     "    at flakyParse (/repo/examples/polyglot/js/workload.js:31:11)",
			wantFunc: "flakyParse", wantFile: "/repo/examples/polyglot/js/workload.js", wantLine: 31, wantCol: 11,
		},
		{
			name:     "async",
			line:     "    at async fetchThing (/repo/app/net.js:12:3)",
			wantFunc: "async fetchThing", wantFile: "/repo/app/net.js", wantLine: 12, wantCol: 3,
		},
		{
			name:     "new",
			line:     "    at new Client (/repo/app/client.js:5:9)",
			wantFunc: "new Client", wantFile: "/repo/app/client.js", wantLine: 5, wantCol: 9,
		},
		{
			name:     "bare location, no function",
			line:     "    at /repo/app/index.js:1:1",
			wantFunc: "", wantFile: "/repo/app/index.js", wantLine: 1, wantCol: 1,
		},
		{
			name:     "file url",
			line:     "    at flakyParse (file:///repo/examples/polyglot/js/workload.js:31:11)",
			wantFunc: "flakyParse", wantFile: "/repo/examples/polyglot/js/workload.js", wantLine: 31, wantCol: 11,
		},
		{
			name:     "eval anonymous",
			line:     "    at eval (eval at <anonymous> (/repo/app/index.js:10:5), <anonymous>:3:9)",
			wantFunc: "eval", wantFile: "<anonymous>", wantLine: 3, wantCol: 9,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, ok := parseJSFrameLine(tc.line)
			if !ok {
				t.Fatalf("parseJSFrameLine(%q) did not match", tc.line)
			}
			if f.Function != tc.wantFunc {
				t.Errorf("Function = %q, want %q", f.Function, tc.wantFunc)
			}
			if f.Filename != tc.wantFile {
				t.Errorf("Filename = %q, want %q", f.Filename, tc.wantFile)
			}
			if f.Lineno != tc.wantLine || f.Colno != tc.wantCol {
				t.Errorf("Lineno:Colno = %d:%d, want %d:%d", f.Lineno, f.Colno, tc.wantLine, tc.wantCol)
			}
		})
	}
}

func TestParseJSNotAFrameLine(t *testing.T) {
	if _, ok := parseJSFrameLine("Error: boom"); ok {
		t.Error("parseJSFrameLine matched a non-frame line")
	}
}
