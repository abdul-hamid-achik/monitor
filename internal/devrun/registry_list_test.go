package devrun

import (
	"os"
	"testing"
	"time"
)

func TestListRegistryEntriesAcrossProjects(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if got := ListRegistryEntries(); len(got) != 0 {
		t.Fatalf("empty registry listed %v", got)
	}
	older := RegistryEntry{Schema: RegistrySchema, LaunchID: "L1", PID: os.Getpid(), Name: "api", Project: "shop",
		StartedAt: time.Now().Add(-time.Hour), Scan: "stderr"}
	newer := RegistryEntry{Schema: RegistrySchema, LaunchID: "L2", PID: 999999, Name: "worker", Project: "billing",
		StartedAt: time.Now(), Scan: "both"}
	for _, e := range []RegistryEntry{older, newer} {
		if _, err := WriteRegistryEntry(e); err != nil {
			t.Fatal(err)
		}
	}
	got := ListRegistryEntries()
	if len(got) != 2 {
		t.Fatalf("listed %d entries, want 2: %+v", len(got), got)
	}
	if got[0].Entry.LaunchID != "L2" || got[1].Entry.LaunchID != "L1" {
		t.Fatalf("order = %s, %s; want newest first", got[0].Entry.LaunchID, got[1].Entry.LaunchID)
	}
	if !got[1].Alive {
		t.Fatal("this test process's own pid should be alive")
	}
}
