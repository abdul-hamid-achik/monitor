package stacktrace

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// openTestFile creates a real file with the given content and returns an
// open read handle to it (closed on test cleanup), for tests that exercise
// ResolveOffset's real stat-based (dev/inode/mtime/content) reset rule --
// unlike the pre-CC-1/CC-2 API, ResolveOffset now stats and reads an actual
// *os.File rather than taking caller-supplied inode/size values.
func openTestFile(t *testing.T, path, content string) *os.File {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

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

func TestResolveOffsetFromStartAlwaysZeroButKeepsGeneration(t *testing.T) {
	withStateHome(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	f := openTestFile(t, path, strings.Repeat("x", 200))
	dev, inode, _, _, err := statFile(f)
	if err != nil {
		t.Fatalf("statFile: %v", err)
	}
	if err := SaveCheckpointByKey(CheckpointKey{Dev: dev, Inode: inode}, Checkpoint{Dev: dev, Inode: inode, Size: 100, Offset: 100, Generation: 3}); err != nil {
		t.Fatalf("SaveCheckpointByKey: %v", err)
	}
	start, cp, err := ResolveOffset(f, path, true)
	if err != nil {
		t.Fatalf("ResolveOffset: %v", err)
	}
	if start != 0 {
		t.Errorf("start = %d, want 0 (--from-start)", start)
	}
	if cp.Offset != 0 {
		t.Errorf("cp.Offset = %d, want 0", cp.Offset)
	}
	if cp.Dev != dev || cp.Inode != inode || cp.Size != 200 {
		t.Errorf("cp = %+v, want current dev/inode/size stamped", cp)
	}
	if cp.Generation != 3 {
		t.Errorf("cp.Generation = %d, want 3: --from-start replays the SAME generation, it does not start a new one", cp.Generation)
	}
}

func TestResolveOffsetNoCheckpointStartsAtZero(t *testing.T) {
	withStateHome(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "never-seen.log")
	f := openTestFile(t, path, strings.Repeat("y", 500))
	start, cp, err := ResolveOffset(f, path, false)
	if err != nil {
		t.Fatalf("ResolveOffset: %v", err)
	}
	if start != 0 {
		t.Errorf("start = %d, want 0", start)
	}
	if cp.Size != 500 || cp.Offset != 0 {
		t.Errorf("cp = %+v, want size 500 offset 0", cp)
	}
}

func TestResolveOffsetResumesOnGrowthWithMatchingContent(t *testing.T) {
	withStateHome(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	full := strings.Repeat("a", 700) + strings.Repeat("b", 800) // grown to 1500 bytes
	f := openTestFile(t, path, full)
	dev, inode, _, mtimeNs, err := statFile(f)
	if err != nil {
		t.Fatalf("statFile: %v", err)
	}
	prev := Checkpoint{Dev: dev, Inode: inode, Size: 1000, Offset: 700, MtimeNs: mtimeNs - int64(time.Second), PrefixHash: PrefixHash(f, 700)}
	if err := SaveCheckpointByKey(CheckpointKey{Dev: dev, Inode: inode}, prev); err != nil {
		t.Fatalf("SaveCheckpointByKey: %v", err)
	}
	start, cp, err := ResolveOffset(f, path, false)
	if err != nil {
		t.Fatalf("ResolveOffset: %v", err)
	}
	if start != 700 {
		t.Errorf("start = %d, want 700 (resume: same dev/inode, grew, matching prefix)", start)
	}
	if cp.Offset != 700 || cp.Dev != dev || cp.Inode != inode || cp.Size != 1500 {
		t.Errorf("cp = %+v, want offset 700, current dev/inode, size 1500", cp)
	}
	if cp.Generation != 0 {
		t.Errorf("cp.Generation = %d, want 0 (no reset)", cp.Generation)
	}
}

// TestResolveOffsetByKeyResumesAcrossRename is CC-2's core claim: logrotate's
// default rotation (`mv app.log app.log.1`) preserves the file's dev/inode,
// so the SAME checkpoint (keyed by dev/inode, not path) resumes cleanly
// under the new name instead of replaying from 0 and double-counting every
// occurrence already recorded under the old path.
func TestResolveOffsetByKeyResumesAcrossRename(t *testing.T) {
	withStateHome(t)
	dir := t.TempDir()
	origPath := filepath.Join(dir, "app.log")
	f := openTestFile(t, origPath, strings.Repeat("a", 700)+strings.Repeat("b", 300))
	dev, inode, _, mtimeNs, err := statFile(f)
	if err != nil {
		t.Fatalf("statFile: %v", err)
	}
	prev := Checkpoint{Dev: dev, Inode: inode, Size: 700, Offset: 700, MtimeNs: mtimeNs, PrefixHash: PrefixHash(f, 700)}
	if err := SaveCheckpointByKey(CheckpointKey{Dev: dev, Inode: inode}, prev); err != nil {
		t.Fatalf("SaveCheckpointByKey: %v", err)
	}
	_ = f.Close()

	rotatedPath := filepath.Join(dir, "app.log.1")
	if err := os.Rename(origPath, rotatedPath); err != nil {
		t.Fatalf("rename: %v", err)
	}
	f2, err := os.Open(rotatedPath)
	if err != nil {
		t.Fatalf("open rotated file: %v", err)
	}
	defer f2.Close()

	start, cp, err := ResolveOffset(f2, rotatedPath, false)
	if err != nil {
		t.Fatalf("ResolveOffset: %v", err)
	}
	if start != 700 {
		t.Errorf("start = %d, want 700 (resume across a rename that preserves dev/inode)", start)
	}
	if cp.Generation != 0 {
		t.Errorf("cp.Generation = %d, want 0 (renamed, not rewritten -- no reset)", cp.Generation)
	}
}

// TestResolveOffsetByKeyFallsBackToLegacyPathKeyedCheckpoint is CC-2's
// migration case: a checkpoint saved by a pre-CC-2 monitor binary (path-
// keyed, no Dev field) must still resume, not silently replay a whole log
// from 0 the first time a --record run happens after upgrading.
func TestResolveOffsetByKeyFallsBackToLegacyPathKeyedCheckpoint(t *testing.T) {
	withStateHome(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	f := openTestFile(t, path, strings.Repeat("a", 700)+strings.Repeat("b", 300))
	_, inode, _, mtimeNs, err := statFile(f)
	if err != nil {
		t.Fatalf("statFile: %v", err)
	}
	legacy := Checkpoint{Inode: inode, Size: 700, Offset: 700, MtimeNs: mtimeNs, PrefixHash: PrefixHash(f, 700)}
	if err := SaveCheckpoint(path, legacy); err != nil {
		t.Fatalf("SaveCheckpoint (legacy): %v", err)
	}

	start, cp, err := ResolveOffset(f, path, false)
	if err != nil {
		t.Fatalf("ResolveOffset: %v", err)
	}
	if start != 700 {
		t.Errorf("start = %d, want 700 (a pre-CC-2 legacy checkpoint must still resume)", start)
	}
	if cp.Dev == 0 {
		t.Errorf("cp.Dev = 0, want the current file's real device stamped going forward")
	}
}

// TestResolveOffsetResetsOnRotationNewInodeAtSamePath covers the OTHER
// rotation shape (copytruncate-less rotation where the ORIGINAL inode is
// gone and a brand-new file/inode appears at the same path): with no prior
// checkpoint recorded for this inode, and no legacy path-keyed checkpoint
// pointing at a matching inode either, the correct answer is to replay the
// new file from 0.
func TestResolveOffsetResetsOnRotationNewInodeAtSamePath(t *testing.T) {
	withStateHome(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	f1 := openTestFile(t, path, strings.Repeat("a", 1000))
	dev1, inode1, _, mtimeNs1, err := statFile(f1)
	if err != nil {
		t.Fatalf("statFile: %v", err)
	}
	if err := SaveCheckpointByKey(CheckpointKey{Dev: dev1, Inode: inode1}, Checkpoint{Dev: dev1, Inode: inode1, Size: 1000, Offset: 700, MtimeNs: mtimeNs1, PrefixHash: PrefixHash(f1, 700)}); err != nil {
		t.Fatalf("SaveCheckpointByKey: %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	f2 := openTestFile(t, path, strings.Repeat("z", 2000))
	dev2, inode2, _, _, err := statFile(f2)
	if err != nil {
		t.Fatalf("statFile: %v", err)
	}
	if dev1 == dev2 && inode1 == inode2 {
		t.Skip("filesystem reused the same dev/inode for the recreated file; cannot exercise this case here")
	}

	start, cp, err := ResolveOffset(f2, path, false)
	if err != nil {
		t.Fatalf("ResolveOffset: %v", err)
	}
	if start != 0 {
		t.Errorf("start = %d, want 0 (a genuinely new file at the same path, no checkpoint under its own or a legacy path-keyed identity)", start)
	}
	if cp.Generation != 0 {
		t.Errorf("cp.Generation = %d, want 0: this is a brand-new dev/inode identity, not a detected RESET of a previously tracked one", cp.Generation)
	}
}

func TestResolveOffsetResetsOnTruncation(t *testing.T) {
	withStateHome(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	f := openTestFile(t, path, strings.Repeat("a", 500))
	dev, inode, _, mtimeNs, err := statFile(f)
	if err != nil {
		t.Fatalf("statFile: %v", err)
	}
	// The checkpoint's own mtime is stamped in the future, so the
	// mtime-advanced rewrittenInPlace check cannot also be what triggers
	// the reset here -- this test isolates the plain size<offset rule.
	prev := Checkpoint{Dev: dev, Inode: inode, Size: 1000, Offset: 900, MtimeNs: mtimeNs + int64(time.Hour)}
	if err := SaveCheckpointByKey(CheckpointKey{Dev: dev, Inode: inode}, prev); err != nil {
		t.Fatalf("SaveCheckpointByKey: %v", err)
	}
	// Same dev/inode, but current size (500) is smaller than the saved
	// offset (900): the file was truncated (a restarted service
	// overwriting its own log) rather than merely growing further.
	start, cp, err := ResolveOffset(f, path, false)
	if err != nil {
		t.Fatalf("ResolveOffset: %v", err)
	}
	if start != 0 {
		t.Errorf("start = %d, want 0 (truncation)", start)
	}
	if cp.Offset != 0 {
		t.Errorf("cp.Offset = %d, want 0", cp.Offset)
	}
	if cp.Generation != 1 {
		t.Errorf("cp.Generation = %d, want 1 (a reset bumps it)", cp.Generation)
	}
}

// TestResolveOffsetResetsOnInPlaceRewriteSameInode is CC-1's core case: `cmd
// 2> app.log` restarted against the same redirect target truncates and
// rewrites the file without ever unlinking it, so the inode never changes
// and the new content can easily reach or exceed the old checkpoint's
// offset again. mtime advancing while size does not grow past the OLD size
// is what catches it.
func TestResolveOffsetResetsOnInPlaceRewriteSameInode(t *testing.T) {
	withStateHome(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Repeat("a", 900)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	dev, inode, _, mtimeNs, err := statFile(f)
	if err != nil {
		t.Fatalf("statFile: %v", err)
	}
	prev := Checkpoint{Dev: dev, Inode: inode, Size: 900, Offset: 900, MtimeNs: mtimeNs, PrefixHash: PrefixHash(f, 900)}
	if err := SaveCheckpointByKey(CheckpointKey{Dev: dev, Inode: inode}, prev); err != nil {
		t.Fatalf("SaveCheckpointByKey: %v", err)
	}

	// A tick so the new mtime is observably later than the checkpoint's
	// (filesystem mtime resolution can be as coarse as 1s on some
	// platforms/filesystems).
	time.Sleep(1100 * time.Millisecond)
	if err := f.Truncate(0); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	if _, err := f.WriteString(strings.Repeat("c", 850)); err != nil { // smaller than the old 900-byte size
		t.Fatalf("write: %v", err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	rf, err := os.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer rf.Close()

	start, cp, err := ResolveOffset(rf, path, false)
	if err != nil {
		t.Fatalf("ResolveOffset: %v", err)
	}
	if start != 0 {
		t.Errorf("start = %d, want 0 (an in-place rewrite must reset, not resume from the stale offset)", start)
	}
	if cp.Generation != 1 {
		t.Errorf("cp.Generation = %d, want 1", cp.Generation)
	}
}

// TestResolveOffsetResetsOnContentMismatchDespiteStableMtimeAndSize is CC-1's
// belt-and-suspenders case: a rewrite whose size and mtime happen to
// coincide with the previous checkpoint's (coarse filesystem mtime
// resolution, or a rewrite that reproduces the exact same size) is still
// caught by PrefixHash.
func TestResolveOffsetResetsOnContentMismatchDespiteStableMtimeAndSize(t *testing.T) {
	withStateHome(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Repeat("a", 500)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	dev, inode, _, mtimeNs, err := statFile(f)
	if err != nil {
		t.Fatalf("statFile: %v", err)
	}
	prev := Checkpoint{Dev: dev, Inode: inode, Size: 500, Offset: 500, MtimeNs: mtimeNs, PrefixHash: PrefixHash(f, 500)}
	if err := SaveCheckpointByKey(CheckpointKey{Dev: dev, Inode: inode}, prev); err != nil {
		t.Fatalf("SaveCheckpointByKey: %v", err)
	}

	if err := f.Truncate(0); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	if _, err := f.WriteString(strings.Repeat("d", 500)); err != nil { // same size, different content
		t.Fatalf("write: %v", err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	// Force the mtime back to exactly what the checkpoint recorded, so
	// only PrefixHash can catch this rewrite.
	stamp := time.Unix(0, prev.MtimeNs)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	rf, err := os.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer rf.Close()

	start, cp, err := ResolveOffset(rf, path, false)
	if err != nil {
		t.Fatalf("ResolveOffset: %v", err)
	}
	if start != 0 {
		t.Errorf("start = %d, want 0 (content changed even though size and mtime look unchanged)", start)
	}
	if cp.Generation != 1 {
		t.Errorf("cp.Generation = %d, want 1", cp.Generation)
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
