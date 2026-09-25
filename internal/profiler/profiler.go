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
	"math"
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
	// Weight ~0 but Cum close to that callee's total). Additive; pprof
	// proto captures (cpu/heap/goroutine) and macOS `sample` captures
	// populate it; CDP (Node/Bun/Deno) does not, since V8 positionTicks are
	// already self-time at statement granularity — there is no separate
	// "cumulative" concept to compute at that level. When present,
	// correlation scoring prefers it over Weight (a wrapper's hot *line* —
	// the call site — is exactly what Cum surfaces and Weight alone can't).
	Cum float64 `json:"cum,omitempty"`
	// FuncLine is the function's own declaration line (1-based), when known
	// — for CDP captures, callFrame.lineNumber+1. It disambiguates two
	// distinct hot-line rows that share the same (File, Func) but are not
	// actually the same function: V8 labels every anonymous closure "" /
	// "(anonymous)", so two unrelated closures in one file are otherwise
	// indistinguishable by (File, Func) alone (see correlateProfile's cache
	// key in internal/cli/profile_logs.go). Additive; 0 when unknown (pprof
	// proto and macOS `sample` symbols never set it — Go/native function
	// names are already unique, so the collision this exists for can't
	// happen there).
	FuncLine int `json:"func_line,omitempty"`
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

// defaultCPUDuration is the CPU sampling window used by Capture, which
// predates a per-call duration knob: every caller that hasn't adopted
// CaptureWithDuration (MCP's monitor_profile_capture doesn't expose a
// --duration equivalent yet — that's E1.7) keeps this exact 1s window.
const defaultCPUDuration = 1 * time.Second

// Capture takes a profile snapshot for the given pid, using a 1s CPU
// sampling window (see defaultCPUDuration). It is CaptureWithDuration with
// that default; callers that need to honor a caller-supplied duration (the
// `profile` CLI command's --duration flag) should call CaptureWithDuration
// directly instead.
func Capture(ctx context.Context, pid int32, t ProfileType, addr string) (Profile, error) {
	return CaptureWithDuration(ctx, pid, t, addr, defaultCPUDuration)
}

// CaptureWithDuration takes a profile snapshot for the given pid. For the
// pprof types (heap/cpu/goroutine) it scrapes the net/http/pprof server at
// addr (default "localhost:6060" when addr is ""); the caller is
// responsible for pointing addr at the target pid's own pprof server —
// callers that need proof the endpoint belongs to pid should call
// VerifyListenerOwnership first (the investigate pipeline and the MCP
// profile tool do). cpuDuration sizes the CPU sampling window
// (?seconds=N) and the macOS `sample` fallback's own seconds argument
// (clamped to 1-120 by SampleSeconds, LUX-15); it is ignored for
// heap/goroutine (instant snapshots). For the macOS sample type addr is
// ignored.
func CaptureWithDuration(ctx context.Context, pid int32, t ProfileType, addr string, cpuDuration time.Duration) (Profile, error) {
	p := Profile{PID: pid, Type: t, Taken: time.Now()}
	if err := ValidateCapture(t); err != nil {
		return p, err
	}
	if pid <= 0 {
		return p, fmt.Errorf("invalid pid %d", pid)
	}
	if cpuDuration <= 0 {
		cpuDuration = defaultCPUDuration
	}
	switch t {
	case ProfileHeap, ProfileGoroutine, ProfileCPU:
		return captureProfilePprof(ctx, p, addr, t, cpuDuration)
	case ProfileSample:
		return captureSample(ctx, pid, cpuDuration)
	default:
		return p, fmt.Errorf("unknown profile type %q", t)
	}
}

// SampleSeconds clamps a requested macOS `sample` window to the whole
// seconds `sample`'s own CLI accepts (LUX-15): at least 1 (a 0 would mean
// `sample`'s own longer default, silently ignoring the caller) and at most
// 120 (`monitor profile --duration`'s own cap — anything longer holds the
// terminal for no additional diagnostic value a heatmap needs). Shared by
// captureSample and the `monitor hot` banner, so the duration shown is
// always the duration actually sampled.
func SampleSeconds(d time.Duration) int {
	secs := int(math.Ceil(d.Seconds()))
	if secs < 1 {
		secs = 1
	}
	if secs > 120 {
		secs = 120
	}
	return secs
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
// protobuf for Symbols and the on-disk Path evidence; cpu: /debug/pprof/
// profile, always proto) and parses it in-process with
// github.com/google/pprof/profile — monitor no longer shells out to
// `go tool pprof`, so profiling works on a host with no go toolchain
// installed. The proto is always saved to Path so the raw evidence
// survives even when in-process symbolication finds nothing (a profile
// with zero samples of the selected type isn't an error). cpuDuration
// sizes the CPU sampling window (ignored for heap/goroutine, which are
// instant snapshots either way).
//
// For heap/goroutine, Text is the legacy ?debug=1 text dump (CC-1): the
// `monitor profile --json` contract (glyphrun procmon stores Text
// verbatim, AGENTS.md) promises the full readable dump — every goroutine
// stack, the heap's own legend lines — not a summary table. A target that
// can't serve debug=1 falls back to summarizeSymbols rather than losing
// Text entirely.
func captureProfilePprof(ctx context.Context, p Profile, addr string, t ProfileType, cpuDuration time.Duration) (Profile, error) {
	p.Method = "pprof_" + string(t)
	endpoint := pprofURL(addr, t, cpuDuration)
	client := pprofClient
	if t == ProfileCPU {
		// The server blocks for ~cpuDuration seconds serving this one
		// request (?seconds=N); the shared client's fixed 30s bound would
		// kill any request close to or past that, so CPU gets its own
		// client sized to the requested window plus network/scheduling
		// slack instead.
		client = cpuHTTPClient(cpuDuration)
	}
	body, err := httpGet(ctx, client, endpoint)
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
		// its Text stays empty (see the doc comment above).
		if dump, derr := httpGet(ctx, client, pprofDebugURL(addr, t)); derr == nil && len(dump) > 0 {
			p.Text = string(dump)
		} else {
			p.Text = summarizeSymbols(syms, 25)
		}
	}
	return p, nil
}

// pprofDebugURL maps a non-CPU ProfileType to its legacy human-readable
// ?debug=1 dump endpoint at addr (host:port, defaulting to localhost:6060
// when empty) — the text form main's `monitor profile` always returned in
// Text for heap/goroutine (CC-1). Never used for CPU: /debug/pprof/profile
// has no debug=1 form, and the proto capture above already covers it.
func pprofDebugURL(addr string, t ProfileType) string {
	if addr == "" {
		addr = DefaultPprofAddr
	}
	return fmt.Sprintf("http://%s/debug/pprof/%s?debug=1", addr, t)
}

// DefaultPprofAddr is the host:port scraped when Capture is given no address.
const DefaultPprofAddr = "localhost:6060"

// pprofURL maps a ProfileType to its net/http/pprof PROTO endpoint at addr
// (host:port, defaulting to localhost:6060 when empty). Every type is
// fetched as the raw protobuf here: CPU always was (there is no /cpu
// handler; /profile bounded with ?seconds=N so the scrape can't block
// indefinitely), and heap/goroutine's symbols need the proto so
// symbolsFromPprof can compute real flat/cum per line with inlining
// instead of text-scraping a human-oriented dump (heap/goroutine's
// human-readable ?debug=1 text is fetched separately for Text — see
// pprofDebugURL and CC-1). cpuDuration sets N, rounded up to the next
// whole second (net/http/pprof's `seconds` query param is an integer) and
// floored at 1 so a caller-supplied sub-second duration still samples for
// at least one second rather than requesting `seconds=0` (net/http/pprof
// treats that as "use its own 30s default", silently ignoring the
// caller's intent). Ignored for heap/goroutine.
func pprofURL(addr string, t ProfileType, cpuDuration time.Duration) string {
	if addr == "" {
		addr = DefaultPprofAddr
	}
	base := "http://" + addr + "/debug/pprof/"
	switch t {
	case ProfileCPU:
		secs := int(math.Ceil(cpuDuration.Seconds()))
		if secs < 1 {
			secs = 1
		}
		return fmt.Sprintf("%sprofile?seconds=%d", base, secs)
	default:
		return base + string(t)
	}
}

// pprofClient bounds a scrape so a hung/slow pprof endpoint can't stall
// forever when the caller passes a context without a deadline. Used for
// heap/goroutine (always-instant snapshots regardless of caller intent) and
// as Capture's implicit 1s-CPU-window callers' bound; see cpuHTTPClient for
// CPU requests that ask for a longer window.
var pprofClient = &http.Client{Timeout: 30 * time.Second}

// cpuHTTPClientSlack is added on top of the requested CPU sampling window
// to give the server's own timer, network transit, and scheduling jitter
// room, so a --duration close to pprofClient's fixed 30s bound doesn't get
// killed mid-scrape.
const cpuHTTPClientSlack = 30 * time.Second

// cpuHTTPClient returns an HTTP client whose timeout comfortably covers a
// CPU profile scrape of the given duration. Built fresh per call (CPU
// profiles are rare, latency-insensitive requests, so losing connection
// reuse is not a real cost) rather than mutating the shared pprofClient,
// which heap/goroutine captures keep using with their fixed, always-modest
// bound.
func cpuHTTPClient(cpuDuration time.Duration) *http.Client {
	if cpuDuration <= 0 {
		cpuDuration = defaultCPUDuration
	}
	return &http.Client{Timeout: cpuDuration + cpuHTTPClientSlack}
}

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

func httpGet(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
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

// DiscardRawArtifact removes the profile's on-disk temp file, if any — a
// pprof capture (heap/cpu/goroutine, via writeTempProfile) or a CDP heap
// snapshot (ProfileInspectorHeap's own .heapsnapshot file) can set Path; a
// CDP CPU profile and macOS `sample` never write one — and clears both
// Path and Text so neither the file nor its bytes linger in a caller's
// response.
//
// A caller that wants to keep the raw capture — a human inspecting a CPU
// profile with `go tool pprof`, an agent that explicitly asked to keep the
// raw payload — must copy Path/Text out (or request --output / keep:true /
// --include-raw at its own layer) BEFORE calling this; it is destructive
// and offers no undo. It exists so MCP's monitor_profile_capture (repeated
// calls without keep:true) and investigate's pipeline don't leave a
// /tmp/monitor-<type>-<pid>-*.pb.gz behind on every capture (E1.7).
//
// Safe to call on a Profile with no Path (a CDP/sample capture, or one
// already discarded); the returned error is only a failed os.Remove of an
// existing Path (a permission issue, say) — Path/Text are still cleared to
// keep the fields internally consistent (Path never points at a file that
// might not exist) even when that Remove fails, and IsNotExist is treated
// as success (the file is already gone, which is the caller's goal).
func (p *Profile) DiscardRawArtifact() error {
	p.Text = ""
	if p.Path == "" {
		return nil
	}
	path := p.Path
	p.Path = ""
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
