package stacktrace

import (
	"testing"
	"time"
)

func TestLineTimestampsStrip(t *testing.T) {
	for _, tc := range []struct {
		name, line, want string
		at               string // RFC3339Nano UTC, "" for none
	}{
		{"docker nanos Z", "2026-09-26T21:03:56.839182000Z TypeError: boom", "TypeError: boom", "2026-09-26T21:03:56.839182Z"},
		{"offset with colon", "2026-09-26T15:03:56-06:00 Traceback (most recent call last):", "Traceback (most recent call last):", "2026-09-26T21:03:56Z"},
		{"offset without colon", "2026-09-26T21:03:56.5+0000     at f (/app/a.js:1:2)", "    at f (/app/a.js:1:2)", "2026-09-26T21:03:56.5Z"},
		{"no prefix", "TypeError: boom", "TypeError: boom", ""},
		{"date only is not a prefix", "2026-09-26 TypeError: boom", "2026-09-26 TypeError: boom", ""},
		{"timestamp without trailing space is kept", "2026-09-26T21:03:56Z", "2026-09-26T21:03:56Z", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := NewLineTimestamps()
			if got := l.Strip(tc.line); got != tc.want {
				t.Fatalf("Strip = %q, want %q", got, tc.want)
			}
			got := l.At(1)
			if tc.at == "" {
				if !got.IsZero() {
					t.Fatalf("At(1) = %v, want zero", got)
				}
				return
			}
			if want, _ := time.Parse(time.RFC3339Nano, tc.at); !got.Equal(want) {
				t.Fatalf("At(1) = %v, want %v", got.UTC(), want)
			}
		})
	}
}

func TestLineTimestampsAtFallsBackToEarlierLine(t *testing.T) {
	l := NewLineTimestamps()
	l.Strip("2026-09-26T21:03:56Z TypeError: boom")
	l.Strip("    at f (/app/a.js:1:2)") // unstamped continuation
	want, _ := time.Parse(time.RFC3339, "2026-09-26T21:03:56Z")
	if got := l.At(2); !got.Equal(want) {
		t.Fatalf("At(2) = %v, want the previous line's %v", got, want)
	}
}

// A timestamped container log is invisible to every grammar until the
// prefix is stripped; with it stripped, the trace parses and keeps the
// log's own time rather than the replay's.
func TestLineTimestampsMakeTimestampedLogsParse(t *testing.T) {
	log := []string{
		"2026-09-26T21:03:56.839182Z TypeError: Cannot read properties of undefined (reading 'amount')",
		"2026-09-26T21:03:56.839300Z     at applyDiscount (/app/src/cart.js:3:31)",
		"2026-09-26T21:03:56.839300Z     at checkout (/app/src/cart.js:7:17)",
	}
	parse := func(strip bool) []*Exception {
		j := NewJoiner()
		l := NewLineTimestamps()
		var out []*Exception
		collect := func(bs []Block) {
			for _, b := range bs {
				if ex := Parse(b); ex != nil {
					if ex.ObservedAt.IsZero() {
						ex.ObservedAt = l.At(b.LineStart)
					}
					out = append(out, ex)
				}
			}
		}
		base := time.Unix(0, 0)
		for i, line := range log {
			if strip {
				line = l.Strip(line)
			}
			collect(j.Feed(line, base.Add(time.Duration(i)*time.Microsecond)))
		}
		collect(j.Flush())
		return out
	}
	if got := parse(false); len(got) != 0 {
		t.Fatalf("unstripped log parsed %d exceptions; the prefix should hide the trace", len(got))
	}
	got := parse(true)
	if len(got) != 1 || got[0].Type != "TypeError" {
		t.Fatalf("stripped log = %+v, want one TypeError", got)
	}
	want, _ := time.Parse(time.RFC3339Nano, "2026-09-26T21:03:56.839182Z")
	if !got[0].ObservedAt.Equal(want) {
		t.Fatalf("ObservedAt = %v, want the log line's %v", got[0].ObservedAt, want)
	}
}

func TestParsePathMap(t *testing.T) {
	for _, tc := range []struct {
		spec    string
		want    PathMap
		wantErr bool
	}{
		{spec: "/app=/srv/checkout", want: PathMap{From: "/app", To: "/srv/checkout"}},
		{spec: "/app/=/srv/checkout/", want: PathMap{From: "/app", To: "/srv/checkout"}},
		{spec: "app=/srv", wantErr: true},
		{spec: "/app=srv", wantErr: true},
		{spec: "/app", wantErr: true},
		{spec: "=/srv", wantErr: true},
	} {
		got, err := ParsePathMap(tc.spec)
		if (err != nil) != tc.wantErr {
			t.Fatalf("ParsePathMap(%q) err = %v, wantErr %v", tc.spec, err, tc.wantErr)
		}
		if err == nil && got != tc.want {
			t.Fatalf("ParsePathMap(%q) = %+v, want %+v", tc.spec, got, tc.want)
		}
	}
}

func TestApplyPathMapThenGitRootMakesContainerFramesInApp(t *testing.T) {
	ex := &Exception{
		Type: "TypeError",
		Frames: []Frame{
			{Function: "handle", Filename: "/app/src/server.js", AbsPath: "/app/src/server.js"},
			{Function: "applyDiscount", Filename: "/app/src/cart.js", AbsPath: "/app/src/cart.js"},
			{Function: "Module._compile", Filename: "node:internal/modules/cjs/loader"},
			{Function: "elsewhere", Filename: "/application/x.js", AbsPath: "/application/x.js"},
		},
		Chained: []Exception{{Frames: []Frame{{Filename: "/app/src/db.js", AbsPath: "/app/src/db.js"}}}},
	}
	ApplyPathMap(ex, []PathMap{{From: "/app", To: "/repo"}})
	ApplyGitRoot(ex, "/repo")

	crash := ex.Frames[1]
	if !crash.InApp || crash.Filename != "src/cart.js" || crash.AbsPath != "/repo/src/cart.js" {
		t.Fatalf("crash frame = %+v, want in-app src/cart.js under /repo", crash)
	}
	if ex.Frames[2].Filename != "node:internal/modules/cjs/loader" || ex.Frames[2].InApp {
		t.Fatalf("runtime frame changed: %+v", ex.Frames[2])
	}
	// A sibling path that merely shares the prefix string is not under it.
	if ex.Frames[3].AbsPath != "/application/x.js" {
		t.Fatalf("/application was rewritten: %+v", ex.Frames[3])
	}
	if got := ex.Chained[0].Frames[0]; got.Filename != "src/db.js" || !got.InApp {
		t.Fatalf("chained cause frame = %+v, want in-app src/db.js", got)
	}
}
