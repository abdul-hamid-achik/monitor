package devrun

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/monitor/internal/events"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/scrub"
)

func envEntry(env []string, name string) (string, int) {
	var val string
	n := 0
	for _, kv := range env {
		if k, v, _ := strings.Cut(kv, "="); k == name {
			val = v
			n++
		}
	}
	return val, n
}

func TestApplySDKExportsTheLaunchDirAndOnlyAppends(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	base := []string{
		"PATH=/usr/bin:/bin",
		events.EnvDir + "=/outer/launch",
		"NODE_OPTIONS=--max-old-space-size=4096",
		"PYTHONPATH=/app/lib",
	}

	plain, err := applySDK(append([]string(nil), base...), Options{}, "/state/events/launches/abc")
	if err != nil {
		t.Fatal(err)
	}
	if v, n := envEntry(plain.env, events.EnvDir); n != 1 || v != "/state/events/launches/abc" {
		t.Fatalf("%s = %q (x%d), want this launch's dir exactly once", events.EnvDir, v, n)
	}
	if v, _ := envEntry(plain.env, "NODE_OPTIONS"); v != "--max-old-space-size=4096" {
		t.Fatalf("NODE_OPTIONS changed without --probes: %q", v)
	}

	probed, err := applySDK(append([]string(nil), base...), Options{Probes: true}, "/state/events/launches/abc")
	if err != nil {
		t.Fatal(err)
	}
	node, _ := envEntry(probed.env, "NODE_OPTIONS")
	if !strings.HasPrefix(node, "--max-old-space-size=4096 --require \"") || !strings.HasSuffix(node, "auto.cjs\"") {
		t.Fatalf("NODE_OPTIONS = %q, want the app's options first, then --require <auto.cjs>", node)
	}
	bun, _ := envEntry(probed.env, "BUN_OPTIONS")
	if !strings.HasPrefix(bun, "--preload ") || !strings.HasSuffix(bun, "auto.cjs") {
		t.Fatalf("BUN_OPTIONS = %q", bun)
	}
	py, _ := envEntry(probed.env, "PYTHONPATH")
	parts := strings.Split(py, string(os.PathListSeparator))
	if len(parts) != 2 || filepath.Base(parts[0]) != "bootstrap" || parts[1] != "/app/lib" {
		t.Fatalf("PYTHONPATH = %q, want <bootstrap>:/app/lib", py)
	}
	if _, err := os.Stat(filepath.Join(parts[0], "sitecustomize.py")); err != nil {
		t.Fatalf("bootstrap not materialized: %v", err)
	}
}

// writeEventScript writes one monitor.event.v1 file into $MONITOR_EVENTS_DIR
// the way an SDK does (a dot-file renamed into place), from a shell child.
func writeEventScript(name, doc string) string {
	return `d="$MONITOR_EVENTS_DIR"; printf '%s' '` + doc + `' > "$d/.` + name + `.tmp" && mv "$d/.` + name + `.tmp" "$d/` + name + `.json"`
}

const sdkPanicEvent = `{"$schema":"monitor.event.v1","event_id":"evt-panic","kind":"uncaught","runtime":"go",` +
	`"error":{"type":"panic","value":"go-crash workload: intentional uncaught failure",` +
	`"frames":[{"function":"main.main","module":"main","filename":"/repo/examples/polyglot/go-crash/main.go","lineno":11}]},` +
	`"breadcrumbs":[{"timestamp":"2026-10-02T09:00:00Z","category":"job","message":"started batch 7"}],` +
	`"tags":{"region":"mx"},"sdk":{"name":"monitor.go","version":"0.1.0","mode":"explicit"}}`

const sdkLoggedEvent = `{"$schema":"monitor.event.v1","event_id":"evt-logged","kind":"logged","runtime":"node",` +
	`"error":{"type":"TypeError","value":"order has no card"},"sdk":{"name":"monitor.node","version":"0.1.0","mode":"auto"}}`

// The same crash seen by an SDK and printed on stderr is ONE occurrence
// carrying the SDK's context; an error only the SDK saw is its own issue.
func TestRunRecordsSDKEventsAndMergesThemWithStderr(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	script := strings.Join([]string{
		writeEventScript("0000000000000000001-1-evt-panic", sdkPanicEvent),
		writeEventScript("0000000000000000002-1-evt-logged", sdkLoggedEvent),
		"printf %s '" + goCrashPanicText + "' >&2",
		"exit 2",
	}, "; ")
	opts := baseOptions(t, []string{"sh", "-c", script})
	var stdout, stderr, banner bytes.Buffer
	opts.Stdout, opts.Stderr, opts.Banner = &stdout, &stderr, &banner

	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ExitCode != 2 || result.SDKEvents != 2 || len(result.NewIssueIDs) != 2 {
		t.Fatalf("result = %+v (stderr %q)", result, stderr.String())
	}

	store := openStoreForTest(t, opts.StorePath)
	list, err := store.List(issues.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("%d issues, want 2: %+v", len(list), list)
	}
	for _, issue := range list {
		occ, err := store.Occurrences(issue.ID, 0)
		if err != nil || len(occ) != 1 {
			t.Fatalf("%s: occurrences = %v %+v", issue.Title, err, occ)
		}
		if issue.OccurrenceCount != 1 {
			t.Fatalf("%s counted %d times: SDK and stderr were not merged", issue.Title, issue.OccurrenceCount)
		}
		if occ[0].Metadata["source"] != "sdk" {
			t.Fatalf("%s lost its SDK provenance: %+v", issue.Title, occ[0].Metadata)
		}
		if strings.HasPrefix(issue.Title, "panic") {
			if len(occ[0].Breadcrumbs) != 1 || occ[0].Tags["region"] != "mx" {
				t.Fatalf("SDK context not stored: %+v", occ[0])
			}
			if issue.Handled == nil || *issue.Handled {
				t.Fatalf("SDK's handled=false lost: %v", issue.Handled)
			}
		}
	}

	// The launch's directory is gone, and nothing leaked to the inbox.
	dir, _ := events.InboxDir()
	if left, _ := events.Pending(dir); len(left) != 0 {
		t.Fatalf("events handed to the inbox: %v", left)
	}
	launches := filepath.Join(filepath.Dir(dir), "launches")
	if entries, _ := os.ReadDir(launches); len(entries) != 0 {
		t.Fatalf("launch directories left behind: %v", entries)
	}
}

func TestRunWithoutDetectionExportsNoEventsDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	opts := baseOptions(t, []string{"sh", "-c", "env | grep ^MONITOR_EVENTS_DIR || true"})
	opts.NoIssues = true
	opts.Probes = true
	opts.Quiet = false
	var stdout, stderr, banner bytes.Buffer
	opts.Stdout, opts.Stderr, opts.Banner = &stdout, &stderr, &banner
	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), events.EnvDir) {
		t.Fatalf("--no-issues still exported %s", events.EnvDir)
	}
	if !strings.Contains(banner.String(), "--probes has no effect with --no-issues") {
		t.Fatalf("no note explaining --probes was ignored: %q", banner.String())
	}
}

// An SDK-only window (nothing on stderr) dedupes on the event id, so the
// same event delivered twice is one occurrence.
func TestDetectorSDKOnlyEventDedupesOnItsEventID(t *testing.T) {
	store := isolatedStore(t)
	ev, err := events.Decode([]byte(sdkLoggedEvent))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		det := newDetector(detectorOptions{storePath: store, id: testProjectIdentity(), launch: LaunchIDs{ID: "l", Root: "l"}})
		p, ok := events.Prepare(ev, scrub.New())
		if !ok {
			t.Fatal("nothing prepared")
		}
		det.observeEvent(context.Background(), p)
		det.flushAllPending(context.Background())
		if det.failedWrites != 0 {
			t.Fatalf("write failed")
		}
	}
	s := openStoreForTest(t, store)
	list, err := s.List(issues.ListOptions{})
	if err != nil || len(list) != 1 || list[0].OccurrenceCount != 1 {
		t.Fatalf("issues = %v %+v", err, list)
	}
}
