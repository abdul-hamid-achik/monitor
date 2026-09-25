// Command seed writes one issue occurrence directly through
// issues.RecordException (or, for the "alert" scenario,
// issues.Store.UpsertOccurrenceResult) into an isolated issues.veclite
// store, for the local-sentry specs that exercise `monitor issue`/
// `monitor issues`/monitor_issue (specs/issues_context.yml,
// specs/mcp_issue_latest.yml, specs/issues_at.yml).
//
// It exists because those specs need a REAL exception issue -- one with a
// stack culprit or a message-search-findable message, at a real file:line
// -- and monitor's own producers for that (`monitor run -- <cmd>`,
// `monitor stacktrace parse --record`) are E2.4/E2.1's --record halves,
// built in a parallel slice of the same roadmap and not yet landed on this
// branch. `go run` (a few seconds' compile) is an acceptable cost inside a
// spec's target.cmd; go is required on PATH for every glyphrun runtime
// matrix regardless.
//
// This is a standalone testdata program: Go's `...` build pattern already
// skips any directory named "testdata", so `go build ./...`/`go vet
// ./...`/`go test ./...` never touch it -- only an explicit `go run
// ./internal/explain/testdata/seed` (what the specs do) compiles it.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/contextids"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/project"
	"github.com/abdul-hamid-achik/monitor/internal/stacktrace"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	store := fs.String("store", "", "issues.veclite path (required)")
	scenario := fs.String("scenario", "stack", "stack | message | alert")
	projectSlug := fs.String("project", "polyglot", "project slug")
	service := fs.String("service", "", "service name")
	root := fs.String("root", "", "git root (required for scenario=stack; frame paths are resolved relative to it)")
	file := fs.String("file", "", "culprit file, relative to --root (scenario=stack)")
	line := fs.Int("line", 0, "culprit line (scenario=stack)")
	function := fs.String("function", "", "culprit function name")
	typ := fs.String("type", "", "exception type")
	value := fs.String("value", "boom", "exception value/message")
	handled := fs.Bool("handled", true, "stacktrace.Exception.Handled")
	level := fs.String("level", "error", "fatal | error | warning")
	kind := fs.String("kind", "", "for scenario=alert: the rule name appended after monitor.alert.; ignored otherwise")
	observedAgoSeconds := fs.Int("observed-ago-seconds", 0, "how many seconds before now to stamp ObservedAt/LastSeen")
	runID := fs.String("run-id", "", "contextids.IDs.RunID / RunContext.ID")
	release := fs.String("release", "", "contextids.IDs.Release")
	count := fs.Int64("count", 1, "OccurrenceInput.Count")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *store == "" {
		return fmt.Errorf("--store is required")
	}
	observedAt := time.Now().UTC().Add(-time.Duration(*observedAgoSeconds) * time.Second)

	var id string
	var err error
	switch *scenario {
	case "stack", "message":
		id, err = seedException(*store, *scenario, seedExceptionOpts{
			project: *projectSlug, service: *service, root: *root,
			file: *file, line: *line, function: *function,
			typ: *typ, value: *value, handled: *handled, level: *level,
			runID: *runID, release: *release, count: *count, observedAt: observedAt,
		})
	case "alert":
		id, err = seedAlert(*store, *projectSlug, *service, *kind, *value, observedAt)
	default:
		err = fmt.Errorf("unknown --scenario %q (want stack, message, or alert)", *scenario)
	}
	if err != nil {
		return err
	}
	fmt.Println(id)
	return nil
}

type seedExceptionOpts struct {
	project, service, root      string
	file                        string
	line                        int
	function, typ, value, level string
	handled                     bool
	runID, release              string
	count                       int64
	observedAt                  time.Time
}

func seedException(storePath, scenario string, o seedExceptionOpts) (string, error) {
	ex := stacktrace.Exception{
		Runtime: "go", Type: o.typ, Value: o.value, Parser: "seed", Level: o.level,
		Handled: &o.handled,
	}
	if scenario == "stack" {
		if o.root == "" || o.file == "" || o.line <= 0 {
			return "", fmt.Errorf("scenario=stack requires --root, --file, and --line")
		}
		abs := o.root + string(os.PathSeparator) + o.file
		ex.Frames = []stacktrace.Frame{
			{Function: o.function, AbsPath: abs, Filename: abs, Lineno: o.line},
		}
	}
	id := project.Identity{Slug: o.project, Service: o.service, Root: o.root, GitRoot: o.root}
	run := contextids.IDs{RunID: o.runID, Release: o.release}
	result, err := issues.RecordException(context.Background(), storePath, issues.DefaultWriterWait, ex, id, run, issues.RecordExceptionOptions{
		ObservedAt: o.observedAt, Count: o.count,
	})
	if err != nil {
		return "", err
	}
	return result.Issue.ID, nil
}

func seedAlert(storePath, projectSlug, service, rule, message string, observedAt time.Time) (string, error) {
	if rule == "" {
		rule = "cpu_spike"
	}
	var id string
	err := issues.WithWriter(context.Background(), storePath, issues.DefaultWriterWait, func(store *issues.Store) error {
		result, err := store.UpsertOccurrenceResult(issues.OccurrenceInput{
			ObservedAt: observedAt, Project: projectSlug, Service: service,
			Kind: "monitor.alert." + rule, Title: rule, Message: message, Severity: "warning",
		})
		if err != nil {
			return err
		}
		id = result.Issue.ID
		return nil
	})
	return id, err
}
