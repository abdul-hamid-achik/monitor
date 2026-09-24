package cli

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/contextids"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/project"
	"github.com/abdul-hamid-achik/monitor/internal/scrub"
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
		file        string
		projectFlag string
		service     string
		root        string
		fromStart   bool
		record      bool
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

Detection only by default: nothing is written to the issues store. With
--record, every detected exception is instead recorded into the issues
store (FingerprintV2Exception, ExceptionInfo, Culprit -- the same
pipeline internal/issues.RecordException uses), and reprocessing is
idempotent: a per-file checkpoint at $XDG_STATE_HOME/monitor/parse/
<sha256(abspath)>.json tracks {inode, size, offset} so re-running
--record over a log that has not grown past its checkpoint replays
nothing (--from-start ignores the checkpoint and replays the whole
file; a DedupeKey derived from the file's inode, path, and each
block's byte offset also protects a --from-start replay against
double-counting). ObservedAt for a recorded occurrence is the log
line's own timestamp when one could be read, else the file's mtime --
never the current time, so replaying an old log never bumps an issue's
last_seen to "now".`,
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rootDir := "."
			if file != "" {
				rootDir = filepath.Dir(file)
			}
			gitRoot := root
			if gitRoot == "" {
				gitRoot, _ = findGitRoot(rootDir)
			}
			if gitRoot == "" && rootDir != "." {
				gitRoot, _ = findGitRoot(".")
			}

			if record {
				if file == "" {
					return errors.New("--record requires --file")
				}
				storePath, err := issues.ResolvePath("")
				if err != nil {
					return err
				}
				return runStacktraceRecord(cmd.Context(), cmd.OutOrStdout(), stacktraceRecordOptions{
					file:      file,
					project:   projectFlag,
					service:   service,
					gitRoot:   gitRoot,
					fromStart: fromStart,
					storePath: storePath,
				})
			}

			var r io.Reader = cmd.InOrStdin()
			live := true
			if file != "" {
				f, err := os.Open(file)
				if err != nil {
					return fmt.Errorf("open %s: %w", file, err)
				}
				defer f.Close()
				r = f
				live = false
			}
			return runStacktraceParse(r, cmd.OutOrStdout(), stacktraceParseOptions{
				project: projectFlag,
				service: service,
				gitRoot: gitRoot,
				live:    live,
				tick:    stacktraceTickInterval,
			})
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "path to read (default: stdin)")
	cmd.Flags().StringVar(&projectFlag, "project", "", "project tag to stamp on each emitted event")
	cmd.Flags().StringVar(&service, "service", "", "service tag to stamp on each emitted event")
	cmd.Flags().StringVar(&root, "root", "", "git root for in_app and relative filenames (default: discovered)")
	cmd.Flags().BoolVar(&record, "record", false,
		"record detected exceptions into the issues store (idempotent via a per-file checkpoint; requires --file)")
	cmd.Flags().BoolVar(&fromStart, "from-start", false,
		"with --record, ignore the saved checkpoint and replay the whole file from byte 0 (no effect without --record, which always reads from the start)")
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

// fileReplayPIDHint is a nonzero sentinel passed as project.Hints.PID for
// `stacktrace parse --record`: Resolve treats PID<=0 as "a host-wide event
// with no process attached" and skips the git-root/marker walk entirely in
// favor of project "host" (see project.Hints.PID's doc comment) -- but a log
// FILE always has a real directory to walk even though there is no live
// process behind a replay. The value is never stored anywhere; Identity
// carries no PID field, so this only steers Resolve's own precedence rules.
const fileReplayPIDHint int32 = 1

// stacktraceRecordOptions configures one `stacktrace parse --record` run.
type stacktraceRecordOptions struct {
	file, project, service, gitRoot string
	fromStart                       bool
	storePath                       string
}

// recordSummary is --record's one-line human summary (the roadmap's Act-1
// demo mockup: "parsed 1,204 lines · 0 new blocks (checkpoint at offset
// 88,412 · inode 4410) · 0 occurrences written").
type recordSummary struct {
	LinesParsed        int    `json:"lines_parsed"`
	NewBlocks          int    `json:"new_blocks"`
	OccurrencesWritten int    `json:"occurrences_written"`
	CheckpointOffset   int64  `json:"checkpoint_offset"`
	CheckpointInode    uint64 `json:"checkpoint_inode"`
	// Deduped is how many detected blocks matched an already-retained
	// DedupeKey (UpsertResult.Deduped) and so were NOT counted in
	// OccurrencesWritten -- e.g. a --from-start replay of already-seen
	// bytes. Not part of the roadmap's fixed mockup text, so it is only
	// appended when non-zero, keeping the common-case line unchanged.
	Deduped int `json:"deduped,omitempty"`
}

func (s recordSummary) String() string {
	line := fmt.Sprintf("parsed %d lines · %d new blocks (checkpoint at offset %d · inode %d) · %d occurrences written",
		s.LinesParsed, s.NewBlocks, s.CheckpointOffset, s.CheckpointInode, s.OccurrencesWritten)
	if s.Deduped > 0 {
		line += fmt.Sprintf(" · %d deduped", s.Deduped)
	}
	return line
}

// runStacktraceRecord reads opts.file from its checkpointed byte offset (or
// from 0 with --from-start), detects exceptions exactly like the plain
// parse path, and records each one via issues.RecordException. It always
// advances and saves the checkpoint to wherever reading stopped -- even
// when nothing new was found -- so a clean log's checkpoint still tracks
// the file's growth.
//
// EOF is NOT automatically treated as a block boundary the way the plain
// (non --record) parse path treats it: hitting EOF while a writer is still
// mid-trace looks identical, from here, to a log that is genuinely done.
// Force-closing (Flush) and recording whatever is left open in the FIRST
// case fabricates a wrongly-shaped issue from a truncated fragment, and
// then the checkpoint permanently skips past the very bytes that would
// have completed it correctly on a later run (see stacktrace.SettleWindow's
// doc comment). So: every block the Joiner closes STRUCTURALLY during this
// run (a later boundary line arriving, e.g. the next trace starting) is
// safe to record and checkpoint past immediately -- nothing can retroactively
// un-close it. Only the very LAST block, if the file's own content ends
// while it is still open, is held back: it is force-closed and recorded
// (and the checkpoint advances past it) only when the file's mtime is
// already older than SettleWindow, i.e. nothing has touched it recently
// enough to still be mid-write. Otherwise the checkpoint stops at the last
// SAFE (structurally closed) point, and the next --record run re-reads the
// open fragment from scratch, into a fresh Joiner, alongside whatever got
// appended since.
func runStacktraceRecord(ctx context.Context, w io.Writer, opts stacktraceRecordOptions) error {
	absPath, err := filepath.Abs(opts.file)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", opts.file, err)
	}
	f, err := os.Open(absPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", opts.file, err)
	}
	defer f.Close()

	start, cp, err := stacktrace.ResolveOffset(f, absPath, opts.fromStart)
	if err != nil {
		return fmt.Errorf("stat %s: %w", opts.file, err)
	}
	// mtime is the file's mtime AT THE START of this run (ResolveOffset's
	// own stat, via cp.MtimeNs) -- the ObservedAt fallback for every block
	// this run processes (see recordParsedException), same as before CC-1
	// added Checkpoint.MtimeNs. The checkpoint SAVED at the end of this run
	// stamps a separately re-stat'd, later mtime (see fileSettled below),
	// not this one -- so the NEXT run's CC-1 rewrite check compares against
	// what this run actually left the file at.
	mtime := time.Unix(0, cp.MtimeNs)
	if start > 0 {
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return fmt.Errorf("seek %s: %w", opts.file, err)
		}
	}

	id := project.Resolve(project.Hints{
		ExplicitProject: opts.project,
		ExplicitService: opts.service,
		Dir:             filepath.Dir(absPath),
		PID:             fileReplayPIDHint,
	})
	if opts.gitRoot != "" {
		id.GitRoot = opts.gitRoot
		id.Root = opts.gitRoot
	}
	run := contextids.FromEnv(contextids.IDs{})
	scrubber := scrub.New(scrub.WithValues(scrub.SecretEnvValues(os.Environ(), nil)))

	j := stacktrace.NewJoiner()
	lr := newCheckpointLineReader(f, stacktrace.DefaultMaxBytes)

	offset := start
	// safeOffset is the byte offset up to which every block the Joiner has
	// produced so far is the result of a STRUCTURAL close (a later
	// boundary line arriving), never an EOF-forced one -- see the func
	// doc comment. It only ever advances to the start of a line that
	// itself triggered an earlier block's closure, which is exactly what
	// "safe to checkpoint past" means here.
	safeOffset := start
	// lineOffsets[n] is the byte offset of the n-th line fed to j this run
	// (1-based, matching Block.LineStart/LineEnd) -- index 0 is unused.
	lineOffsets := []int64{0}
	summary := recordSummary{CheckpointInode: cp.Inode}

	process := func(blocks []stacktrace.Block) error {
		for _, b := range blocks {
			summary.NewBlocks++
			ex := stacktrace.Parse(b)
			if ex == nil {
				continue
			}
			blockOffset := int64(0)
			if b.LineStart > 0 && b.LineStart < len(lineOffsets) {
				blockOffset = lineOffsets[b.LineStart]
			}
			deduped, err := recordParsedException(ctx, opts.storePath, cp.Dev, cp.Inode, cp.Generation, blockOffset, b, ex, id, run, scrubber, mtime)
			if err != nil {
				return err
			}
			if deduped {
				summary.Deduped++
			} else {
				summary.OccurrencesWritten++
			}
		}
		return nil
	}

	base := time.Unix(0, 0)
	n := 0
	var readErr error
	for {
		line, consumed, rerr := lr.next()
		if rerr != nil {
			readErr = rerr
			break
		}
		n++
		summary.LinesParsed++
		lineOffsets = append(lineOffsets, offset)
		offset += consumed
		blocks := j.Feed(line, base.Add(time.Duration(n)*time.Microsecond))
		if len(blocks) > 0 {
			if err := process(blocks); err != nil {
				return err
			}
			// Every block just closed did so structurally (Feed only
			// force-closes a block on its own idle timeout, which cannot
			// fire on this synthetic, monotonically-microsecond clock);
			// the line that triggered it (this one) starts the new safe
			// boundary.
			safeOffset = lineOffsets[n]
		}
	}
	if readErr != io.EOF {
		return fmt.Errorf("read %s: %w", opts.file, readErr)
	}

	finalOffset := safeOffset
	settled, endMtime := fileSettled(f, mtime)
	if settled {
		if err := process(j.Flush()); err != nil {
			return err
		}
		finalOffset = offset
	}

	cp.Offset = finalOffset
	// Stamp the checkpoint with THIS run's own end-of-read mtime/content
	// fingerprint (not the start-of-run mtime `mtime` still holds -- that
	// one remains the ObservedAt fallback above), so the NEXT run's CC-1
	// rewrite check (ResolveOffset) compares against what this run
	// actually left the file at.
	cp.MtimeNs = endMtime.UnixNano()
	cp.PrefixHash = stacktrace.PrefixHash(f, finalOffset)
	if err := stacktrace.SaveCheckpointByKey(stacktrace.CheckpointKey{Dev: cp.Dev, Inode: cp.Inode}, cp); err != nil {
		return fmt.Errorf("save checkpoint: %w", err)
	}
	summary.CheckpointOffset = cp.Offset

	fmt.Fprintln(w, summary.String())
	return nil
}

// fileSettled reports whether f's CURRENT mtime (re-stat'd here, not the
// snapshot taken before reading -- a writer that appended WHILE this run
// was reading must still be caught) already predates stacktrace.
// SettleWindow, i.e. nothing has written to it recently enough to still be
// mid-trace, and also returns that current mtime (or fallback, on a Stat
// failure) for the caller to stamp onto the checkpoint it is about to save
// (CC-1: the NEXT run's rewrite check needs THIS run's own end-of-read
// mtime, not the one observed when this run started). A Stat failure fails
// OPEN (returns true, the pre-existing always-flush behavior) rather than
// silently never finishing a legitimately complete log because of an
// unrelated stat error.
func fileSettled(f *os.File, fallback time.Time) (settled bool, mtime time.Time) {
	info, err := f.Stat()
	mtime = fallback
	if err == nil {
		mtime = info.ModTime()
	}
	return err != nil || time.Since(mtime) >= stacktrace.SettleWindow, mtime
}

// recordParsedException applies the git root, scrubs the exception's text,
// derives ObservedAt and the reprocess DedupeKey (docs/contracts/
// local-sentry-naming.md §4-5), and writes one occurrence via
// issues.RecordException. The returned bool is UpsertResult.Deduped: the
// caller only counts a write toward "occurrences written" when it is
// false, so a --from-start replay (or any other reprocess that lands on an
// already-retained DedupeKey) reports the truth instead of claiming N
// occurrences written when the store actually deduped every one of them.
//
// dev/inode/generation (CC-2, CC-1) replace the file's absPath in the
// DedupeKey seed: a path breaks across a logrotate-style rename (the same
// physical file, same dev/inode, gets a NEW path) and across two spellings
// of the same file (a symlinked root, CC-4), either of which used to
// double-count every occurrence already recorded under the old path.
// generation -- ResolveOffset's Checkpoint.Generation, bumped on every
// detected rotation/truncation/rewrite -- keeps a genuinely rewritten
// file's occurrences from silently deduping against a PREVIOUS
// generation's occurrence recorded at the same byte offset, while two
// --from-start replays of the SAME (unrotated) generation still dedupe
// against each other correctly.
func recordParsedException(ctx context.Context, storePath string, dev, inode uint64, generation int, blockOffset int64, block stacktrace.Block, ex *stacktrace.Exception, id project.Identity, run contextids.IDs, scrubber *scrub.Scrubber, mtime time.Time) (deduped bool, err error) {
	stacktrace.ApplyGitRoot(ex, id.GitRoot)
	scrubException(scrubber, ex)

	observedAt := ex.ObservedAt
	if observedAt.IsZero() {
		observedAt = mtime
	}

	dedupeSeed := fmt.Sprintf("%d:%d:%d:%d:%s", dev, inode, generation, blockOffset, stacktrace.HashBlock(block.Text()))
	sum := sha256.Sum256([]byte(dedupeSeed))

	result, err := issues.RecordException(ctx, storePath, issues.DefaultWriterWait, *ex, id, run, issues.RecordExceptionOptions{
		ObservedAt: observedAt,
		DedupeKey:  hex.EncodeToString(sum[:]),
	})
	return result.Deduped, err
}

// scrubException redacts ex's Type/Value and every frame's Function text,
// recursively through Chained, using scrubber -- the golden rule that error
// text is untrusted data and must be redacted before it is persisted or
// printed (docs/contracts/local-sentry-naming.md's "Scrub por defecto").
// Filename/AbsPath are left alone: they are resolved, checked paths (see
// stacktrace.ApplyGitRoot), not attacker- or user-controlled message text.
func scrubException(scrubber *scrub.Scrubber, ex *stacktrace.Exception) {
	if ex == nil {
		return
	}
	ex.Type = scrubber.String(ex.Type)
	ex.Value = scrubber.String(ex.Value)
	for i := range ex.Frames {
		ex.Frames[i].Function = scrubber.String(ex.Frames[i].Function)
	}
	for i := range ex.Chained {
		scrubException(scrubber, &ex.Chained[i])
	}
}

// checkpointLineReader is lineReader's --record sibling: it additionally
// reports the exact number of raw bytes consumed from the underlying reader
// for each line (including any line terminator), which --record needs to
// advance the checkpoint's byte offset precisely. Using len(line)+1 instead
// would silently drift on CRLF input and on a line long enough to be
// truncated (the underlying reader still consumes the full raw chunk even
// though the returned line is capped at max).
type checkpointLineReader struct {
	br  *bufio.Reader
	max int
	err error
}

func newCheckpointLineReader(r io.Reader, max int) *checkpointLineReader {
	return &checkpointLineReader{br: bufio.NewReaderSize(r, 64*1024), max: max}
}

// next returns the next line (EOL stripped), the exact number of raw bytes
// consumed for it, and any read error. A final line with no trailing
// newline is returned (with its own byte count) before the reader's error.
func (r *checkpointLineReader) next() (line string, consumed int64, err error) {
	if r.err != nil {
		return "", 0, r.err
	}
	var buf []byte
	for {
		chunk, rerr := r.br.ReadSlice('\n')
		consumed += int64(len(chunk))
		if room := r.max - len(buf); room > 0 {
			buf = append(buf, chunk[:min(len(chunk), room)]...)
		}
		if errors.Is(rerr, bufio.ErrBufferFull) {
			continue
		}
		if rerr != nil {
			r.err = rerr
			if len(chunk) == 0 && len(buf) == 0 {
				return "", consumed, rerr
			}
		}
		return string(trimEOL(buf)), consumed, nil
	}
}
