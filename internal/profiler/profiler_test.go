package profiler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/pprof"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/capability"
)

// TestCaptureHeapOverHTTP exercises the real scrape path: Capture builds the
// /debug/pprof/heap and /debug/pprof/goroutine URLs (proto, not ?debug=1
// text — the net/http/pprof mux serves runtime/pprof's real gzipped
// protobuf by default), fetches them over HTTP, and parses them in-process
// with github.com/google/pprof/profile. We serve a live pprof endpoint from
// this test process so the profile describes this process's real heap.
func TestCaptureHeapOverHTTP(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index) // dispatches /debug/pprof/heap etc.
	srv := httptest.NewServer(mux)
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	for _, pt := range []ProfileType{ProfileHeap, ProfileGoroutine} {
		p, err := Capture(context.Background(), 4321, pt, addr)
		if err != nil {
			t.Fatalf("Capture(%s): %v", pt, err)
		}
		if p.PID != 4321 || p.Type != pt {
			t.Errorf("%s: meta = %+v", pt, p)
		}
		if p.Path == "" {
			t.Errorf("%s: expected the raw proto to be saved to Path", pt)
		} else {
			defer os.Remove(p.Path)
			if info, statErr := os.Stat(p.Path); statErr != nil || info.Size() == 0 {
				t.Errorf("%s: saved profile missing/empty (stat=%v err=%v)", pt, info, statErr)
			}
		}
	}

	// A real, currently-running Go test process always has some heap
	// allocations and at least one goroutine, so real symbols (not just an
	// empty capture) must come back — this is the regression for the
	// removed HasSuffix(fn,"s") bug corrupting/dropping real symbols.
	p, err := Capture(context.Background(), 1, ProfileHeap, addr)
	if err != nil {
		t.Fatalf("Capture(heap): %v", err)
	}
	if p.Path != "" {
		defer os.Remove(p.Path)
	}
	if len(p.Symbols) == 0 {
		t.Fatal("expected non-empty symbols from a real heap profile")
	}
	if p.Text == "" {
		t.Error("expected Profile.Text to hold a readable top-N summary now that heap has no ?debug=1 text dump")
	}
	for _, s := range p.Symbols {
		if s.Func == "unknown" || s.Func == "(unknown)" {
			t.Errorf("real heap profile produced an %s symbol: %+v", s.Func, s)
		}
	}
}

// TestCaptureWithDurationSendsRequestedSeconds is the end-to-end regression
// for the major finding that `monitor profile --duration X` silently
// ignored X on the pprof path (pprofURL hardcoded ?seconds=1, and Capture
// had no duration parameter at all). This drives the real HTTP path — not
// just pprofURL in isolation — asserting the server actually received the
// caller's requested window, and that Capture itself (no explicit
// duration) keeps requesting the historical default of 1.
func TestCaptureWithDurationSendsRequestedSeconds(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	if _, err := CaptureWithDuration(context.Background(), 1, ProfileCPU, addr, 7*time.Second); err != nil {
		t.Fatalf("CaptureWithDuration: %v", err)
	}
	if gotQuery != "seconds=7" {
		t.Errorf("server received query %q, want seconds=7", gotQuery)
	}

	if _, err := Capture(context.Background(), 1, ProfileCPU, addr); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if gotQuery != "seconds=1" {
		t.Errorf("Capture (no explicit duration) sent %q, want seconds=1 (its historical default)", gotQuery)
	}
}

// TestCaptureScrapeErrorIsReported: a non-2xx endpoint surfaces a scrape error
// rather than a silent empty profile.
func TestCaptureScrapeErrorIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	if _, err := Capture(context.Background(), 1, ProfileHeap, addr); err == nil {
		t.Error("expected a scrape error on a 500 response")
	}
}

func TestCaptureInvalidPID(t *testing.T) {
	for _, pt := range []ProfileType{ProfileHeap, ProfileCPU, ProfileGoroutine, ProfileSample} {
		if _, err := Capture(context.Background(), 0, pt, ""); err == nil {
			t.Errorf("pid 0 should error for %s", pt)
		}
		if _, err := Capture(context.Background(), -1, pt, ""); err == nil {
			t.Errorf("pid -1 should error for %s", pt)
		}
	}
}

func TestCaptureUnknownType(t *testing.T) {
	if _, err := Capture(context.Background(), 1234, ProfileType("bogus"), ""); err == nil {
		t.Error("unknown profile type should error")
	}
}

func TestValidateCaptureWithInjectedCapabilities(t *testing.T) {
	linux := capability.Detect(capability.Detector{GOOS: "linux", LookPath: func(string) (string, error) {
		return "", context.Canceled
	}})
	if err := ValidateCaptureWith(linux, ProfileHeap); err != nil {
		t.Fatalf("heap unexpectedly rejected: %v", err)
	}
	if err := ValidateCaptureWith(linux, ProfileSample); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("sample error = %v, want unsupported capability", err)
	}
}

// parsePprof/parsePprofTop/goToolPprofTop (text-scraping `go tool pprof`
// output, requiring the go toolchain on PATH) were removed in favor of
// symbolsFromPprof (pprof.go / pprof_test.go), which parses the real
// protobuf in-process. See pprof_test.go for their regression coverage,
// including the removed HasSuffix(fn,"s") → "unknown" bug.

// parseSample (a single-line regex parser with no tree/self-time semantics)
// was replaced by parseSampleTree (see sample_parse_test.go): the regex
// required a digit at the start of the line, but real `sample` output
// prefixes nested frames with '+'/'!'/':'/'|', so it returned zero symbols
// in practice.

// TestPprofURLMapsCPUToProfile is a regression for the bug where
// ProfileCPU built /debug/pprof/cpu (a 404 — net/http/pprof has no "cpu"
// handler) instead of /debug/pprof/profile. It also pins heap/goroutine to
// the plain proto endpoint (no ?debug=1): the old text dump can't carry
// per-line flat/cum with inlining, only the wire proto can.
func TestPprofURLMapsCPUToProfile(t *testing.T) {
	// "" defaults to localhost:6060. A duration of 1s (Capture's implicit
	// default) matches the historical hardcoded ?seconds=1.
	cases := map[ProfileType]string{
		ProfileHeap:      "http://localhost:6060/debug/pprof/heap",
		ProfileGoroutine: "http://localhost:6060/debug/pprof/goroutine",
	}
	for pt, want := range cases {
		if got := pprofURL("", pt, time.Second); got != want {
			t.Errorf("pprofURL(%q, %s) = %q, want %q", "", pt, got, want)
		}
	}
	if got := pprofURL("", ProfileCPU, time.Second); got != "http://localhost:6060/debug/pprof/profile?seconds=1" {
		t.Errorf("pprofURL(cpu, 1s) = %q, want ?seconds=1", got)
	}
	// A custom address is honored.
	if got := pprofURL("10.0.0.5:7070", ProfileHeap, time.Second); got != "http://10.0.0.5:7070/debug/pprof/heap" {
		t.Errorf("custom addr pprofURL = %q", got)
	}
}

// TestPprofURLHonorsCPUDuration is the major-finding regression: a
// --duration passed through to the pprof CPU path (the profile command's
// only path capable of a multi-second capture) must actually change the
// requested ?seconds=N, not silently stay pinned at the old hardcoded 1.
func TestPprofURLHonorsCPUDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{5 * time.Second, "http://localhost:6060/debug/pprof/profile?seconds=5"},
		// Sub-second durations still round UP to a whole second (the
		// query param is an integer) rather than truncating to 0/1.
		{1500 * time.Millisecond, "http://localhost:6060/debug/pprof/profile?seconds=2"},
		// A non-positive duration is not a valid sampling window; floor at
		// 1s rather than emitting ?seconds=0 (net/http/pprof would then
		// fall back to its OWN 30s default, silently ignoring the caller).
		{0, "http://localhost:6060/debug/pprof/profile?seconds=1"},
	}
	for _, tc := range cases {
		if got := pprofURL("", ProfileCPU, tc.d); got != tc.want {
			t.Errorf("pprofURL(cpu, %v) = %q, want %q", tc.d, got, tc.want)
		}
	}
	// heap/goroutine ignore the duration entirely — they're instant
	// snapshots, not a bounded sampling window.
	if got := pprofURL("", ProfileHeap, 90*time.Second); got != "http://localhost:6060/debug/pprof/heap" {
		t.Errorf("pprofURL(heap, 90s) = %q, want duration ignored", got)
	}
}

func TestProfileJSON(t *testing.T) {
	p := Profile{PID: 42, Type: ProfileHeap}
	b, err := p.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	if len(b) == 0 {
		t.Error("JSON empty")
	}
}

// TestDiscardRawArtifactRemovesFileAndClearsFields verifies E1.7's cleanup
// helper: it deletes the on-disk temp file (only a pprof capture ever sets
// one) and clears both Path and Text.
func TestDiscardRawArtifactRemovesFileAndClearsFields(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "monitor-cpu-*.pb.gz")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("raw profile bytes"); err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	p := Profile{PID: 1, Type: ProfileCPU, Path: path, Text: "should be dropped too"}
	if err := p.DiscardRawArtifact(); err != nil {
		t.Fatalf("DiscardRawArtifact: %v", err)
	}
	if p.Path != "" || p.Text != "" {
		t.Errorf("Path/Text not cleared: %+v", p)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("temp file still exists at %s: %v", path, statErr)
	}
}

// TestDiscardRawArtifactNoPathIsNoop verifies a CDP/sample capture (Text
// only, no Path) is left with Text cleared and no error, even with nothing
// on disk to remove.
func TestDiscardRawArtifactNoPathIsNoop(t *testing.T) {
	p := Profile{PID: 1, Type: ProfileSample, Text: "Sampling process 1\n"}
	if err := p.DiscardRawArtifact(); err != nil {
		t.Fatalf("DiscardRawArtifact: %v", err)
	}
	if p.Text != "" {
		t.Errorf("Text = %q, want cleared", p.Text)
	}
}
