package stacktrace

import (
	"strings"
	"testing"
	"time"
)

func TestJoinerIdleFlushesOnTick(t *testing.T) {
	j := NewJoiner()
	base := time.Unix(0, 0)

	if b := j.Feed("Error: boom", base); !b.empty() {
		t.Fatalf("Feed on empty joiner returned non-empty block: %+v", b)
	}
	if b := j.Feed("    at fn (/repo/a.js:1:1)", base.Add(10*time.Millisecond)); !b.empty() {
		t.Fatalf("continuation line flushed early: %+v", b)
	}
	// Idle not yet exceeded.
	if b := j.Tick(base.Add(20 * time.Millisecond)); !b.empty() {
		t.Fatalf("Tick flushed before idle timeout: %+v", b)
	}
	// Idle exceeded (300ms default) since the last Feed.
	b := j.Tick(base.Add(10*time.Millisecond + DefaultIdle + time.Millisecond))
	if b.empty() {
		t.Fatal("Tick did not flush after idle timeout")
	}
	if len(b.Lines) != 2 {
		t.Errorf("flushed block has %d lines, want 2: %+v", len(b.Lines), b.Lines)
	}
	// A second Tick with nothing buffered is a no-op.
	if b := j.Tick(base.Add(time.Hour)); !b.empty() {
		t.Fatalf("Tick on empty joiner returned non-empty: %+v", b)
	}
}

func TestJoinerNewBlockStartsWithoutWaiting(t *testing.T) {
	// Feeding two occurrences back-to-back with no idle time between them
	// (as a batch file replay does) must still split them by structure.
	j := NewJoiner()
	now := time.Unix(0, 0)
	var got []Block
	feed := func(l string) {
		if b := j.Feed(l, now); !b.empty() {
			got = append(got, b)
		}
	}
	feed("Error: flakyParse: boom")
	feed("    at flakyParse (/repo/a.js:1:1)")
	feed("[node] caught in tickErrors: flakyParse: boom") // noise; ends block 1
	feed("Error: flakyParse: boom")
	feed("    at flakyParse (/repo/a.js:1:1)")
	if b := j.Flush(); !b.empty() {
		got = append(got, b)
	}
	if len(got) != 2 {
		t.Fatalf("got %d blocks, want 2: %+v", len(got), got)
	}
	for i, b := range got {
		if len(b.Lines) != 2 {
			t.Errorf("block %d has %d lines, want 2: %+v", i, len(b.Lines), b.Lines)
		}
	}
}

func TestJoinerDropsUnrecognizedLines(t *testing.T) {
	j := NewJoiner()
	now := time.Unix(0, 0)
	for _, l := range []string{"just a normal log line", "another one", "GET /health 200 3ms"} {
		if b := j.Feed(l, now); !b.empty() {
			t.Fatalf("clean line unexpectedly produced a block: %+v", b)
		}
	}
	if b := j.Flush(); !b.empty() {
		t.Fatalf("Flush on all-clean input produced a block: %+v", b)
	}
}

func TestJoinerCapsLines(t *testing.T) {
	j := NewJoiner()
	j.MaxLines = 3
	now := time.Unix(0, 0)
	var blocks []Block
	feed := func(l string) {
		if b := j.Feed(l, now); !b.empty() {
			blocks = append(blocks, b)
		}
	}
	feed("Error: boom")
	feed("    at a (/repo/a.js:1:1)")
	feed("    at b (/repo/a.js:2:1)")
	// This 4th line would be the 4th in the buffer, exceeding MaxLines=3;
	// the block must be force-completed instead of growing past the cap.
	feed("    at c (/repo/a.js:3:1)")
	if b := j.Flush(); !b.empty() {
		blocks = append(blocks, b)
	}
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1 (the capped one; the 4th continuation line has nowhere to start a new block and is dropped): %+v", len(blocks), blocks)
	}
	if len(blocks[0].Lines) != 3 {
		t.Errorf("capped block has %d lines, want 3", len(blocks[0].Lines))
	}
}

func TestJoinerCapsBytes(t *testing.T) {
	j := NewJoiner()
	j.MaxBytes = 40
	now := time.Unix(0, 0)
	var blocks []Block
	feed := func(l string) {
		if b := j.Feed(l, now); !b.empty() {
			blocks = append(blocks, b)
		}
	}
	feed("Error: boom") // 11 bytes + 1 = 12
	feed(strings.Repeat("x", 50))
	if b := j.Flush(); !b.empty() {
		blocks = append(blocks, b)
	}
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1: %+v", len(blocks), blocks)
	}
	if len(blocks[0].Lines) != 1 {
		t.Errorf("capped block has %d lines, want 1 (the oversized line forced an early flush)", len(blocks[0].Lines))
	}
}

func TestBlockText(t *testing.T) {
	b := Block{Lines: []string{"a", "b", "c"}}
	if got, want := b.Text(), "a\nb\nc"; got != want {
		t.Errorf("Text() = %q, want %q", got, want)
	}
}

func TestJoinerLineNumbers(t *testing.T) {
	j := NewJoiner()
	now := time.Unix(0, 0)
	j.Feed("noise", now)
	j.Feed("noise 2", now)
	j.Feed("Error: boom", now)
	j.Feed("    at a (/repo/a.js:1:1)", now)
	b := j.Flush()
	if b.LineStart != 3 || b.LineEnd != 4 {
		t.Errorf("block line range = [%d,%d], want [3,4]", b.LineStart, b.LineEnd)
	}
}
