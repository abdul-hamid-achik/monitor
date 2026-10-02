package events

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/project"
	"github.com/abdul-hamid-achik/monitor/internal/scrub"
	"github.com/abdul-hamid-achik/monitor/internal/stacktrace"
)

// fixtureRoot makes a git working tree holding the fixture apps, so frames
// resolve in-app exactly as they did when the fixtures were captured.
func fixtureRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for src, dst := range map[string]string{"app.js.txt": "app.js", "app.py.txt": "app.py", "main.go.txt": "main.go"} {
		data, err := os.ReadFile(filepath.Join("testdata", src))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dst), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func fixture(t *testing.T, name, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(data), "{{ROOT}}", root)
}

// sdkView is what the fixture apps' own hooks saw: the runtime's text for
// the outer error and for its cause, as an SDK sends them.
type sdkView struct {
	Outer, Cause, Name, CName, Msg, CMsg string
}

func loadView(t *testing.T, name, root string) sdkView {
	t.Helper()
	var raw map[string]string
	if err := json.Unmarshal([]byte(fixture(t, name, root)), &raw); err != nil {
		t.Fatal(err)
	}
	return sdkView{Outer: raw["outer"], Cause: raw["cause"], Name: raw["name"], CName: raw["cname"], Msg: raw["msg"], CMsg: raw["cmsg"]}
}

func uncaughtEvent(runtime string, v sdkView, root string) Event {
	return Event{
		Schema: Schema, EventID: "e1", Kind: KindUncaught, Runtime: runtime, Cwd: root, PID: 42,
		Timestamp: time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC),
		Error: &Error{Type: v.Name, Value: v.Msg, Stack: v.Outer,
			Cause: &Error{Type: v.CName, Value: v.CMsg, Stack: v.Cause}},
	}
}

func TestDecodeRejectsWhatCanNeverBeRecorded(t *testing.T) {
	cases := map[string]string{
		"wrong schema": `{"$schema":"monitor.probe.v0","kind":"uncaught","message":"x"}`,
		"unknown kind": `{"$schema":"monitor.event.v1","kind":"weird","message":"x"}`,
		"nothing":      `{"$schema":"monitor.event.v1","kind":"captured","error":{"type":"  "}}`,
		"not json":     `{"$schema":`,
	}
	for name, doc := range cases {
		if _, err := Decode([]byte(doc)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := Decode(make([]byte, MaxFileBytes+1)); !errors.Is(err, ErrInvalid) {
		t.Errorf("oversized: err = %v, want ErrInvalid", err)
	}
}

func TestDecodeAppliesBounds(t *testing.T) {
	deep := &Error{Type: "E0"}
	cur := deep
	for i := 1; i < 20; i++ {
		cur.Cause = &Error{Type: "E"}
		cur = cur.Cause
	}
	ev := Event{Schema: Schema, Kind: "CAPTURED", Level: "warn", EventID: "../../etc/passwd",
		Error: &Error{Type: "T", Value: strings.Repeat("é", maxValueBytes), Stack: strings.Repeat("s", maxStackBytes+10), Cause: deep}}
	data, _ := json.Marshal(ev)
	got, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindCaptured || got.Level != LevelWarning {
		t.Fatalf("kind/level not normalized: %q %q", got.Kind, got.Level)
	}
	if got.EventID != "" {
		t.Fatalf("unsafe event id kept: %q", got.EventID)
	}
	if len(got.Error.Value) > maxValueBytes || !strings.HasSuffix(got.Error.Value, "é") {
		t.Fatalf("value clipped badly: %d bytes", len(got.Error.Value))
	}
	if len(got.Error.Stack) != maxStackBytes {
		t.Fatalf("stack is %d bytes, want %d", len(got.Error.Stack), maxStackBytes)
	}
	depth := 0
	for c := got.Error; c != nil; c = c.Cause {
		depth++
	}
	if depth != maxCauseDepth {
		t.Fatalf("chain depth %d, want %d", depth, maxCauseDepth)
	}
}

// The central promise: the same crash seen by an SDK and printed on
// stderr groups into ONE issue, so `monitor run --probes` can merge them.
func TestSDKEventFingerprintsLikeTheSameCrashOnStderr(t *testing.T) {
	root := fixtureRoot(t)
	for _, tc := range []struct {
		runtime, stderr, view string
		outerType, causeType  string
	}{
		{"node", "node-stderr.txt", "node-event.json", "Error", "SyntaxError"},
		{"python", "py-stderr.txt", "py-event.json", "RuntimeError", "json.decoder.JSONDecodeError"},
	} {
		t.Run(tc.runtime, func(t *testing.T) {
			parsed := stacktrace.Detect(fixture(t, tc.stderr, root))
			if len(parsed) != 1 {
				t.Fatalf("stderr fixture parsed into %d exceptions, want 1", len(parsed))
			}
			fromStderr := *parsed[0]
			stacktrace.ApplyGitRoot(&fromStderr, root)

			ev := uncaughtEvent(tc.runtime, loadView(t, tc.view, root), root)
			fromSDK, ok := ToException(ev)
			if !ok {
				t.Fatal("ToException: nothing to record")
			}
			stacktrace.ApplyGitRoot(&fromSDK, root)

			if fromSDK.Type != tc.outerType || len(fromSDK.Chained) != 1 || fromSDK.Chained[0].Type != tc.causeType {
				t.Fatalf("SDK exception = %s caused by %+v", fromSDK.Type, fromSDK.Chained)
			}
			if fromSDK.Handled == nil || *fromSDK.Handled || fromSDK.Level != LevelFatal || fromSDK.Runtime != tc.runtime {
				t.Fatalf("disposition = handled %v level %q runtime %q", fromSDK.Handled, fromSDK.Level, fromSDK.Runtime)
			}
			a := issues.FingerprintV2Exception(fromStderr, "proj")
			b := issues.FingerprintV2Exception(fromSDK, "proj")
			if a != b {
				t.Fatalf("fingerprints differ:\n stderr %s %+v\n sdk    %s %+v", a, fromStderr.Frames, b, fromSDK.Frames)
			}
		})
	}
}

// The Go SDK sends structured frames instead of text; a panic it recorded
// through Recover still groups with the trace Go printed on stderr.
func TestGoSDKPanicFingerprintsLikeTheStderrPanic(t *testing.T) {
	root := fixtureRoot(t)
	parsed := stacktrace.Detect(fixture(t, "go-stderr.txt", root))
	if len(parsed) != 1 {
		t.Fatalf("stderr fixture parsed into %d exceptions, want 1", len(parsed))
	}
	fromStderr := *parsed[0]
	stacktrace.ApplyGitRoot(&fromStderr, root)

	ev, err := Decode([]byte(fixture(t, "go-event.json", root)))
	if err != nil {
		t.Fatal(err)
	}
	fromSDK, ok := ToException(ev)
	if !ok {
		t.Fatal("nothing to record")
	}
	stacktrace.ApplyGitRoot(&fromSDK, root)
	if fromSDK.Type != "panic" || fromSDK.Runtime != "go" || fromSDK.Level != LevelFatal {
		t.Fatalf("SDK exception = %+v", fromSDK)
	}
	if a, b := issues.FingerprintV2Exception(fromStderr, "proj"), issues.FingerprintV2Exception(fromSDK, "proj"); a != b {
		t.Fatalf("fingerprints differ:\n stderr %+v\n sdk    %+v", fromStderr, fromSDK)
	}
}

func TestStructuredFramesAndMessages(t *testing.T) {
	ev := Event{Schema: Schema, Kind: KindCaptured, Runtime: "go", Error: &Error{
		Type: "*fs.PathError", Value: "open config.yml: no such file or directory\nsecond line",
		Frames: []Frame{
			{Function: "main.main", Filename: "/repo/main.go", Lineno: 12},
			{Function: "main.load", Module: "main", Filename: "/repo/load.go", Lineno: 30},
		},
	}}
	ex, ok := ToException(ev)
	if !ok {
		t.Fatal("no exception")
	}
	if ex.Parser != ParserSDK || len(ex.Frames) != 2 || ex.Frames[1].Function != "main.load" || ex.Frames[1].AbsPath != "/repo/load.go" {
		t.Fatalf("frames = %+v", ex.Frames)
	}
	if ex.Value != "open config.yml: no such file or directory" {
		t.Fatalf("value = %q, want its first line", ex.Value)
	}
	if ex.Handled == nil || !*ex.Handled || ex.Level != LevelError {
		t.Fatalf("captured error should be handled/error, got %v %q", ex.Handled, ex.Level)
	}

	msg, ok := ToException(Event{Schema: Schema, Kind: KindMessage, Message: "cache warmed in 3s"})
	if !ok || msg.Value != "cache warmed in 3s" || msg.Level != LevelInfo || len(msg.Frames) != 0 {
		t.Fatalf("message event = %+v", msg)
	}
}

func TestPrepareScrubsEverythingPersisted(t *testing.T) {
	secret := "sk_" + "live_" + "4eC39HqLyjWDarjtT1zdp7dc"
	scrubber := scrub.New()
	ev := Event{Schema: Schema, EventID: "abc", Kind: KindCaptured, Environment: "staging", Release: "1.2.3",
		Error:       &Error{Type: "AuthError", Value: "bad key " + secret},
		Tags:        map[string]string{"key": secret, "region": "mx"},
		Breadcrumbs: []Breadcrumb{{Message: "called stripe with " + secret, Category: "http"}},
		SDK:         SDK{Name: "monitorcli.node", Version: "0.1.0", Mode: "explicit"},
	}
	p, ok := Prepare(ev, scrubber)
	if !ok {
		t.Fatal("nothing prepared")
	}
	persisted, _ := json.Marshal(p.Options)
	if strings.Contains(p.Exception.Value, secret) || strings.Contains(string(persisted), secret) {
		t.Fatalf("secret survived: %s / %s", p.Exception.Value, persisted)
	}
	if p.Options.Tags["environment"] != "staging" || p.Options.Tags["region"] != "mx" || p.Run.Release != "1.2.3" {
		t.Fatalf("context lost: %+v %+v", p.Options.Tags, p.Run)
	}
	if p.Options.DedupeKey != "sdk:abc" || p.Options.Metadata["sdk"] != "monitorcli.node/0.1.0" || p.Options.Metadata["sdk_mode"] != "explicit" {
		t.Fatalf("provenance = %q %+v", p.Options.DedupeKey, p.Options.Metadata)
	}
}

func TestIngestRecordsDedupesAndQuarantines(t *testing.T) {
	root := fixtureRoot(t)
	inbox := filepath.Join(t.TempDir(), "inbox")
	store := filepath.Join(t.TempDir(), "issues.veclite")
	ctx := context.Background()

	// An empty inbox never creates the store.
	if res, err := Ingest(ctx, IngestOptions{Dir: inbox, StorePath: store}); err != nil || res.Recorded+res.Rejected+res.Remaining != 0 {
		t.Fatalf("empty ingest = %+v %v", res, err)
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Fatalf("empty ingest touched the store: %v", err)
	}

	ev := uncaughtEvent("node", loadView(t, "node-event.json", root), root)
	ev.Breadcrumbs = []Breadcrumb{{Message: "loading config", Category: "app"}}
	ev.Tags = map[string]string{"region": "mx"}
	if _, err := Write(inbox, ev); err != nil {
		t.Fatal(err)
	}
	// One broken file next to it.
	if err := os.WriteFile(filepath.Join(inbox, "0000000000000000001-1-bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A symlink is never followed, even to a valid event.
	valid, _ := Pending(inbox)
	if err := os.Symlink(valid[len(valid)-1], filepath.Join(inbox, "0000000000000000002-1-link.json")); err != nil {
		t.Fatal(err)
	}

	res, err := Ingest(ctx, IngestOptions{Dir: inbox, StorePath: store})
	if err != nil {
		t.Fatal(err)
	}
	if res.Recorded != 1 || res.Deduped != 0 || res.Rejected != 2 || res.Remaining != 0 || len(res.IssueIDs) != 1 {
		t.Fatalf("ingest = %+v", res)
	}
	if left, _ := Pending(inbox); len(left) != 0 {
		t.Fatalf("files left behind: %v", left)
	}
	if q, _ := os.ReadDir(filepath.Join(inbox, rejectedDir)); len(q) != 2 {
		t.Fatalf("quarantine holds %d files, want 2", len(q))
	}

	// The same event delivered again (an SDK retry, or a second drainer
	// racing the first) folds into the existing occurrence.
	ev.Timestamp = ev.Timestamp.Add(time.Second)
	if _, err := Write(inbox, ev); err != nil {
		t.Fatal(err)
	}
	again, err := Ingest(ctx, IngestOptions{Dir: inbox, StorePath: store})
	if err != nil || again.Recorded != 0 || again.Deduped != 1 {
		t.Fatalf("re-delivery = %+v %v", again, err)
	}

	s, err := issues.OpenReadOnly(store)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	issue, err := s.Get(res.IssueIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	want := project.Resolve(project.Hints{Dir: root, PID: 1}).Slug
	if issue.Project != want || issue.Culprit == nil || issue.Culprit.File != "app.js" || issue.Culprit.Line != 1 {
		t.Fatalf("issue = project %q culprit %+v", issue.Project, issue.Culprit)
	}
	occ, err := s.Occurrences(issue.ID, 0)
	if err != nil || len(occ) != 1 {
		t.Fatalf("occurrences = %v %+v", err, occ)
	}
	if len(occ[0].Breadcrumbs) != 1 || occ[0].Tags["region"] != "mx" || occ[0].Metadata["event_kind"] != KindUncaught {
		t.Fatalf("SDK context not stored: %+v", occ[0])
	}
}
