package profiler

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	gpprof "github.com/google/pprof/profile"
)

func TestLoadFileDetectsV8CPUProfile(t *testing.T) {
	src, err := LoadFile("testdata/v8-hot.cpuprofile")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if src.Kind != SourceCDP {
		t.Fatalf("Kind = %q, want %q", src.Kind, SourceCDP)
	}
	if src.CDP == nil || len(src.CDP.Nodes) == 0 {
		t.Fatal("CDP profile has no nodes")
	}
	if src.Pprof != nil {
		t.Error("Pprof should be nil for a CDP source")
	}
}

func TestLoadFileDetectsBunCPUProfile(t *testing.T) {
	src, err := LoadFile("testdata/bun-cpu-prof.cpuprofile")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if src.Kind != SourceCDP {
		t.Fatalf("Kind = %q, want %q", src.Kind, SourceCDP)
	}
}

// TestLoadFileDetectsPprofGzip round-trips a synthetic pprof.Profile through
// (*profile.Profile).Write (the real gzip .pb.gz shape `monitor profile
// --output` and net/http/pprof's CPU endpoint both produce) and confirms
// LoadFile parses it without needing the go toolchain or a real process.
func TestLoadFileDetectsPprofGzip(t *testing.T) {
	prof := syntheticPprofProfile(t)
	var buf bytes.Buffer
	if err := prof.Write(&buf); err != nil {
		t.Fatalf("write synthetic pprof: %v", err)
	}
	path := filepath.Join(t.TempDir(), "cpu.pb.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	src, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if src.Kind != SourcePprof {
		t.Fatalf("Kind = %q, want %q", src.Kind, SourcePprof)
	}
	if src.Pprof == nil || len(src.Pprof.Sample) != 1 {
		t.Fatalf("Pprof = %+v, want 1 sample round-tripped", src.Pprof)
	}
	if src.CDP != nil {
		t.Error("CDP should be nil for a pprof source")
	}
}

// TestLoadFileDetectsPprofUncompressed exercises the raw (non-gzip) .pb
// branch profile.Parse also accepts.
func TestLoadFileDetectsPprofUncompressed(t *testing.T) {
	prof := syntheticPprofProfile(t)
	var buf bytes.Buffer
	if err := prof.WriteUncompressed(&buf); err != nil {
		t.Fatalf("write uncompressed: %v", err)
	}
	path := filepath.Join(t.TempDir(), "cpu.pb")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	src, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if src.Kind != SourcePprof {
		t.Fatalf("Kind = %q, want %q", src.Kind, SourcePprof)
	}
}

func TestLoadFileRejectsNeitherFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-profile.txt")
	if err := os.WriteFile(path, []byte("this is neither a cpuprofile nor a pprof proto\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("expected an error for a file that is neither format")
	}
}

func TestLoadFileRejectsCPUProfileWithNoNodes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.cpuprofile")
	if err := os.WriteFile(path, []byte(`{"nodes":[],"samples":[],"startTime":0,"endTime":0}`), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("expected an error for a .cpuprofile with zero nodes")
	}
}

func TestLoadFileMissingFile(t *testing.T) {
	if _, err := LoadFile(filepath.Join(t.TempDir(), "does-not-exist.cpuprofile")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

// syntheticPprofProfile builds a minimal, valid pprof profile whose
// Location/Function/Mapping ARE registered in the Profile's own slices (not
// just referenced from a Sample) — required for a real Write/Parse
// round-trip: protobuf serialization walks Profile.Location/Function by ID,
// so a Location only reachable via Sample.Location, and never listed in
// Profile.Location itself, silently becomes a nil reference on the other
// side of the wire (CheckValid then rejects it as "sample has nil
// location").
func syntheticPprofProfile(t *testing.T) *gpprof.Profile {
	t.Helper()
	fn := &gpprof.Function{ID: 1, Name: "main.work", Filename: "main.go"}
	loc := &gpprof.Location{ID: 1, Line: []gpprof.Line{{Function: fn, Line: 10}}}
	return &gpprof.Profile{
		SampleType: []*gpprof.ValueType{{Type: "samples", Unit: "count"}},
		Sample:     []*gpprof.Sample{{Value: []int64{5}, Location: []*gpprof.Location{loc}}},
		Location:   []*gpprof.Location{loc},
		Function:   []*gpprof.Function{fn},
	}
}
