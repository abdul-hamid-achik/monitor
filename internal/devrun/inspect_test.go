package devrun

import (
	"bytes"
	"context"
	"sync"
	"testing"
)

func TestBannerScanningWriterPassesBytesThroughUnchanged(t *testing.T) {
	var real bytes.Buffer
	w := &bannerScanningWriter{Real: &real}
	msg := "ordinary child output\nmore output\n"
	n, err := w.Write([]byte(msg))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(msg) {
		t.Errorf("Write returned n=%d, want %d", n, len(msg))
	}
	if real.String() != msg {
		t.Errorf("real writer = %q, want the exact bytes written (never altered)", real.String())
	}
}

// TestBannerScanningWriterFindsNodeStyleBanner also pins down that
// FindPID/OnBanner run OFF the Write() call itself (see bannerScanningWriter's
// own doc comment on why): WG lets the test wait out that goroutine
// deterministically instead of racing it.
func TestBannerScanningWriterFindsNodeStyleBanner(t *testing.T) {
	var real bytes.Buffer
	var got []InspectorBanner
	var wg sync.WaitGroup
	w := &bannerScanningWriter{
		Real:    &real,
		Ctx:     context.Background(),
		WG:      &wg,
		FindPID: func(context.Context, int) (int32, bool) { return 4242, true },
		OnBanner: func(b InspectorBanner) {
			got = append(got, b)
		},
	}
	line := "Debugger listening on ws://127.0.0.1:9239/fa0d31b7-1aa5-4e69-985b-2b3eaeb227aa\n"
	if _, err := w.Write([]byte(line)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	wg.Wait()
	if len(got) != 1 {
		t.Fatalf("banners found = %d, want 1", len(got))
	}
	if got[0].Port != 9239 {
		t.Errorf("Port = %d, want 9239", got[0].Port)
	}
	if !got[0].PIDKnown || got[0].PID != 4242 {
		t.Errorf("PID/PIDKnown = %d/%v, want 4242/true", got[0].PID, got[0].PIDKnown)
	}
	if got[0].WS != "ws://127.0.0.1:9239/fa0d31b7-1aa5-4e69-985b-2b3eaeb227aa" {
		t.Errorf("WS = %q, want the full ws:// URL preserved", got[0].WS)
	}
}

// TestBannerScanningWriterFindsMultipleBanners covers a wrapper (yarn/npm)
// re-execing into node: each process that actually opens an inspector
// prints its own banner line, and every one must be reported, not just the
// first.
func TestBannerScanningWriterFindsMultipleBanners(t *testing.T) {
	var real bytes.Buffer
	var mu sync.Mutex
	var ports []int
	var wg sync.WaitGroup
	w := &bannerScanningWriter{
		Real: &real,
		Ctx:  context.Background(),
		WG:   &wg,
		OnBanner: func(b InspectorBanner) {
			mu.Lock()
			ports = append(ports, b.Port)
			mu.Unlock()
		},
	}
	text := "Debugger listening on ws://127.0.0.1:9229/uuid-1\n" +
		"some readiness text\n" +
		"Debugger listening on ws://127.0.0.1:9230/uuid-2\n"
	if _, err := w.Write([]byte(text)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(ports) != 2 {
		t.Fatalf("ports = %v, want 2 entries", ports)
	}
	// Each banner is handled on its OWN goroutine (see bannerScanningWriter's
	// doc comment), so the two ports can complete in either order; sort
	// before comparing rather than assuming line order survives.
	if (ports[0] != 9229 && ports[0] != 9230) || (ports[1] != 9229 && ports[1] != 9230) || ports[0] == ports[1] {
		t.Errorf("ports = %v, want {9229, 9230} in some order", ports)
	}
}

// TestBannerScanningWriterHandlesSplitAcrossWrites covers a banner line
// split across two separate Write calls (a chunk boundary mid-line) --
// realistic given pump.go's copyStream reads in fixed-size chunks.
func TestBannerScanningWriterHandlesSplitAcrossWrites(t *testing.T) {
	var real bytes.Buffer
	var got []InspectorBanner
	var wg sync.WaitGroup
	w := &bannerScanningWriter{
		Real: &real,
		Ctx:  context.Background(),
		WG:   &wg,
		OnBanner: func(b InspectorBanner) {
			got = append(got, b)
		},
	}
	full := "Debugger listening on ws://127.0.0.1:9241/859f9e37-4f33-45c8-83b2-c3f7c2c2da0d\n"
	mid := len(full) / 2
	if _, err := w.Write([]byte(full[:mid])); err != nil {
		t.Fatalf("Write (first half): %v", err)
	}
	if _, err := w.Write([]byte(full[mid:])); err != nil {
		t.Fatalf("Write (second half): %v", err)
	}
	wg.Wait()
	if len(got) != 1 || got[0].Port != 9241 {
		t.Errorf("banners = %+v, want exactly one banner on port 9241", got)
	}
}

func TestBannerScanningWriterIgnoresNonBannerLines(t *testing.T) {
	var real bytes.Buffer
	called := false
	w := &bannerScanningWriter{
		Real:     &real,
		OnBanner: func(InspectorBanner) { called = true },
	}
	if _, err := w.Write([]byte("just some ordinary stderr output\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if called {
		t.Error("OnBanner fired for a line with no inspector banner")
	}
}

func TestBannerScanningWriterUnresolvedPortStillReportsBanner(t *testing.T) {
	var real bytes.Buffer
	var got []InspectorBanner
	var wg sync.WaitGroup
	w := &bannerScanningWriter{
		Real:    &real,
		Ctx:     context.Background(),
		WG:      &wg,
		FindPID: func(context.Context, int) (int32, bool) { return 0, false },
		OnBanner: func(b InspectorBanner) {
			got = append(got, b)
		},
	}
	if _, err := w.Write([]byte("Debugger listening on ws://127.0.0.1:9229/uuid\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	wg.Wait()
	if len(got) != 1 {
		t.Fatalf("banners = %d, want 1 (still recorded even with no resolvable pid)", len(got))
	}
	if got[0].PIDKnown {
		t.Error("PIDKnown = true, want false: FindPID reported no owner")
	}
	if got[0].Port != 9229 {
		t.Errorf("Port = %d, want 9229 (still parsed even without a resolvable pid)", got[0].Port)
	}
}

func TestParseWSPort(t *testing.T) {
	port, ok := parseWSPort("ws://127.0.0.1:9229/uuid")
	if !ok || port != 9229 {
		t.Errorf("parseWSPort = (%d, %v), want (9229, true)", port, ok)
	}
	if _, ok := parseWSPort("not-a-url"); ok {
		t.Error("parseWSPort should fail on a non-ws:// string with no port")
	}
}
