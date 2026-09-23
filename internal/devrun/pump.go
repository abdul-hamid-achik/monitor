package devrun

import (
	"bytes"
	"io"
	"sync/atomic"
)

// linesChanCap bounds the detector's line channel (docs/contracts/
// local-sentry-naming.md §3): the copy goroutine's send is always
// non-blocking, so a full channel drops the line and counts it rather than
// ever applying back-pressure to the child.
const linesChanCap = 1024

// pumpReadBufSize is the chunk size the copy goroutine reads at once. It is
// unrelated to line splitting -- copyStream writes every chunk to the
// terminal exactly as read, before it ever looks for a line boundary inside
// it.
const pumpReadBufSize = 32 * 1024

// copyStream is the "golden rule" goroutine (docs/contracts/
// local-sentry-naming.md §3, "No dejar que la copia de stderr/stdout espere
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
func copyStream(out io.Writer, in io.Reader, lines chan<- string, dropped *int64) error {
	buf := make([]byte, pumpReadBufSize)
	var partial []byte
	for {
		n, rerr := in.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if _, werr := out.Write(chunk); werr != nil {
				return werr
			}
			partial = extractLines(append(partial, chunk...), lines, dropped)
		}
		if rerr != nil {
			if len(partial) > 0 {
				sendLine(lines, string(partial), dropped)
			}
			if rerr == io.EOF {
				return nil
			}
			return rerr
		}
	}
}

// extractLines splits every complete line ("...\n") off the front of buf,
// sending each (CR-trimmed) to lines via sendLine, and returns whatever
// incomplete tail remains for the next read to extend.
func extractLines(buf []byte, lines chan<- string, dropped *int64) []byte {
	for {
		idx := bytes.IndexByte(buf, '\n')
		if idx < 0 {
			return buf
		}
		line := bytes.TrimSuffix(buf[:idx], []byte("\r"))
		sendLine(lines, string(line), dropped)
		buf = buf[idx+1:]
	}
}

// sendLine makes exactly one non-blocking attempt to deliver line to lines.
// A full channel increments dropped instead of waiting -- the copy
// goroutine's write to the terminal must never be slowed down by the
// detector falling behind.
func sendLine(lines chan<- string, line string, dropped *int64) {
	select {
	case lines <- line:
	default:
		atomic.AddInt64(dropped, 1)
	}
}
