// Package profiler captures heap, CPU, goroutine, and macOS `sample` profiles
// for a process. For Go processes it scrapes net/http/pprof endpoints; for any
// other process it falls back to macOS `sample <pid>`.
package profiler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/google/pprof/profile"

	"github.com/abdul-hamid-achik/monitor/internal/capability"
	"github.com/abdul-hamid-achik/monitor/internal/contextids"
)

// ProfileType is a discriminator for the kind of profile captured.
type ProfileType string

const (
	ProfileHeap      ProfileType = "heap"
	ProfileCPU       ProfileType = "cpu"
	ProfileGoroutine ProfileType = "goroutine"
	ProfileSample    ProfileType = "sample"
)

// Symbol is one parsed stack frame.
type Symbol struct {
	Func   string `json:"func"`
	File   string `json:"file"`
	Line   int    `json:"line"`
	Source string `json:"source,omitempty"`
	// Weight is the frame's flat (self) share of the profile as a
	// percentage, when available. 0 for formats without a per-frame cost.
	Weight float64 `json:"weight,omitempty"`
	// Cum is the frame's cumulative share of the profile as a percentage:
	// this line's own weight plus everything sampled underneath it on the
	// stack (e.g. a wrapper that only ever calls into a hot function has
	// Weight ~0 but Cum close to that callee's total). Additive; only pprof
	// proto captures (cpu/heap/goroutine) populate it today. When present,
	// correlation scoring prefers it over Weight (a wrapper's hot *line* —
	// the call site — is exactly what Cum surfaces and Weight alone can't).
	Cum float64 `json:"cum,omitempty"`
}

// Profile is the result of a capture.
type Profile struct {
	PID     int32          `json:"pid"`
	Type    ProfileType    `json:"type"`
	Method  string         `json:"method,omitempty"`
	Taken   time.Time      `json:"taken"`
	Path    string         `json:"path,omitempty"`
	Text    string         `json:"text,omitempty"`
	Symbols []Symbol       `json:"symbols,omitempty"`
	Stats   *Stats         `json:"stats,omitempty"`
	Receipt *Receipt       `json:"receipt,omitempty"`
	Context contextids.IDs `json:"context,omitempty"`
}

// Stats reports the pseudo-frame/idle breakdown of a profile so callers
// never divide a hot line's weight by a padded denominator. Samples is every
// observed sample (including idle/GC/program pseudo-frames and, for macOS
// `sample`, idle syscalls); ActiveSamples excludes them. Additive: omitted
// entirely (via Profile.Stats being nil) for capture methods that don't
// compute it (pprof text/proto dumps, plain `go tool pprof`).
type Stats struct {
	Samples       int     `json:"samples"`
	ActiveSamples int     `json:"active_samples"`
	IdlePct       float64 `json:"idle_pct"`
	GCPct         float64 `json:"gc_pct"`
}

// ValidateCaptureWith checks a requested profile type against an injected
// capability set. Entry points call this before ownership checks or collection.
func ValidateCaptureWith(caps capability.Set, t ProfileType) error {
	switch t {
	case ProfileHeap, ProfileCPU, ProfileGoroutine:
		return caps.Require(capability.ProfilePprof)
	case ProfileSample:
		return caps.Require(capability.ProfileSample)
	default:
		return fmt.Errorf("unknown profile type %q", t)
	}
}

// ValidateCapture checks profile support on the current host.
func ValidateCapture(t ProfileType) error {
	return ValidateCaptureWith(capability.Current(), t)
}

// Capture takes a profile snapshot for the given pid. For the pprof types
// (heap/cpu/goroutine) it scrapes the net/http/pprof server at addr (default
// "localhost:6060" when addr is ""); the caller is responsible for pointing
// addr at the target pid's own pprof server — callers that need proof the
// endpoint belongs to pid should call VerifyListenerOwnership first (the
// investigate pipeline and the MCP profile tool do). For the macOS sample
// type it runs `sample <pid>` and addr is ignored.
func Capture(ctx context.Context, pid int32, t ProfileType, addr string) (Profile, error) {
	p := Profile{PID: pid, Type: t, Taken: time.Now()}
	if err := ValidateCapture(t); err != nil {
		return p, err
	}
	if pid <= 0 {
		return p, fmt.Errorf("invalid pid %d", pid)
	}
	switch t {
	case ProfileHeap, ProfileGoroutine, ProfileCPU:
		return captureProfilePprof(ctx, p, addr, t)
	case ProfileSample:
		return captureSample(ctx, pid)
	default:
		return p, fmt.Errorf("unknown profile type %q", t)
	}
}

// pprofPreferredValue picks which of a pprof profile's (possibly several)
// sample-value columns symbolsFromPprof weights by, per profile type. Heap
// profiles from net/http/pprof always carry all four
// alloc/inuse x objects/space columns regardless of ?gc=1; inuse_space
// answers "what's using memory right now" rather than "what has ever been
// allocated", which is what a human reaches for `hot`/`profile -t heap` to
// find. CPU prefers the "cpu" (nanoseconds) column over the parallel
// "samples" (count) column so profiles taken at different durations/rates
// stay comparable; goroutine profiles carry a single unnamed count column,
// so pprofValueIndex's fallback (the last column) is exactly right without
// a preferred name.
func pprofPreferredValue(t ProfileType) []string {
	switch t {
	case ProfileHeap:
		return []string{"inuse_space", "alloc_space", "inuse_objects", "alloc_objects"}
	case ProfileCPU:
		return []string{"cpu", "samples"}
	default:
		return nil
	}
}

// captureProfilePprof fetches a pprof proto (heap/goroutine: the raw
// protobuf, never ?debug=1 text; cpu: /debug/pprof/profile, always proto)
// and parses it in-process with github.com/google/pprof/profile —
// monitor no longer shells out to `go tool pprof`, so profiling works on a
// host with no go toolchain installed. The proto is always saved to Path so
// the raw evidence survives even when in-process symbolication finds
// nothing (a profile with zero samples of the selected type isn't an
// error).
func captureProfilePprof(ctx context.Context, p Profile, addr string, t ProfileType) (Profile, error) {
	p.Method = "pprof_" + string(t)
	endpoint := pprofURL(addr, t)
	body, err := httpGet(ctx, endpoint)
	if err != nil {
		return p, fmt.Errorf("scrape %s: %w", endpoint, err)
	}
	path, werr := writeTempProfile(p.PID, t, body)
	if werr != nil {
		return p, fmt.Errorf("save %s profile: %w", t, werr)
	}
	p.Path = path

	prof, perr := profile.Parse(bytes.NewReader(body))
	if perr != nil {
		// The raw proto is still saved and still analyzable externally
		// (`go tool pprof` on Path); a parse failure degrades to "no
		// symbols found" rather than failing the whole capture.
		return p, nil
	}
	valueIdx := pprofValueIndex(prof, pprofPreferredValue(t)...)
	syms := symbolsFromPprof(prof, valueIdx)
	p.Symbols = syms
	if t != ProfileCPU {
		// CPU's saved .pb.gz IS the primary artifact `go tool pprof` reads;
		// heap/goroutine no longer have a ?debug=1 text dump at all now
		// that both are fetched as proto, so Text becomes the human-
		// readable top-N summary instead of going empty.
		p.Text = summarizeSymbols(syms, 25)
	}
	return p, nil
}

// DefaultPprofAddr is the host:port scraped when Capture is given no address.
const DefaultPprofAddr = "localhost:6060"

// pprofURL maps a ProfileType to its net/http/pprof endpoint at addr
// (host:port, defaulting to localhost:6060 when empty). Every type is
// fetched as the raw protobuf now (never ?debug=1 text): CPU always was
// (there is no /cpu handler; /profile bounded with ?seconds=1 so the scrape
// can't block indefinitely), and heap/goroutine moved off ?debug=1 so
// symbolsFromPprof can compute real flat/cum per line with inlining instead
// of text-scraping a human-oriented dump.
func pprofURL(addr string, t ProfileType) string {
	if addr == "" {
		addr = DefaultPprofAddr
	}
	base := "http://" + addr + "/debug/pprof/"
	switch t {
	case ProfileCPU:
		return base + "profile?seconds=1"
	default:
		return base + string(t)
	}
}

// pprofClient bounds a scrape so a hung/slow pprof endpoint can't stall
// forever when the caller passes a context without a deadline. The timeout
// comfortably covers the bounded CPU profile (?seconds=1).
var pprofClient = &http.Client{Timeout: 30 * time.Second}

const maxRawProfileBytes int64 = 128 << 20

// writeTempProfile saves a raw pprof protobuf to a private temp file and
// returns its path, so the profile is preserved (and analyzable with
// `go tool pprof` if the caller wants a second opinion) even when in-process
// symbolication finds nothing.
func writeTempProfile(pid int32, t ProfileType, body []byte) (string, error) {
	f, err := os.CreateTemp("", fmt.Sprintf("monitor-%s-%d-*.pb.gz", t, pid))
	if err != nil {
		return "", err
	}
	path := f.Name()
	if _, err := f.Write(body); err != nil {
		closeErr := f.Close()
		removeErr := os.Remove(path)
		return "", errors.Join(err, closeErr, removeErr)
	}
	if err := f.Close(); err != nil {
		return "", errors.Join(err, os.Remove(path))
	}
	return path, nil
}

func httpGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := pprofClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, errors.Join(fmt.Errorf("status %d", resp.StatusCode), resp.Body.Close())
	}
	b, readErr := io.ReadAll(io.LimitReader(resp.Body, maxRawProfileBytes+1))
	closeErr := resp.Body.Close()
	if readErr == nil && int64(len(b)) > maxRawProfileBytes {
		readErr = fmt.Errorf("profile response exceeds %d bytes", maxRawProfileBytes)
	}
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	return b, nil
}

// ToJSON is a convenience for CLI --json output.
func (p Profile) ToJSON() ([]byte, error) { return json.MarshalIndent(p, "", "  ") }
