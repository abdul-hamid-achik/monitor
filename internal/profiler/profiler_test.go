package profiler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/pprof"
	"os"
	"strings"
	"testing"

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
	if len(p.Symbols) == 0 {
		t.Fatal("expected non-empty symbols from a real heap profile")
	}
	if p.Text == "" {
		t.Error("expected Profile.Text to hold a readable top-N summary now that heap has no ?debug=1 text dump")
	}
	for _, s := range p.Symbols {
		if s.Func == "unknown" {
			t.Errorf("real heap profile produced an unknown symbol: %+v", s)
		}
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
	// "" defaults to localhost:6060.
	cases := map[ProfileType]string{
		ProfileCPU:       "http://localhost:6060/debug/pprof/profile?seconds=1",
		ProfileHeap:      "http://localhost:6060/debug/pprof/heap",
		ProfileGoroutine: "http://localhost:6060/debug/pprof/goroutine",
	}
	for pt, want := range cases {
		if got := pprofURL("", pt); got != want {
			t.Errorf("pprofURL(%q, %s) = %q, want %q", "", pt, got, want)
		}
	}
	// A custom address is honored.
	if got := pprofURL("10.0.0.5:7070", ProfileHeap); got != "http://10.0.0.5:7070/debug/pprof/heap" {
		t.Errorf("custom addr pprofURL = %q", got)
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
