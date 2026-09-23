package stacktrace

import (
	"os"
	"path/filepath"
	"testing"
)

// withStateHome points $XDG_STATE_HOME at a fresh temp dir for the duration
// of the test, so CheckpointPath/checkpointDir never touch the real
// developer machine's state directory.
func withStateHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	return dir
}

func TestCheckpointPathIsStableAndScopedUnderStateHome(t *testing.T) {
	state := withStateHome(t)
	p1, err := CheckpointPath("/repo/app.log")
	if err != nil {
		t.Fatalf("CheckpointPath: %v", err)
	}
	p2, err := CheckpointPath("/repo/app.log")
	if err != nil {
		t.Fatalf("CheckpointPath: %v", err)
	}
	if p1 != p2 {
		t.Errorf("CheckpointPath not stable: %q vs %q", p1, p2)
	}
	if filepath.Dir(p1) != filepath.Join(state, "monitor", "parse") {
		t.Errorf("checkpoint dir = %q, want under %q", filepath.Dir(p1), filepath.Join(state, "monitor", "parse"))
	}
	other, err := CheckpointPath("/repo/other.log")
	if err != nil {
		t.Fatalf("CheckpointPath: %v", err)
	}
	if other == p1 {
		t.Errorf("different paths hashed to the same checkpoint file: %q", p1)
	}
}

func TestCheckpointDirIsPrivate(t *testing.T) {
	withStateHome(t)
	dir, err := checkpointDir()
	if err != nil {
		t.Fatalf("checkpointDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat checkpoint dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("checkpoint dir mode = %v, want 0700", perm)
	}
}

func TestLoadCheckpointMissingReturnsZeroValue(t *testing.T) {
	withStateHome(t)
	cp, err := LoadCheckpoint("/repo/never-seen.log")
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if cp != (Checkpoint{}) {
		t.Errorf("checkpoint = %+v, want zero value", cp)
	}
}

func TestSaveThenLoadCheckpointRoundTrips(t *testing.T) {
	withStateHome(t)
	want := Checkpoint{Inode: 4410, Size: 88412, Offset: 88412}
	if err := SaveCheckpoint("/repo/app.log", want); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	got, err := LoadCheckpoint("/repo/app.log")
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if got != want {
		t.Errorf("checkpoint = %+v, want %+v", got, want)
	}
}

func TestSaveCheckpointOverwritesPrevious(t *testing.T) {
	withStateHome(t)
	if err := SaveCheckpoint("/repo/app.log", Checkpoint{Inode: 1, Size: 100, Offset: 100}); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	want := Checkpoint{Inode: 1, Size: 200, Offset: 200}
	if err := SaveCheckpoint("/repo/app.log", want); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	got, err := LoadCheckpoint("/repo/app.log")
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if got != want {
		t.Errorf("checkpoint = %+v, want %+v", got, want)
	}
}

func TestLoadCheckpointCorruptFileIsTreatedAsMissing(t *testing.T) {
	withStateHome(t)
	path, err := CheckpointPath("/repo/app.log")
	if err != nil {
		t.Fatalf("CheckpointPath: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt checkpoint: %v", err)
	}
	cp, err := LoadCheckpoint("/repo/app.log")
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if cp != (Checkpoint{}) {
		t.Errorf("checkpoint = %+v, want zero value for corrupt file", cp)
	}
}

func TestResolveOffsetFromStartAlwaysZero(t *testing.T) {
	withStateHome(t)
	if err := SaveCheckpoint("/repo/app.log", Checkpoint{Inode: 1, Size: 100, Offset: 100}); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	start, cp := ResolveOffset("/repo/app.log", 1, 200, true)
	if start != 0 {
		t.Errorf("start = %d, want 0 (--from-start)", start)
	}
	if cp.Offset != 0 {
		t.Errorf("cp.Offset = %d, want 0", cp.Offset)
	}
	if cp.Inode != 1 || cp.Size != 200 {
		t.Errorf("cp = %+v, want current inode/size stamped", cp)
	}
}

func TestResolveOffsetNoCheckpointStartsAtZero(t *testing.T) {
	withStateHome(t)
	start, cp := ResolveOffset("/repo/never-seen.log", 7, 500, false)
	if start != 0 {
		t.Errorf("start = %d, want 0", start)
	}
	if cp.Inode != 7 || cp.Size != 500 || cp.Offset != 0 {
		t.Errorf("cp = %+v, want {7 500 0}", cp)
	}
}

func TestResolveOffsetResumesWhenInodeAndSizeMatch(t *testing.T) {
	withStateHome(t)
	if err := SaveCheckpoint("/repo/app.log", Checkpoint{Inode: 4410, Size: 1000, Offset: 700}); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	// File grew from 1000 to 1500 bytes, same inode: resume at 700.
	start, cp := ResolveOffset("/repo/app.log", 4410, 1500, false)
	if start != 700 {
		t.Errorf("start = %d, want 700 (resume)", start)
	}
	if cp.Offset != 700 || cp.Inode != 4410 || cp.Size != 1500 {
		t.Errorf("cp = %+v, want {4410 1500 700}", cp)
	}
}

func TestResolveOffsetResetsOnInodeChange(t *testing.T) {
	withStateHome(t)
	if err := SaveCheckpoint("/repo/app.log", Checkpoint{Inode: 4410, Size: 1000, Offset: 700}); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	// Same path, new inode (logrotate): reset to 0 even though the new
	// file is bigger than the old offset.
	start, cp := ResolveOffset("/repo/app.log", 9999, 2000, false)
	if start != 0 {
		t.Errorf("start = %d, want 0 (rotation)", start)
	}
	if cp.Offset != 0 || cp.Inode != 9999 {
		t.Errorf("cp = %+v, want inode 9999 offset 0", cp)
	}
}

func TestResolveOffsetResetsOnTruncation(t *testing.T) {
	withStateHome(t)
	if err := SaveCheckpoint("/repo/app.log", Checkpoint{Inode: 4410, Size: 1000, Offset: 900}); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	// Same inode, but current size (500) is smaller than the saved offset
	// (900): the file was truncated (a restarted service overwriting its
	// own log) rather than merely growing further.
	start, cp := ResolveOffset("/repo/app.log", 4410, 500, false)
	if start != 0 {
		t.Errorf("start = %d, want 0 (truncation)", start)
	}
	if cp.Offset != 0 {
		t.Errorf("cp.Offset = %d, want 0", cp.Offset)
	}
}

func TestStatInodeReportsSizeAndNonZeroInode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	inode, size, err := StatInode(f)
	if err != nil {
		t.Fatalf("StatInode: %v", err)
	}
	if size != 5 {
		t.Errorf("size = %d, want 5", size)
	}
	if inode == 0 {
		t.Errorf("inode = 0, want a real inode number")
	}
}

func TestSettleWindowMatchesDefaultIdle(t *testing.T) {
	// Not an arbitrary duplicate constant: SettleWindow's whole rationale
	// (see its doc comment) is that a settled writer stays quiet for at
	// least one Joiner idle window, so the two must track each other.
	if SettleWindow != DefaultIdle {
		t.Errorf("SettleWindow = %v, want it to equal DefaultIdle (%v)", SettleWindow, DefaultIdle)
	}
}

func TestHashBlockDeterministicAndDistinct(t *testing.T) {
	h1 := HashBlock("panic: boom\nmain.main()\n")
	h2 := HashBlock("panic: boom\nmain.main()\n")
	if h1 != h2 {
		t.Errorf("HashBlock not deterministic: %q vs %q", h1, h2)
	}
	h3 := HashBlock("panic: boom\nmain.main2()\n")
	if h1 == h3 {
		t.Errorf("HashBlock collided for different text")
	}
	if len(h1) != 64 {
		t.Errorf("len(HashBlock) = %d, want 64 (hex sha256)", len(h1))
	}
}
