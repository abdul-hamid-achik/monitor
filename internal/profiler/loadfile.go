package profiler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/google/pprof/profile"
)

// SourceKind discriminates the two on-disk profile formats LoadFile
// understands.
type SourceKind string

const (
	// SourceCDP is a V8/Bun .cpuprofile: the same hierarchical call-tree
	// JSON shape (cdpProfile, in inspector.go) ProfileInspector decodes
	// live from Profiler.stop over the CDP websocket.
	SourceCDP SourceKind = "cdp"
	// SourcePprof is a github.com/google/pprof proto, gzip-compressed
	// (.pb.gz, what `monitor profile --output` and net/http/pprof's CPU
	// endpoint write) or raw (.pb).
	SourcePprof SourceKind = "pprof"
)

// Source is one profile loaded from disk by LoadFile, ready for
// BuildHeatmap. Exactly one of CDP or Pprof is set, matching Kind.
type Source struct {
	Kind SourceKind
	Path string
	// CDP is set for SourceCDP. Its type is unexported (cdpProfile, from
	// inspector.go) since only this package's own BuildHeatmap needs to
	// walk it; callers outside the package just carry a *Source through to
	// BuildHeatmap without inspecting its fields.
	CDP *cdpProfile
	// Pprof is set for SourcePprof.
	Pprof *profile.Profile
}

// LoadFile detects and loads a CPU profile from disk:
//
//   - a V8/Bun .cpuprofile: JSON with a top-level "nodes" array of
//     call-tree nodes carrying positionTicks — what `node --cpu-prof`,
//     `bun --cpu-prof`, and Chrome DevTools' "Save profile…" all write, and
//     the exact shape ProfileInspector (inspector.go) decodes live over the
//     CDP websocket;
//   - a github.com/google/pprof proto — gzip-compressed (.pb.gz, what
//     `monitor profile --output` and net/http/pprof's own CPU endpoint
//     write) or raw (.pb). profile.Parse auto-detects the gzip envelope by
//     its magic bytes, so both extensions decode the same way here.
//
// Detection sniffs content rather than trusting the file extension — an
// exported profile can be renamed, and neither extension is guaranteed —
// by attempting the CDP-shaped JSON decode first and falling back to the
// pprof proto parser. A file that is neither (or a .cpuprofile with zero
// nodes) is reported as an error naming both formats it was tried against,
// never silently treated as an empty profile.
func LoadFile(path string) (*Source, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read profile %s: %w", path, err)
	}
	if cp, ok := tryDecodeCPUProfile(data); ok {
		return &Source{Kind: SourceCDP, Path: path, CDP: cp}, nil
	}
	prof, perr := profile.ParseData(data)
	if perr != nil {
		return nil, fmt.Errorf("%s is neither a V8/Bun .cpuprofile nor a pprof profile: %w", path, perr)
	}
	return &Source{Kind: SourcePprof, Path: path, Pprof: prof}, nil
}

// tryDecodeCPUProfile attempts the CDP-shaped JSON decode. A pprof proto is
// always binary (gzip magic 0x1f 0x8b, or a raw protobuf whose first byte
// is essentially never the ASCII '{' a JSON object opens with), so it fails
// the leading-brace check immediately and LoadFile falls straight through
// to profile.Parse without wasting a JSON-decode attempt on binary data.
func tryDecodeCPUProfile(data []byte) (*cdpProfile, bool) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, false
	}
	var cp cdpProfile
	if err := json.Unmarshal(trimmed, &cp); err != nil {
		return nil, false
	}
	if len(cp.Nodes) == 0 {
		// Valid JSON, even an object with a "nodes" key, but no nodes at all
		// is not a usable capture — fall through to the pprof attempt (which
		// will also fail on JSON input) so LoadFile's caller sees one clear
		// "neither format" error instead of silently returning an empty CDP
		// profile that would make BuildHeatmap fabricate an "idle" verdict
		// from zero real data.
		return nil, false
	}
	return &cp, true
}
