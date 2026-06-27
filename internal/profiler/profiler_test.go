package profiler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/pprof"
	"strings"
	"testing"
)

// TestCaptureHeapOverHTTP exercises the real scrape path: Capture builds the
// /debug/pprof/heap?debug=1 URL, fetches it over HTTP, and parses frames. We
// serve a live pprof endpoint from this test process so the data is real.
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
		if p.Text == "" {
			t.Errorf("%s: expected profile text", pt)
		}
	}

	// The debug=1 heap profile carries a recognizable header.
	p, _ := Capture(context.Background(), 1, ProfileHeap, addr)
	if !strings.Contains(p.Text, "heap profile") {
		t.Errorf("heap text missing 'heap profile' header:\n%.200s", p.Text)
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

func TestParsePprofExtractsFrames(t *testing.T) {
	text := `File: foo.go
Build ID: abc123
Type: cpu
Time: now
Duration: 1s

     100ms  40%  main.runLoop  foo.go:42  0x1234
      50ms  20%  main.handler  bar.go:99  0x2345
`
	syms := parsePprof(text)
	if len(syms) < 2 {
		t.Fatalf("parsePprof returned %d symbols, want >= 2", len(syms))
	}
	if syms[0].Func != "main.runLoop" || syms[0].Line != 42 {
		t.Errorf("first symbol = %+v", syms[0])
	}
}

// TestParsePprofParsesDebug1Heap feeds real /debug/pprof/heap?debug=1 text
// (tab-separated "# 0xADDR func+0xOFF /path/file.go:LINE" frames). The old
// parser only saw protobuf on the wire and returned nothing.
func TestParsePprofParsesDebug1Heap(t *testing.T) {
	text := "heap profile: 3: 4128 [3: 4129] @ heap/1048576\n" +
		"1: 32 [1: 32] @ 0x10461a9cc 0x10461b688\n" +
		"#\t0x10461a9cb\tsyscall.anyToSockaddr+0x9b\t/go/src/syscall/syscall_bsd.go:257\n" +
		"#\t0x10461b687\tsyscall.Getpeername+0x77\t/go/src/syscall/syscall_unix.go:309\n"
	syms := parsePprof(text)
	if len(syms) < 2 {
		t.Fatalf("parsePprof returned %d symbols, want >= 2", len(syms))
	}
	if syms[0].Func != "syscall.anyToSockaddr" {
		t.Errorf("func = %q, want syscall.anyToSockaddr (the +0x offset should be stripped)", syms[0].Func)
	}
	if syms[0].Line != 257 {
		t.Errorf("line = %d, want 257", syms[0].Line)
	}
}

// TestParsePprofTopExtractsFrames uses real `go tool pprof -top -lines`
// output (the CPU-profile symbolication path).
func TestParsePprofTopExtractsFrames(t *testing.T) {
	text := `File: cpuprof
Type: cpu
Duration: 1.11s, Total samples = 2990ms (268.25%)
Showing nodes accounting for 2990ms, 100% of 2990ms total
      flat  flat%   sum%        cum   cum%
    2630ms 87.96% 87.96%     2990ms   100%  main.burn /tmp/cpuprof.go:11
     360ms 12.04%   100%      360ms 12.04%  runtime.asyncPreempt /go/src/runtime/preempt_arm64.s:7
`
	syms := parsePprofTop(text)
	if len(syms) != 2 {
		t.Fatalf("parsePprofTop returned %d symbols, want 2; %+v", len(syms), syms)
	}
	if syms[0].Func != "main.burn" || syms[0].Line != 11 {
		t.Errorf("first symbol = %+v, want main.burn :11", syms[0])
	}
	if syms[0].Weight != 87.96 {
		t.Errorf("first symbol weight = %v, want 87.96 (the flat%%)", syms[0].Weight)
	}
	if syms[1].Func != "runtime.asyncPreempt" {
		t.Errorf("second symbol = %+v, want runtime.asyncPreempt", syms[1])
	}
}

// TestParseSampleExtractsFrames uses a real macOS `sample` call-graph dump
// (captured from `sample <pid> 1`). The old parser matched only lines
// starting with "0x", which real `sample` output never produces.
func TestParseSampleExtractsFrames(t *testing.T) {
	text := `Call graph:
    869 Thread_331472   DispatchQueue_1: com.apple.main-thread  (serial)
      869 start  (in dyld) + 6992  [0x1804abe00]
        869 ???  (in sleep)  load address 0x1048e0000 + 0x740  [0x1048e0740]
          869 nanosleep  (in libsystem_c.dylib) + 220  [0x180705cc0]
            869 __semwait_signal  (in libsystem_kernel.dylib) + 8  [0x180829308]

Sort by top of stack, same collapsed (when >= 5):
        __semwait_signal  (in libsystem_kernel.dylib)        869
`
	syms := parseSample(text)
	byName := map[string]Symbol{}
	for _, s := range syms {
		byName[s.Func] = s
	}
	for _, want := range []string{"start", "nanosleep", "__semwait_signal"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("parseSample missing frame %q; got %+v", want, syms)
		}
	}
	if s, ok := byName["nanosleep"]; ok && s.File != "libsystem_c.dylib" {
		t.Errorf("nanosleep image = %q, want libsystem_c.dylib", s.File)
	}
	// The Thread header line must not be parsed as a frame.
	for _, bad := range []string{"Thread_331472", "DispatchQueue_1:"} {
		if _, ok := byName[bad]; ok {
			t.Errorf("parseSample wrongly parsed header token %q as a frame", bad)
		}
	}
}

// TestPprofURLMapsCPUToProfile is a regression for the bug where
// ProfileCPU built /debug/pprof/cpu (a 404 — net/http/pprof has no "cpu"
// handler) instead of /debug/pprof/profile.
func TestPprofURLMapsCPUToProfile(t *testing.T) {
	// "" defaults to localhost:6060.
	cases := map[ProfileType]string{
		ProfileCPU:       "http://localhost:6060/debug/pprof/profile?seconds=1",
		ProfileHeap:      "http://localhost:6060/debug/pprof/heap?debug=1",
		ProfileGoroutine: "http://localhost:6060/debug/pprof/goroutine?debug=1",
	}
	for pt, want := range cases {
		if got := pprofURL("", pt); got != want {
			t.Errorf("pprofURL(%q, %s) = %q, want %q", "", pt, got, want)
		}
	}
	// A custom address is honored.
	if got := pprofURL("10.0.0.5:7070", ProfileHeap); got != "http://10.0.0.5:7070/debug/pprof/heap?debug=1" {
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
