package profiler

import (
	"context"
	"testing"
)

func TestCaptureInvalidPID(t *testing.T) {
	for _, pt := range []ProfileType{ProfileHeap, ProfileCPU, ProfileGoroutine, ProfileSample} {
		if _, err := Capture(context.Background(), 0, pt); err == nil {
			t.Errorf("pid 0 should error for %s", pt)
		}
		if _, err := Capture(context.Background(), -1, pt); err == nil {
			t.Errorf("pid -1 should error for %s", pt)
		}
	}
}

func TestCaptureUnknownType(t *testing.T) {
	if _, err := Capture(context.Background(), 1234, ProfileType("bogus")); err == nil {
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

func TestParseSampleExtractsFrames(t *testing.T) {
	text := `Sample analysis of process 1234:
  0x1000  main.workLoop
  0x2000  main.handler
  0x3000  main.parse
`
	syms := parseSample(text)
	if len(syms) < 3 {
		t.Fatalf("parseSample returned %d symbols, want >= 3", len(syms))
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