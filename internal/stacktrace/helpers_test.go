package stacktrace

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testZone is the fixed "local" zone the golden tests read zone-less
// timestamps (Python asctime, Ruby Logger) in, so they do not depend on the
// machine's TZ. The real captures were recorded at UTC-6.
var testZone = time.FixedZone("UTC-6", -6*3600)

func readFixture(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read fixture %s: %v", rel, err)
	}
	return string(b)
}

// detectIn is Detect with zone-less timestamps read in loc.
func detectIn(text string, loc *time.Location) []*Exception {
	j := NewJoiner()
	j.Location = loc
	return detectWith(j, text)
}

// frameRef renders a frame as "func@file:line" (file is the base name).
func frameRef(f Frame) string {
	return fmt.Sprintf("%s@%s:%d", f.Function, path.Base(strings.ReplaceAll(f.Filename, `\`, "/")), f.Lineno)
}

func crashRef(ex Exception) string {
	if len(ex.Frames) == 0 {
		return "-"
	}
	return frameRef(ex.Frames[len(ex.Frames)-1])
}

func typeValue(ex Exception) string {
	switch {
	case ex.Type == "":
		return fmt.Sprintf("%q", ex.Value)
	case ex.Value == "":
		return ex.Type
	}
	return ex.Type + ": " + ex.Value
}

// summarize renders the parts of an Exception the golden tables assert on:
//
//	parser/runtime level handled Type: Value @crash(nframes) <= cause... [ts]
func summarize(ex *Exception) string {
	h := "?"
	if ex.Handled != nil {
		h = map[bool]string{true: "handled", false: "unhandled"}[*ex.Handled]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s/%s %s %s %s @%s(%d)", ex.Parser, ex.Runtime, ex.Level, h, typeValue(*ex), crashRef(*ex), len(ex.Frames))
	for _, c := range ex.Chained {
		fmt.Fprintf(&b, " <= %s @%s(%d)", typeValue(c), crashRef(c), len(c.Frames))
	}
	if !ex.ObservedAt.IsZero() {
		fmt.Fprintf(&b, " [%s]", ex.ObservedAt.UTC().Format("2006-01-02T15:04:05.000Z"))
	}
	return b.String()
}

func summarizeAll(exs []*Exception) []string {
	out := make([]string, 0, len(exs))
	for _, ex := range exs {
		out = append(out, summarize(ex))
	}
	return out
}

// assertSummaries compares summaries line by line with a readable diff.
func assertSummaries(t *testing.T, got []*Exception, want []string) {
	t.Helper()
	g := summarizeAll(got)
	if strings.Join(g, "\n") == strings.Join(want, "\n") {
		return
	}
	var b strings.Builder
	n := max(len(g), len(want))
	for i := 0; i < n; i++ {
		var gl, wl string
		if i < len(g) {
			gl = g[i]
		}
		if i < len(want) {
			wl = want[i]
		}
		mark := "  "
		if gl != wl {
			mark = "!!"
		}
		fmt.Fprintf(&b, "%s #%d\n     got:  %s\n     want: %s\n", mark, i, gl, wl)
	}
	t.Errorf("summaries differ (got %d, want %d):\n%s", len(g), len(want), b.String())
}
