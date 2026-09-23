package stacktrace

import (
	"strings"
	"testing"
	"time"
)

// feedAll feeds lines at a fixed step and returns every completed block,
// including the final Flush.
func feedAll(j *Joiner, start time.Time, step time.Duration, lines ...string) []Block {
	var out []Block
	for i, l := range lines {
		out = append(out, j.Feed(l, start.Add(time.Duration(i)*step))...)
	}
	return append(out, j.Flush()...)
}

func TestJoinerIdleFlushesOnTick(t *testing.T) {
	j := NewJoiner()
	base := time.Unix(0, 0)
	if bs := j.Feed("Error: boom", base); len(bs) != 0 {
		t.Fatalf("Feed on empty joiner returned blocks: %+v", bs)
	}
	if bs := j.Feed("    at fn (/repo/a.js:1:1)", base.Add(10*time.Millisecond)); len(bs) != 0 {
		t.Fatalf("continuation line flushed early: %+v", bs)
	}
	if bs := j.Tick(base.Add(20 * time.Millisecond)); len(bs) != 0 {
		t.Fatalf("Tick flushed before the idle timeout: %+v", bs)
	}
	bs := j.Tick(base.Add(10*time.Millisecond + DefaultIdle))
	if len(bs) != 1 || len(bs[0].Lines) != 2 {
		t.Fatalf("Tick after idle = %+v, want one 2-line block", bs)
	}
	if bs := j.Tick(base.Add(time.Hour)); len(bs) != 0 {
		t.Fatalf("Tick on an empty joiner returned blocks: %+v", bs)
	}
}

// R1-34: idle must be enforced by Feed itself, so a caller that never Ticks
// (or ticks late) still splits blocks by time.
func TestJoinerIdleEnforcedInFeedWithoutTick(t *testing.T) {
	j := NewJoiner()
	base := time.Unix(0, 0)
	j.Feed("Traceback (most recent call last):", base)
	j.Feed(`  File "/repo/a.py", line 1, in <module>`, base.Add(time.Millisecond))
	// Ten seconds later, an indented line that would otherwise continue
	// the traceback: the idle gap closes the block first.
	bs := j.Feed("    unrelated indented output", base.Add(10*time.Second))
	if len(bs) != 1 || bs[0].LineEnd != 2 {
		t.Fatalf("Feed after idle gap = %+v, want the 2-line traceback closed first", bs)
	}
	if rest := j.Flush(); len(rest) != 0 {
		t.Fatalf("the post-gap line must not have opened or joined a block: %+v", rest)
	}

	// A Tick at 250ms does not flush, and a line at 350ms is still split
	// off because Feed measures idle from the previous line.
	j = NewJoiner()
	j.Feed("Error: boom", base)
	j.Feed("    at a (/repo/a.js:1:1)", base.Add(time.Millisecond))
	if bs := j.Tick(base.Add(250 * time.Millisecond)); len(bs) != 0 {
		t.Fatalf("early Tick flushed: %+v", bs)
	}
	bs = j.Feed("    at b (/repo/a.js:2:1)", base.Add(350*time.Millisecond))
	if len(bs) != 1 || len(bs[0].Lines) != 2 {
		t.Fatalf("line after idle joined the old block: %+v", bs)
	}
}

func TestJoinerNewBlockStartsWithoutWaiting(t *testing.T) {
	bs := feedAll(NewJoiner(), time.Unix(0, 0), 0,
		"Error: flakyParse: boom",
		"    at flakyParse (/repo/a.js:1:1)",
		"[node] caught in tickErrors: flakyParse: boom", // noise; ends block 1
		"Error: flakyParse: boom",
		"    at flakyParse (/repo/a.js:1:1)",
	)
	if len(bs) != 2 || len(bs[0].Lines) != 2 || len(bs[1].Lines) != 2 {
		t.Fatalf("got %+v, want two 2-line blocks", bs)
	}
	if bs[1].Prev != "[node] caught in tickErrors: flakyParse: boom" {
		t.Errorf("second block Prev = %q, want the noise line before it", bs[1].Prev)
	}
	if bs[0].Prev != "" {
		t.Errorf("first block Prev = %q, want empty", bs[0].Prev)
	}
}

func TestJoinerDropsUnrecognizedLines(t *testing.T) {
	bs := feedAll(NewJoiner(), time.Unix(0, 0), 0, "just a normal log line", "another one", "GET /health 200 3ms")
	if len(bs) != 0 {
		t.Fatalf("clean lines produced blocks: %+v", bs)
	}
}

func TestJoinerCapsLines(t *testing.T) {
	j := NewJoiner()
	j.MaxLines = 3
	bs := feedAll(j, time.Unix(0, 0), 0,
		"Error: boom",
		"    at a (/repo/a.js:1:1)",
		"    at b (/repo/a.js:2:1)",
		"    at c (/repo/a.js:3:1)", // 4th line: over the cap
	)
	if len(bs) != 1 || len(bs[0].Lines) != 3 {
		t.Fatalf("got %+v, want one block capped at 3 lines", bs)
	}
}

func TestJoinerCapsBytes(t *testing.T) {
	j := NewJoiner()
	j.MaxBytes = 40
	bs := feedAll(j, time.Unix(0, 0), 0, "Error: boom", "    at "+strings.Repeat("x", 50))
	if len(bs) != 1 || len(bs[0].Lines) != 1 {
		t.Fatalf("got %+v, want the oversized continuation to force an early close", bs)
	}
}

// R1-42: the first line of a block is capped too.
func TestJoinerCapsFirstLine(t *testing.T) {
	j := NewJoiner()
	bs := feedAll(j, time.Unix(0, 0), 0, "Error: "+strings.Repeat("y", 200*1024), "    at f (/repo/a.js:1:1)")
	total := 0
	for _, b := range bs {
		for _, l := range b.Lines {
			total += len(l) + 1
		}
	}
	if total > DefaultMaxBytes {
		t.Fatalf("blocks carry %d bytes, over the %d cap", total, DefaultMaxBytes)
	}
	exs := Detect("Error: " + strings.Repeat("y", 200*1024) + "\n    at f (/repo/a.js:1:1)")
	for _, ex := range exs {
		if len(ex.Value) >= DefaultMaxBytes {
			t.Errorf("Value is %d bytes, want it truncated under %d", len(ex.Value), DefaultMaxBytes)
		}
	}
}

func TestTruncateUTF8KeepsRunesWhole(t *testing.T) {
	s := strings.Repeat("é", 10) // 2 bytes each
	got := truncateUTF8(s, 5)
	if got != "éé" {
		t.Errorf("truncateUTF8 = %q, want %q", got, "éé")
	}
}

// R0-15/R1-39: CR is stripped in Feed, so CRLF text parses like LF text.
func TestJoinerStripsCR(t *testing.T) {
	bs := feedAll(NewJoiner(), time.Unix(0, 0), 0, "panic: boom\r", "\r", "goroutine 1 [running]:\r", "main.main()\r", "\t/repo/main.go:5 +0x1d\r")
	if len(bs) != 1 {
		t.Fatalf("got %d blocks, want 1: %+v", len(bs), bs)
	}
	for _, l := range bs[0].Lines {
		if strings.Contains(l, "\r") {
			t.Fatalf("line kept its CR: %q", l)
		}
	}
}

func TestJoinerStripsANSI(t *testing.T) {
	bs := feedAll(NewJoiner(), time.Unix(0, 0), 0, "\x1b[31mError\x1b[0m: boom", "    at \x1b[1mf\x1b[0m (/repo/a.js:1:1)")
	if len(bs) != 1 || bs[0].Lines[0] != "Error: boom" {
		t.Fatalf("got %+v, want one ANSI-free block", bs)
	}
}

func TestBlockText(t *testing.T) {
	b := Block{Lines: []string{"a", "b", "c"}}
	if got, want := b.Text(), "a\nb\nc"; got != want {
		t.Errorf("Text() = %q, want %q", got, want)
	}
}

func TestJoinerLineNumbers(t *testing.T) {
	bs := feedAll(NewJoiner(), time.Unix(0, 0), 0, "noise", "noise 2", "Error: boom", "    at a (/repo/a.js:1:1)")
	if len(bs) != 1 || bs[0].LineStart != 3 || bs[0].LineEnd != 4 {
		t.Fatalf("got %+v, want one block spanning lines 3-4", bs)
	}
	if bs[0].Prev != "noise 2" || bs[0].Kind != "js" {
		t.Errorf("Prev/Kind = %q/%q, want %q/js", bs[0].Prev, bs[0].Kind, "noise 2")
	}
}

// A tentative line (here: a pkg/errors message line after a zap entry) that
// turns out to start a JS error with frames is re-read, not swallowed.
func TestJoinerReplaysTentativeLines(t *testing.T) {
	bs := feedAll(NewJoiner(), time.Unix(0, 0), 0,
		"2026-09-22T10:04:37.123Z\terror\trequest failed\t{}",
		"TypeError: cannot read x",
		"    at handler (/repo/app/h.js:3:9)",
	)
	if len(bs) != 2 || bs[0].Kind != "zap-console" || len(bs[0].Lines) != 1 || bs[1].Kind != "js" || len(bs[1].Lines) != 2 {
		t.Fatalf("got %+v, want a 1-line zap block then a 2-line js block", bs)
	}
}

func TestJoinerLocationStampedOnBlocks(t *testing.T) {
	j := NewJoiner()
	j.Location = testZone
	bs := feedAll(j, time.Unix(0, 0), 0, "Error: boom", "    at a (/repo/a.js:1:1)")
	if len(bs) != 1 || bs[0].Location != testZone {
		t.Fatalf("got %+v, want the joiner's Location on the block", bs)
	}
}

func TestParseEmptyAndUnknownBlocks(t *testing.T) {
	if Parse(Block{}) != nil {
		t.Error("Parse(empty) != nil")
	}
	if Parse(Block{Kind: "no-such-kind", Lines: []string{"x"}}) != nil {
		t.Error("Parse(unknown kind) != nil")
	}
	if Parse(Block{Lines: []string{"plain text"}}) != nil {
		t.Error("Parse(hand-built block with no start line) != nil")
	}
	// A hand-built block with no Kind is re-matched from its first line.
	ex := Parse(Block{Lines: []string{"panic: boom"}})
	if ex == nil || ex.Parser != "gopanic" {
		t.Errorf("Parse(hand-built panic block) = %+v", ex)
	}
}
