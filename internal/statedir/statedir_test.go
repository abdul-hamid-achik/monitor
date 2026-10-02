package statedir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootFollowsXDGStateHome(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	got, err := Path("events", "inbox")
	if err != nil || got != filepath.Join(base, "monitor", "events", "inbox") {
		t.Fatalf("Path = %q %v", got, err)
	}
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Fatalf("Path created %s", got)
	}
	if err := Ensure(got); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(got); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("Ensure mode = %v %v", info.Mode().Perm(), err)
	}
	t.Setenv("XDG_STATE_HOME", "relative/dir")
	if _, err := Root(); err == nil {
		t.Fatal("relative XDG_STATE_HOME accepted")
	}
}
