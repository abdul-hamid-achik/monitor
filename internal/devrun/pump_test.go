package devrun

import (
	"bytes"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func linesOf(sls []streamLine) []string {
	out := make([]string, len(sls))
	for i, sl := range sls {
		out[i] = sl.line
	}
	return out
}

func drainLines(lines chan streamLine) []streamLine {
	close(lines)
	var got []streamLine
	for sl := range lines {
		got = append(got, sl)
	}
	return got
}

func TestCopyStreamWritesEveryByteToOut(t *testing.T) {
	in := strings.NewReader("line one\nline two\nline three\n")
	var out bytes.Buffer
	lines := make(chan streamLine, 8)
	var dropped int64
	if err := copyStream(&out, in, streamStderr, lines, &dropped); err != nil {
		t.Fatalf("copyStream: %v", err)
	}
	if out.String() != "line one\nline two\nline three\n" {
		t.Errorf("out = %q, want the input unchanged", out.String())
	}
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0", dropped)
	}
}

func TestCopyStreamExtractsCompleteLinesAndFinalPartial(t *testing.T) {
	in := strings.NewReader("a\nb\nno-newline-tail")
	var out bytes.Buffer
	lines := make(chan streamLine, 8)
	var dropped int64
	if err := copyStream(&out, in, streamStderr, lines, &dropped); err != nil {
		t.Fatalf("copyStream: %v", err)
	}
	got := linesOf(drainLines(lines))
	want := []string{"a", "b", "no-newline-tail"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestCopyStreamTrimsCarriageReturn(t *testing.T) {
	in := strings.NewReader("crlf line\r\nplain line\n")
	var out bytes.Buffer
	lines := make(chan streamLine, 8)
	var dropped int64
	if err := copyStream(&out, in, streamStderr, lines, &dropped); err != nil {
		t.Fatalf("copyStream: %v", err)
	}
	got := linesOf(drainLines(lines))
	if len(got) != 2 || got[0] != "crlf line" || got[1] != "plain line" {
		t.Errorf("lines = %v, want [crlf line, plain line] (CR stripped)", got)
	}
	// The terminal copy itself must stay byte-for-byte exact -- only the
	// line-extraction side trims CR.
	if out.String() != "crlf line\r\nplain line\n" {
		t.Errorf("out = %q, want the CRLF preserved for the terminal", out.String())
	}
}

// TestCopyStreamTagsEveryLineWithItsStream is the per-stream-Joiner
// prerequisite (the naming ADR's "Joiner por
// stream"): the detector can only keep separate Joiners per stream if every
// streamLine actually carries the stream it came from.
func TestCopyStreamTagsEveryLineWithItsStream(t *testing.T) {
	in := strings.NewReader("a\nb\n")
	lines := make(chan streamLine, 8)
	var dropped int64
	if err := copyStream(io.Discard, in, streamStdout, lines, &dropped); err != nil {
		t.Fatalf("copyStream: %v", err)
	}
	for _, sl := range drainLines(lines) {
		if sl.stream != streamStdout {
			t.Errorf("line %q tagged stream %v, want streamStdout", sl.line, sl.stream)
		}
	}
}

// TestCopyStreamNeverBlocksOnAFullChannel is pump.go's core contract
// (the naming ADR §3): a full lines channel must
// drop and count, never apply back-pressure to the read/write loop. This
// feeds far more lines than the channel can hold and a slow/absent reader,
// and asserts copyStream still finishes quickly and every byte still
// reached `out`.
func TestCopyStreamNeverBlocksOnAFullChannel(t *testing.T) {
	const n = 5000
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString("line\n")
	}
	in := strings.NewReader(sb.String())
	var out bytes.Buffer
	lines := make(chan streamLine, 4) // tiny on purpose; nobody drains it
	var dropped int64

	done := make(chan error, 1)
	go func() { done <- copyStream(&out, in, streamStderr, lines, &dropped) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("copyStream: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("copyStream did not return within 5s with an undrained channel -- it blocked")
	}
	if out.Len() != n*len("line\n") {
		t.Errorf("out has %d bytes, want %d (every byte must still reach the terminal)", out.Len(), n*len("line\n"))
	}
	if atomic.LoadInt64(&dropped) == 0 {
		t.Error("dropped = 0, want > 0 (the channel should have overflowed)")
	}
}

func TestSendLineDropsOnFullChannelWithoutBlocking(t *testing.T) {
	lines := make(chan streamLine, 2)
	var dropped int64
	var gap bool
	sendLine(lines, streamStderr, "first", &gap, &dropped)
	sendLine(lines, streamStderr, "second", &gap, &dropped) // channel now full (cap 2)
	sendLine(lines, streamStderr, "third", &gap, &dropped)  // must drop, not block
	if dropped != 1 {
		t.Errorf("dropped = %d, want 1", dropped)
	}
	if got := <-lines; got.line != "first" {
		t.Errorf("buffered line = %q, want %q", got.line, "first")
	}
	if got := <-lines; got.line != "second" {
		t.Errorf("buffered line = %q, want %q", got.line, "second")
	}
}

// TestSendLineTagsTheLineAfterADropWithGap is the other half of the
// drop-must-lose-not-fabricate rule (the naming ADR,
// extended for --scan both): the first line that successfully lands after
// one or more drops must say so, and the flag must clear again once it has
// been consumed by one delivered line.
func TestSendLineTagsTheLineAfterADropWithGap(t *testing.T) {
	lines := make(chan streamLine, 1) // full after the first send
	var dropped int64
	var gap bool
	sendLine(lines, streamStderr, "first", &gap, &dropped) // fills the channel, gap stays false
	sendLine(lines, streamStderr, "dropped", &gap, &dropped)
	if dropped != 1 || !gap {
		t.Fatalf("after a drop: dropped=%d gap=%v, want 1 true", dropped, gap)
	}
	<-lines // drain "first" so the next send has room
	sendLine(lines, streamStderr, "recovered", &gap, &dropped)
	got := <-lines
	if !got.gap {
		t.Error("the line delivered right after a drop must carry gap:true")
	}
	if gap {
		t.Error("gap must clear once a line was actually delivered")
	}
	sendLine(lines, streamStderr, "next", &gap, &dropped)
	if got2 := <-lines; got2.gap {
		t.Error("a line delivered with no preceding drop must carry gap:false")
	}
}

// A copyStream whose in.Read never returns must not be how we test EOF
// handling; io.EOF specifically must terminate cleanly with nil error.
func TestCopyStreamReturnsNilOnEOF(t *testing.T) {
	r, w := io.Pipe()
	lines := make(chan streamLine, 8)
	var dropped int64
	done := make(chan error, 1)
	go func() { done <- copyStream(io.Discard, r, streamStderr, lines, &dropped) }()
	if _, err := w.Write([]byte("hi\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("copyStream returned %v, want nil on EOF", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("copyStream did not return after the writer closed")
	}
}

// TestScanChunkCapsPartialAndDiscardsUntilNewline pins CC-10's bounded
// buffer algorithm deterministically: an unterminated line grows partial
// only up to maxPartialLineBytes, the bytes past the cap are dropped
// (discarding mode) until the next newline, and delivery then resumes with
// the line AFTER the oversized one.
func TestScanChunkCapsPartialAndDiscardsUntilNewline(t *testing.T) {
	lines := make(chan streamLine, 16)
	var dropped int64
	fill := bytes.Repeat([]byte("x"), pumpReadBufSize)

	var partial []byte
	var pendingGap bool
	var discarding bool
	steps := 0
	for ; steps < 8; steps++ { // 8 x 32 KiB = 256 KiB, well past the 64 KiB cap
		partial, pendingGap, discarding = scanChunk(partial, fill, discarding, streamStderr, lines, &dropped, pendingGap)
		if len(partial) > maxPartialLineBytes {
			t.Fatalf("step %d: partial grew to %d bytes, want <= %d (CC-10)", steps, len(partial), maxPartialLineBytes)
		}
	}
	if len(partial) != maxPartialLineBytes {
		t.Errorf("partial = %d bytes after %d KiB with no newline, want exactly the %d-byte cap", len(partial), steps*pumpReadBufSize/1024, maxPartialLineBytes)
	}
	if !discarding {
		t.Error("discarding = false after the cap was hit; the pump must enter discarding mode (CC-10)")
	}

	// Still inside the oversized line: a chunk with no newline changes
	// nothing, and one that ends the line resumes delivery after it.
	partial, pendingGap, discarding = scanChunk(partial, []byte("still-no-newline"), discarding, streamStderr, lines, &dropped, pendingGap)
	if !discarding || len(partial) != maxPartialLineBytes {
		t.Fatalf("discarding mode did not hold: discarding=%v partial=%d", discarding, len(partial))
	}
	partial, pendingGap, discarding = scanChunk(partial, []byte("TAIL-BYTES\nafter line\n"), discarding, streamStderr, lines, &dropped, pendingGap)
	if discarding {
		t.Fatal("discarding still set after the oversized line's newline; delivery must resume")
	}
	close(lines)
	got := linesOf(drainFrom(lines))
	// The capped prefix of the oversized line is delivered (the Joiner
	// truncates to 8 KiB anyway), then the line after it, verbatim.
	want := []string{strings.Repeat("x", maxPartialLineBytes), "after line"}
	if len(got) != len(want) {
		t.Fatalf("lines after discarding = %d (%q...), want %d", len(got), got[0][:min(32, len(got[0]))], len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d after discarding = %.32q..., want %.32q...", i, got[i], want[i])
		}
	}
}

// drainFrom is drainLines for a channel the test has already closed after
// filling, so it only collects what is buffered.
func drainFrom(lines chan streamLine) []streamLine {
	var got []streamLine
	for sl := range lines {
		got = append(got, sl)
	}
	return got
}

// countWriter counts the bytes copyStream passes through to the terminal,
// proving the bounded line buffer never eats output.
type countWriter struct{ n int64 }

func (w *countWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}

// bigLineReader yields size bytes with no newline, then "\nfin\n" -- the
// shape of a child hammering one enormous line into fd 2 (CC-10's repro).
// It materializes only one pumpReadBufSize chunk at a time.
type bigLineReader struct {
	remaining int
	drained   bool
}

func (r *bigLineReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		if r.drained {
			return 0, io.EOF
		}
		r.drained = true
		tail := []byte("\nfin\n")
		return copy(p, tail), nil
	}
	n := min(len(p), r.remaining)
	for i := range n {
		p[i] = 'x'
	}
	r.remaining -= n
	return n, nil
}

// TestCopyStreamBoundedAndFastOnHugeNewlineFreeWrite is CC-10's end-to-end
// property: a multi-MiB newline-free write must keep the pump's buffer
// bounded and stay fast (the old code rescanned and re-appended the whole
// accumulated partial on every 32 KiB read, which the finding measured at
// 4 s / 403 MB RSS for 128 MiB; the chunk-scanned pump does one linear
// pass). The emitted giant line is exactly maxPartialLineBytes long, the
// byte stream still reaches the terminal in full, and the line after the
// giant one is still delivered.
func TestCopyStreamBoundedAndFastOnHugeNewlineFreeWrite(t *testing.T) {
	const size = 128 << 20 // 128 MiB
	in := &bigLineReader{remaining: size}
	out := &countWriter{}
	lines := make(chan streamLine, 8)
	var dropped int64
	start := time.Now()
	if err := copyStream(out, in, streamStderr, lines, &dropped); err != nil {
		t.Fatalf("copyStream: %v", err)
	}
	elapsed := time.Since(start)

	close(lines)
	got := linesOf(drainFrom(lines))
	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2 (the capped giant line + fin): %v", len(got), got)
	}
	if len(got[0]) != maxPartialLineBytes {
		t.Errorf("giant line delivered at %d bytes, want exactly the %d-byte cap", len(got[0]), maxPartialLineBytes)
	}
	if got[1] != "fin" {
		t.Errorf("line after the giant one = %q, want \"fin\"", got[1])
	}
	if want := int64(size) + int64(len("\nfin\n")); out.n != want {
		t.Errorf("terminal received %d bytes, want all %d (the cap must bound the buffer, never the copy)", out.n, want)
	}
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0 (size-capping is not a channel drop)", dropped)
	}
	if elapsed > 2*time.Second {
		t.Errorf("copyStream took %v for a %d MiB newline-free write; the chunk scan must stay linear (CC-10)", elapsed, size>>20)
	}
}
