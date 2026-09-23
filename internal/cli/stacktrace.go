package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/stacktrace"
)

// parsedEvent is one NDJSON output record: the detected Exception plus the
// project/service tags the caller asked to stamp on it. There is no store
// write here (that's `--record`, a later addition once internal/issues'
// exception model lands); this command only detects and prints.
type parsedEvent struct {
	Project string `json:"project,omitempty"`
	Service string `json:"service,omitempty"`
	*stacktrace.Exception
}

// stacktraceTickInterval is how often a live (stdin) parse lets the Joiner
// close blocks that have been idle for stacktrace.DefaultIdle.
const stacktraceTickInterval = 100 * time.Millisecond

// newStacktraceCmd is the hidden low-level entry point into
// internal/stacktrace: it reads raw text (a file, or stdin) and prints one
// JSON object per detected exception. It is hidden because the intended
// front door is `monitor run -- <cmd>` (which wires this up to a live
// process's stderr/stdout automatically); this command exists for
// dogfooding the detector directly and for reprocessing an existing log by
// hand.
func newStacktraceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "stacktrace <subcommand>",
		Short:  "Low-level stack-trace detector (internal use)",
		Hidden: true,
	}
	cmd.AddCommand(newStacktraceParseCmd())
	return cmd
}

func newStacktraceParseCmd() *cobra.Command {
	var (
		file      string
		project   string
		service   string
		root      string
		fromStart bool
	)
	cmd := &cobra.Command{
		Use:   "parse",
		Short: "Detect exceptions in a log file or stdin and print them as NDJSON",
		Long: `parse reads raw stderr/stdout/log text -- from --file, or from stdin
when --file is omitted -- and prints one JSON object per detected
exception (internal/stacktrace's Exception, tagged with --project and
--service). A block no parser recognizes contributes no line: clean
logs print nothing.

Frames under the git root (--root, else the repository containing
--file, else the one containing the working directory) are marked
in_app and their filename is made root-relative; abs_path keeps the
absolute path. Lines longer than 64 KiB are truncated, never fatal. On
stdin, a trace is printed once its stream has been idle for 300ms, so
a live pipe does not have to reach EOF.

This is detection only: nothing is written to the issues store. That
is --record, added once internal/issues carries the exception model
(FingerprintV2Exception, ExceptionInfo, Culprit) this command's output
is meant to feed.`,
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var r io.Reader = cmd.InOrStdin()
			live := true
			rootDir := "."
			if file != "" {
				f, err := os.Open(file)
				if err != nil {
					return fmt.Errorf("open %s: %w", file, err)
				}
				defer f.Close()
				r = f
				live = false
				rootDir = filepath.Dir(file)
			}
			// --from-start is accepted for forward compatibility with
			// --record's checkpoint resume (a file position saved under
			// $XDG_STATE_HOME/monitor/parse/<sha(path)>.json); this build
			// has no checkpoint to resume from, so parse always reads its
			// input from the start regardless of this flag's value.
			_ = fromStart

			gitRoot := root
			if gitRoot == "" {
				gitRoot, _ = findGitRoot(rootDir)
			}
			if gitRoot == "" && rootDir != "." {
				gitRoot, _ = findGitRoot(".")
			}
			return runStacktraceParse(r, cmd.OutOrStdout(), stacktraceParseOptions{
				project: project,
				service: service,
				gitRoot: gitRoot,
				live:    live,
				tick:    stacktraceTickInterval,
			})
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "path to read (default: stdin)")
	cmd.Flags().StringVar(&project, "project", "", "project tag to stamp on each emitted event")
	cmd.Flags().StringVar(&service, "service", "", "service tag to stamp on each emitted event")
	cmd.Flags().StringVar(&root, "root", "", "git root for in_app and relative filenames (default: discovered)")
	cmd.Flags().BoolVar(&fromStart, "from-start", false,
		"reserved for --record's checkpoint resume (not implemented in this build; parse always reads from the start)")
	return cmd
}

type stacktraceParseOptions struct {
	project, service string
	gitRoot          string
	// live drives the Joiner with the real clock and a ticker (stdin);
	// otherwise a synthetic clock keeps a file replay deterministic.
	live bool
	tick time.Duration
}

// runStacktraceParse streams r through a Joiner and writes one NDJSON event
// per detected exception. Whatever is buffered is always flushed -- at EOF
// and before returning a read error -- so a bad tail never discards the
// exceptions before it.
func runStacktraceParse(r io.Reader, w io.Writer, opts stacktraceParseOptions) error {
	enc := json.NewEncoder(w)
	j := stacktrace.NewJoiner()
	emit := func(bs []stacktrace.Block) error {
		for _, b := range bs {
			ex := stacktrace.Parse(b)
			if ex == nil {
				continue
			}
			stacktrace.ApplyGitRoot(ex, opts.gitRoot)
			if err := enc.Encode(parsedEvent{Project: opts.project, Service: opts.service, Exception: ex}); err != nil {
				return fmt.Errorf("write event: %w", err)
			}
		}
		return nil
	}
	finish := func(readErr error) error {
		if err := emit(j.Flush()); err != nil {
			return err
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("read input: %w", readErr)
		}
		return nil
	}
	lr := newLineReader(r, stacktrace.DefaultMaxBytes)

	if !opts.live {
		base := time.Unix(0, 0)
		for n := 0; ; n++ {
			line, err := lr.next()
			if err != nil {
				return finish(err)
			}
			if err := emit(j.Feed(line, base.Add(time.Duration(n)*time.Microsecond))); err != nil {
				return err
			}
		}
	}

	type readResult struct {
		line string
		err  error
	}
	lines := make(chan readResult)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			line, err := lr.next()
			select {
			case lines <- readResult{line: line, err: err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	tick := opts.tick
	if tick <= 0 {
		tick = stacktraceTickInterval
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case res := <-lines:
			if res.err != nil {
				return finish(res.err)
			}
			if err := emit(j.Feed(res.line, time.Now())); err != nil {
				return err
			}
		case now := <-ticker.C:
			if err := emit(j.Tick(now)); err != nil {
				return err
			}
		}
	}
}

// lineReader reads newline-terminated lines of any length, truncating each
// to max bytes (the rest of an oversized line is discarded) instead of
// failing the way bufio.Scanner does with "token too long".
type lineReader struct {
	br  *bufio.Reader
	max int
	err error
}

func newLineReader(r io.Reader, max int) *lineReader {
	return &lineReader{br: bufio.NewReaderSize(r, 64*1024), max: max}
}

// next returns the next line without its line terminator. A final line with
// no trailing newline is returned before the reader's error.
func (lr *lineReader) next() (string, error) {
	if lr.err != nil {
		return "", lr.err
	}
	var buf []byte
	for {
		chunk, err := lr.br.ReadSlice('\n')
		if room := lr.max - len(buf); room > 0 {
			buf = append(buf, chunk[:min(len(chunk), room)]...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			lr.err = err
			if len(chunk) == 0 && len(buf) == 0 {
				return "", err
			}
		}
		return string(trimEOL(buf)), nil
	}
}

func trimEOL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// findGitRoot walks up from start looking for a ".git" entry, the same
// marker-walk convention internal/procbind uses for codebase roots.
// Returns ("", false) when none is found (e.g. a log file outside any
// repository); callers then leave every frame's InApp false rather than
// guess.
func findGitRoot(start string) (string, bool) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}
	dir := abs
	for {
		if info, err := os.Stat(filepath.Join(dir, ".git")); err == nil && info != nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}
