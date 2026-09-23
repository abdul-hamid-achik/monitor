package stacktrace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// FuzzDetect checks that no input makes the joiner or a parser panic, loop,
// or emit a block over the caps. `go test` runs the seed corpus (every
// committed fixture); `go test -fuzz FuzzDetect` explores further.
func FuzzDetect(f *testing.F) {
	files, _ := filepath.Glob(filepath.Join("testdata", "*", "*.txt"))
	real, _ := filepath.Glob(filepath.Join("testdata", "real", "*", "*.txt"))
	for _, p := range append(files, real...) {
		if b, err := os.ReadFile(p); err == nil {
			f.Add(string(b))
		}
	}
	f.Add("panic: \n\tpanic: \n[signal \ngoroutine 1 [running]:\n()\n\t:1\n")
	f.Add("Error: x\n    at (\n    at )\n    at a ( {\n  [cause]: \n}")
	f.Add("1 |\n^\nerror: \n      at x\n\n2 |\n^\nx\n\nBun v1.0")
	f.Fuzz(func(t *testing.T, text string) {
		j := NewJoiner()
		j.MaxLines, j.MaxBytes = 50, 2048
		var blocks []Block
		base := time.Unix(0, 0)
		for i, l := range strings.Split(text, "\n") {
			blocks = append(blocks, j.Feed(l, base.Add(time.Duration(i)*time.Millisecond))...)
		}
		blocks = append(blocks, j.Flush()...)
		for _, b := range blocks {
			n := 0
			for _, l := range b.Lines {
				n += len(l) + 1
			}
			if len(b.Lines) > 50 || n > 2048 {
				t.Fatalf("block over the caps: %d lines, %d bytes", len(b.Lines), n)
			}
			if ex := Parse(b); ex != nil {
				ApplyGitRoot(ex, "/repo")
			}
		}
	})
}
