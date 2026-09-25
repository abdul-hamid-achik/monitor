package stacktrace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func allFixtures(tb testing.TB) string {
	tb.Helper()
	var b strings.Builder
	for _, pattern := range []string{"testdata/*/*.txt", "testdata/real/*/*.txt"} {
		files, _ := filepath.Glob(pattern)
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				tb.Fatal(err)
			}
			b.Write(data)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func BenchmarkDetectFixtures(b *testing.B) {
	text := allFixtures(b)
	b.SetBytes(int64(len(text)))
	for b.Loop() {
		Detect(text)
	}
}

// TestDetectIsLinearOnPathologicalInput guards the tentative-line replay:
// shapes that keep opening and abandoning blocks must stay linear.
func TestDetectIsLinearOnPathologicalInput(t *testing.T) {
	shapes := map[string]string{
		"headers without frames": "TypeError: x\n",
		"message then noise":     "Error: x\nnoise a\nnoise b\nnoise c\n",
		"bun table rows":         "12 | a | b\n",
		"zap messages":           "2026-09-22T10:04:37.123Z\terror\tboom\t{}\nmsg line\nmain.f\n",
		"python frames forever":  "  File \"/a.py\", line 1, in f\n",
		"indented lines":         "    at\n",
		"blank lines":            "\n",
		"node intro":             "/repo/a.js:1\nsrc\n",
		"ruby snippet":           "a.rb:1:in 'x': y (E)\n\n  z\n",
	}
	for name, unit := range shapes {
		t.Run(name, func(t *testing.T) {
			small := strings.Repeat(unit, 500)
			big := strings.Repeat(unit, 5000)
			start := time.Now()
			Detect(small)
			ts := time.Since(start)
			start = time.Now()
			Detect(big)
			tb := time.Since(start)
			// 10x the input must not cost anywhere near 100x the time.
			if ts > time.Millisecond && tb > 50*ts {
				t.Errorf("10x input took %v vs %v: super-linear", tb, ts)
			}
			if tb > 10*time.Second {
				t.Errorf("5000 units took %v", tb)
			}
		})
	}
}
