package cli

import (
	"bufio"
	"encoding/json"
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

This is detection only: nothing is written to the issues store. That
is --record, added once internal/issues carries the exception model
(FingerprintV2Exception, ExceptionInfo, Culprit) this command's output
is meant to feed.`,
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var r io.Reader = cmd.InOrStdin()
			gitRootDir := "."
			if file != "" {
				f, err := os.Open(file)
				if err != nil {
					return fmt.Errorf("open %s: %w", file, err)
				}
				defer f.Close()
				r = f
				gitRootDir = filepath.Dir(file)
			}
			// --from-start is accepted for forward compatibility with
			// --record's checkpoint resume (a file position saved under
			// $XDG_STATE_HOME/monitor/parse/<sha(path)>.json); this build
			// has no checkpoint to resume from, so parse always reads its
			// input from the start regardless of this flag's value.
			_ = fromStart

			gitRoot, _ := findGitRoot(gitRootDir)
			enc := json.NewEncoder(cmd.OutOrStdout())

			j := stacktrace.NewJoiner()
			scanner := bufio.NewScanner(r)
			scanner.Buffer(make([]byte, 64*1024), 1024*1024)
			emit := func(bs []stacktrace.Block) error {
				for _, b := range bs {
					ex := stacktrace.Parse(b)
					if ex == nil {
						continue
					}
					applyInApp(ex, gitRoot)
					if err := enc.Encode(parsedEvent{Project: project, Service: service, Exception: ex}); err != nil {
						return err
					}
				}
				return nil
			}
			for scanner.Scan() {
				line := stacktrace.StripANSI(scanner.Text())
				if err := emit(j.Feed(line, time.Now())); err != nil {
					return fmt.Errorf("write event: %w", err)
				}
			}
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("read input: %w", err)
			}
			return emit(j.Flush())
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "path to read (default: stdin)")
	cmd.Flags().StringVar(&project, "project", "", "project tag to stamp on each emitted event")
	cmd.Flags().StringVar(&service, "service", "", "service tag to stamp on each emitted event")
	cmd.Flags().BoolVar(&fromStart, "from-start", false,
		"reserved for --record's checkpoint resume (not implemented in this build; parse always reads from the start)")
	return cmd
}

// applyInApp resolves InApp for the top-level exception's frames and every
// frame in its chained causes. stacktrace.Parse never touches the
// filesystem, so this is the CLI's job once it knows a git root.
func applyInApp(ex *stacktrace.Exception, gitRoot string) {
	for i := range ex.Frames {
		ex.Frames[i].InApp = stacktrace.InApp(ex.Frames[i], gitRoot)
	}
	for c := range ex.Chained {
		applyInApp(&ex.Chained[c], gitRoot)
	}
}

// findGitRoot walks up from start looking for a ".git" entry, the same
// marker-walk convention internal/procbind uses for codebase roots.
// Returns ("", false) when none is found (e.g. stdin with no --file, or a
// file outside any repository); callers then leave every frame's InApp
// false rather than guess.
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
