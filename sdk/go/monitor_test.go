package monitor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readEvents(t *testing.T, dir string) []Event {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var ev Event
		if err := json.Unmarshal(data, &ev); err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
	return out
}

func reset(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mu.Lock()
	opts, tags, breadcrumbs, windowCount, dropped = Options{}, map[string]string{}, nil, 0, 0
	mu.Unlock()
	Init(Options{Dir: dir, Service: "api", Release: "1.2.3"})
	return dir
}

func loadConfig() error {
	_, err := os.Open("/definitely/missing/config.yml")
	return fmt.Errorf("load config: %w", err)
}

func TestCaptureErrorWritesTheChainAndTheCallersStack(t *testing.T) {
	dir := reset(t)
	SetTag("region", "mx")
	AddBreadcrumb("db", "select users")

	id := CaptureError(loadConfig(), WithTags(map[string]string{"attempt": "2"}))
	if id == "" {
		t.Fatal("nothing written")
	}
	evs := readEvents(t, dir)
	if len(evs) != 1 {
		t.Fatalf("%d events, want 1", len(evs))
	}
	ev := evs[0]
	if ev.Schema != "monitor.event.v1" || ev.Kind != "captured" || ev.EventID != id || ev.Runtime != "go" || ev.Service != "api" || ev.Release != "1.2.3" {
		t.Fatalf("event = %+v", ev)
	}
	if ev.Handled == nil || !*ev.Handled || ev.Level != LevelError {
		t.Fatalf("disposition = %v %q", ev.Handled, ev.Level)
	}
	if ev.Error.Type != "*fmt.wrapError" || ev.Error.Cause == nil || ev.Error.Cause.Type != "*fs.PathError" {
		t.Fatalf("chain = %+v", ev.Error)
	}
	last := ev.Error.Frames[len(ev.Error.Frames)-1]
	if !strings.HasSuffix(last.Function, "TestCaptureErrorWritesTheChainAndTheCallersStack") || filepath.Base(last.Filename) != "monitor_test.go" {
		t.Fatalf("crash frame = %+v, want this test", last)
	}
	if last.Module != "github.com/abdul-hamid-achik/monitor/sdk/go" {
		t.Fatalf("module = %q", last.Module)
	}
	if ev.Tags["region"] != "mx" || ev.Tags["attempt"] != "2" || len(ev.Breadcrumbs) != 1 {
		t.Fatalf("context = %+v %+v", ev.Tags, ev.Breadcrumbs)
	}
}

func explode() {
	var m map[string]int
	m["x"] = 1 // assignment to entry in nil map
}

func TestRecoverRecordsThePanicSiteAndPanicsAgain(t *testing.T) {
	dir := reset(t)
	func() {
		defer func() {
			if v := recover(); v == nil {
				t.Fatal("Recover swallowed the panic")
			}
		}()
		defer Recover()
		explode()
	}()
	evs := readEvents(t, dir)
	if len(evs) != 1 {
		t.Fatalf("%d events, want 1", len(evs))
	}
	ev := evs[0]
	if ev.Kind != "uncaught" || ev.Handled == nil || *ev.Handled || ev.Level != LevelFatal || ev.Error.Type != "panic" {
		t.Fatalf("event = %+v", ev)
	}
	if !strings.Contains(ev.Error.Value, "nil map") {
		t.Fatalf("value = %q", ev.Error.Value)
	}
	// The stack ends where Go's own panic trace ends: in the runtime
	// function that panicked, with the program's frame right before it.
	var app Frame
	for _, f := range ev.Error.Frames {
		if f.Module != "runtime" && !strings.HasPrefix(f.Module, "internal/") {
			app = f
		}
		if f.Function == "runtime.gopanic" || strings.Contains(f.Function, "monitor.Recover") || strings.Contains(f.Function, "capturePanic") {
			t.Fatalf("panic machinery leaked into frames: %+v", ev.Error.Frames)
		}
	}
	if !strings.HasSuffix(app.Function, ".explode") {
		t.Fatalf("last program frame = %+v, want explode", app)
	}
}

func TestRecoverAndContinueSwallows(t *testing.T) {
	dir := reset(t)
	func() {
		defer RecoverAndContinue()
		panic(errors.New("handler blew up"))
	}()
	if evs := readEvents(t, dir); len(evs) != 1 || evs[0].Error.Value != "handler blew up" {
		t.Fatalf("events = %+v", evs)
	}
}

func TestMessagesBeforeSendAndRateLimit(t *testing.T) {
	dir := reset(t)
	if id := CaptureMessage("  ", LevelInfo); id != "" {
		t.Fatal("blank message written")
	}
	if id := CaptureMessage("cache warmed", ""); id == "" {
		t.Fatal("message not written")
	}
	mu.Lock()
	opts.BeforeSend = func(ev *Event) *Event {
		if ev.Message == "drop me" {
			return nil
		}
		return ev
	}
	mu.Unlock()
	if id := CaptureMessage("drop me", LevelWarning); id != "" {
		t.Fatal("BeforeSend did not drop")
	}
	for i := 0; i < rateMax+5; i++ {
		CaptureError(errors.New("loop"))
	}
	evs := readEvents(t, dir)
	if len(evs) != rateMax {
		t.Fatalf("%d events, want the rate limit %d", len(evs), rateMax)
	}
	if evs[0].Kind != "message" || evs[0].Level != LevelInfo {
		t.Fatalf("first event = %+v", evs[0])
	}
}

func TestFallsBackToTheInboxWhenTheLaunchDirIsGone(t *testing.T) {
	reset(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONITOR_EVENTS_DIR", filepath.Join(blocker, "launch")) // not creatable
	mu.Lock()
	opts.Dir = ""
	mu.Unlock()
	if id := CaptureMessage("after the launch ended", LevelInfo); id == "" {
		t.Fatal("nothing written")
	}
	inbox := filepath.Join(state, "monitor", "events", "inbox")
	if evs := readEvents(t, inbox); len(evs) != 1 {
		t.Fatalf("inbox holds %d events", len(evs))
	}
	info, err := os.Stat(inbox)
	if err != nil || info.Mode().Perm() != fs.FileMode(0o700) {
		t.Fatalf("inbox mode = %v %v", info.Mode().Perm(), err)
	}
}
