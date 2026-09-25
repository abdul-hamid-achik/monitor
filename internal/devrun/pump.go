package devrun

import (
	"bytes"
	"io"
	"sync/atomic"
)

//	linesChanCap bounds the detector's line channel (naming ADR §3): the copy goroutine's send
//
// is always
// non-blocking, so a full channel drops the line and counts it rather than
// ever applying back-pressure to the child.
const linesChanCap = 1024

// pumpReadBufSize is the chunk size the copy goroutine reads at once. It is
// unrelated to line splitting -- copyStream writes every chunk to the
// terminal exactly as read, before it ever looks for a line boundary inside
// it.
const pumpReadBufSize = 32 * 1024

// maxPartialLineBytes caps the unterminated-line buffer copyStream keeps
// between reads (CC-10). The stacktrace Joiner truncates every line to its
// own 8 KiB maxLineBytes anyway, so bytes past this ceiling can never reach
// an issue; holding them only lets a multi-miB newline-free write grow the
// buffer without bound (and, worse, rescan it on every read). The Joiner's
// constant is not exported, so this local ceiling -- comfortably above the
// Joiner's 8 KiB, so the detector still sees the full Joiner-bounded
// prefix -- stands on its own here.
const maxPartialLineBytes = 64 << 10

// streamKind identifies which of the child's scanned streams a streamLine
// came from. `--scan both` runs one copy goroutine per stream, each with
// its own streamKind, so the detector can keep one stacktrace.Joiner PER
// STREAM (the naming ADR's "Joiner por stream"
// rule): a heartbeat line interleaved on stdout must never be able to split
// a traceback being accumulated on stderr, or vice versa.
type streamKind uint8

const (
	streamStderr streamKind = iota
	streamStdout
)

// streamLine is one line the copy goroutine hands to the detector.
type streamLine struct {
	stream streamKind
	line   string
	// gap is true on the first line delivered for THIS stream after one or
	// more earlier lines from the same stream were dropped by a full
	// channel (see sendLine): the detector's Joiner for this stream may
	// have an open block that is now missing lines, and must discard it
	// rather than parse the truncated fragment as if it were complete --
	// a drop must lose an event, never fabricate a wrong one.
	gap bool
}

//	copyStream is the "golden rule" goroutine (naming ADR §3, "No dejar que la copia de
//
// stderr/stdout espere
// al detector"): it copies raw bytes from in to out AS SOON AS THEY ARRIVE,
// with no line buffering or detector-driven delay, and only THEN extracts
// whatever complete lines that chunk finished into a non-blocking send on
// lines. When lines is full, the line is dropped and dropped is
// incremented -- never applying back-pressure to the terminal write above
// it, so a stalled detector (the issues store's writer lock held by another
// process, say) can never slow down, let alone block, what the monitored
// child sees.
//
// It returns once in reaches EOF (nil) or a read/write error.
func copyStream(out io.Writer, in io.Reader, stream streamKind, lines chan<- streamLine, dropped *int64) error {
	buf := make([]byte, pumpReadBufSize)
	var partial []byte
	var pendingGap bool
	var discarding bool
	for {
		n, rerr := in.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if _, werr := out.Write(chunk); werr != nil {
				return werr
			}
			partial, pendingGap, discarding = scanChunk(partial, chunk, discarding, stream, lines, dropped, pendingGap)
		}
		if rerr != nil {
			if len(partial) > 0 {
				// A final unterminated line (or the capped prefix of
				// one still in discarding mode) is delivered as-is; the
				// Joiner's own 8 KiB line cap makes the detection outcome
				// identical to what the full line would have produced.
				sendLine(lines, stream, string(partial), &pendingGap, dropped)
			}
			if rerr == io.EOF {
				return nil
			}
			return rerr
		}
	}
}

// scanChunk consumes ONLY the freshly read chunk (CC-10): the newline
// search starts where the previous chunk's search stopped, never at the
// front of the accumulated partial line again, so a huge newline-free
// write costs one linear pass instead of a quadratic rescan per 32 KiB
// read. partial holds the unterminated line so far and never grows past
// maxPartialLineBytes: once it is full, discarding is set and every
// further byte of the same line is dropped until its newline arrives, at
// which point the capped prefix is delivered as the line (the Joiner
// truncates to its own 8 KiB anyway, so detection sees exactly what the
// full line would have given it) and delivery resumes after it. The drop
// is silent by design -- bytes past the cap could never affect detection,
// and they must not be reported as a channel-gap (sendLine's pendingGap)
// that would make the detector discard a perfectly parseable block.
//
// It returns the updated partial buffer, pendingGap flag and discarding
// mode for the next chunk.
func scanChunk(partial []byte, chunk []byte, discarding bool, stream streamKind, lines chan<- streamLine, dropped *int64, pendingGap bool) ([]byte, bool, bool) {
	if discarding {
		idx := bytes.IndexByte(chunk, '\n')
		if idx < 0 {
			// Still inside the oversized line: drop the whole chunk.
			return partial, pendingGap, true
		}
		// The oversized line ends here: deliver its capped prefix, then
		// treat everything after the newline as fresh data.
		line := bytes.TrimSuffix(partial, []byte("\r"))
		sendLine(lines, stream, string(line), &pendingGap, dropped)
		partial = partial[:0]
		chunk = chunk[idx+1:]
		discarding = false
	}
	for {
		idx := bytes.IndexByte(chunk, '\n')
		if idx < 0 {
			room := maxPartialLineBytes - len(partial)
			if room <= 0 {
				return partial, pendingGap, true
			}
			if len(chunk) > room {
				partial = append(partial, chunk[:room]...)
				return partial, pendingGap, true
			}
			partial = append(partial, chunk...)
			return partial, pendingGap, false
		}
		line := chunk[:idx]
		if len(partial) > 0 {
			partial = append(partial, line...)
			line = partial
		}
		line = bytes.TrimSuffix(line, []byte("\r"))
		sendLine(lines, stream, string(line), &pendingGap, dropped)
		partial = partial[:0]
		chunk = chunk[idx+1:]
		if len(chunk) == 0 {
			return partial, pendingGap, discarding
		}
	}
}

// sendLine makes exactly one non-blocking attempt to deliver line to lines.
// A full channel increments dropped and sets *pendingGap instead of
// waiting -- the copy goroutine's write to the terminal must never be
// slowed down by the detector falling behind. The NEXT line that does get
// delivered for this stream carries gap:true (and clears *pendingGap), so
// the detector can tell a clean stream from one with a hole in it.
func sendLine(lines chan<- streamLine, stream streamKind, line string, pendingGap *bool, dropped *int64) {
	select {
	case lines <- streamLine{stream: stream, line: line, gap: *pendingGap}:
		*pendingGap = false
	default:
		atomic.AddInt64(dropped, 1)
		*pendingGap = true
	}
}
