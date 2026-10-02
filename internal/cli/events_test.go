package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/abdul-hamid-achik/monitor/internal/events"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
)

func writeInboxEvent(t *testing.T, id string) string {
	t.Helper()
	inbox, err := events.InboxDir()
	if err != nil {
		t.Fatal(err)
	}
	path, err := events.Write(inbox, events.Event{
		EventID: id, Kind: events.KindCaptured, Runtime: "python", Cwd: t.TempDir(), PID: 7,
		Error: &events.Error{Type: "ValueError", Value: "bad config " + id},
		SDK:   events.SDK{Name: "monitor.python", Version: "0.1.0", Mode: "explicit"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEventsDrainRecordsTheInboxIntoTheStore(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	store := filepath.Join(t.TempDir(), "issues.veclite")
	file := writeInboxEvent(t, "e1")

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = old }()
	cmd := newEventsCmd()
	cmd.SetArgs([]string{"drain", "--store", store, "--json"})
	runErr := cmd.Execute()
	_ = w.Close()
	out, _ := io.ReadAll(r)
	os.Stdout = old
	if runErr != nil {
		t.Fatalf("events drain: %v", runErr)
	}
	var res events.IngestResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("drain --json: %v\n%s", err, out)
	}
	if res.Recorded != 1 || len(res.IssueIDs) != 1 {
		t.Fatalf("drain = %+v", res)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("drained file still in the inbox: %v", err)
	}
	s, err := issues.OpenReadOnly(store)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Get(res.IssueIDs[0]); err != nil {
		t.Fatal(err)
	}
}

// A read pointed at any store but the default one (a test, a spec, a
// scratch store) never pulls the user's inbox into it.
func TestAutomaticDrainOnlyTargetsTheDefaultStore(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	file := writeInboxEvent(t, "e2")
	ctx := context.Background()

	t.Setenv(issues.StorePathEnv, filepath.Join(t.TempDir(), "scratch.veclite"))
	drainInboxIntoDefaultStore(ctx, "")
	drainInboxIntoDefaultStore(ctx, "/some/other.veclite")
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("an overridden store drained the inbox: %v", err)
	}

	t.Setenv(issues.StorePathEnv, "")
	drainInboxIntoDefaultStore(ctx, "")
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("the default store did not drain the inbox: %v", err)
	}
	def, err := issues.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	s, err := issues.OpenReadOnly(def)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	list, err := s.List(issues.ListOptions{})
	if err != nil || len(list) != 1 {
		t.Fatalf("default store = %v %+v", err, list)
	}
}
