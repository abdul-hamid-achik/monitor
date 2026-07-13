package reload

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeReloader is a Reloader that counts hits and can be programmed to
// return an error.
type fakeReloader struct {
	count atomic.Int32
	err   error
}

func (f *fakeReloader) Reload() error {
	f.count.Add(1)
	return f.err
}

// freePort asks the kernel for an unused port and returns it. We use
// port 0 in NewServer for these tests so two tests never collide.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	defer func() {
		if err := ln.Close(); err != nil {
			t.Errorf("close free-port listener: %v", err)
		}
	}()
	return ln.Addr().String()
}

func closeResponseBody(t *testing.T, resp *http.Response) {
	t.Helper()
	if err := resp.Body.Close(); err != nil {
		t.Errorf("close response body: %v", err)
	}
}

func shutdownServer(t *testing.T, s *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

// waitForServer polls /healthz until it returns 200 or the timeout
// elapses. The HTTP listener takes a few microseconds to come up after
// Start, so we don't assert synchronously.
func waitForServer(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			closeResponseBody(t, resp)
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become healthy within %s", addr, timeout)
}

// TestServerHealthzReturnsOK verifies the /healthz endpoint responds
// before any reload is issued.
func TestServerHealthzReturnsOK(t *testing.T) {
	addr := freePort(t)
	r := &fakeReloader{}
	s := NewServer(addr, r)
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { shutdownServer(t, s) })
	waitForServer(t, addr, time.Second)

	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer closeResponseBody(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("GET /healthz status=%d, want 200", resp.StatusCode)
	}
}

// TestServerReloadCallsReloader verifies the central contract: POST
// /reload invokes the Reloader and returns 200 OK.
func TestServerReloadCallsReloader(t *testing.T) {
	addr := freePort(t)
	r := &fakeReloader{}
	s := NewServer(addr, r)
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { shutdownServer(t, s) })
	waitForServer(t, addr, time.Second)

	resp, err := http.Post("http://"+addr+"/reload", "application/json", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST /reload: %v", err)
	}
	defer closeResponseBody(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("POST /reload status=%d, want 200", resp.StatusCode)
	}
	if r.count.Load() != 1 {
		t.Fatalf("Reloader.Reload called %d times, want 1", r.count.Load())
	}
}

// TestServerReloadIdempotent verifies multiple /reload hits all fire
// the Reloader (no accidental dedup).
func TestServerReloadIdempotent(t *testing.T) {
	addr := freePort(t)
	r := &fakeReloader{}
	s := NewServer(addr, r)
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { shutdownServer(t, s) })
	waitForServer(t, addr, time.Second)

	for i := 0; i < 5; i++ {
		resp, err := http.Post("http://"+addr+"/reload", "application/json", strings.NewReader(""))
		if err != nil {
			t.Fatalf("POST /reload #%d: %v", i, err)
		}
		closeResponseBody(t, resp)
	}
	if got := r.count.Load(); got != 5 {
		t.Fatalf("Reloader.Reload called %d times, want 5", got)
	}
}

// TestServerReloadMethodNotAllowed verifies the endpoint refuses
// non-POST requests with 405 (so a curious user curl'ing without
// -X POST gets a clear error).
func TestServerReloadMethodNotAllowed(t *testing.T) {
	addr := freePort(t)
	r := &fakeReloader{}
	s := NewServer(addr, r)
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { shutdownServer(t, s) })
	waitForServer(t, addr, time.Second)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req, _ := http.NewRequest(method, "http://"+addr+"/reload", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s /reload: %v", method, err)
		}
		closeResponseBody(t, resp)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s /reload status=%d, want 405", method, resp.StatusCode)
		}
	}
	if r.count.Load() != 0 {
		t.Fatalf("Reloader.Reload should not be called on non-POST; got %d", r.count.Load())
	}
}

// TestServerReloadErrorPropagates verifies that a Reloader error
// surfaces as a 500 with a useful body, so the caller can debug.
func TestServerReloadErrorPropagates(t *testing.T) {
	addr := freePort(t)
	r := &fakeReloader{err: fmt.Errorf("disk full")}
	s := NewServer(addr, r)
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { shutdownServer(t, s) })
	waitForServer(t, addr, time.Second)

	resp, err := http.Post("http://"+addr+"/reload", "application/json", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST /reload: %v", err)
	}
	defer closeResponseBody(t, resp)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500", resp.StatusCode)
	}
}

// TestServerConcurrentReloads exercises the goroutine + atomic counter
// under load. With many parallel POSTs the count must equal the
// number of successful requests; no lost updates, no races.
func TestServerConcurrentReloads(t *testing.T) {
	addr := freePort(t)
	r := &fakeReloader{}
	s := NewServer(addr, r)
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { shutdownServer(t, s) })
	waitForServer(t, addr, time.Second)

	const N = 50
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Post("http://"+addr+"/reload", "application/json", strings.NewReader(""))
			if err != nil {
				return
			}
			closeResponseBody(t, resp)
		}()
	}
	wg.Wait()
	if got := r.count.Load(); got != N {
		t.Fatalf("Reloader.Reload called %d times, want %d", got, N)
	}
}

// TestServerStartFailsOnPortConflict verifies the listener refuses to
// start when the configured port is already in use.
func TestServerStartFailsOnPortConflict(t *testing.T) {
	addr := freePort(t)
	// Hold the port with a competing listener.
	hold, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	defer func() {
		if err := hold.Close(); err != nil {
			t.Errorf("close competing listener: %v", err)
		}
	}()

	s := NewServer(addr, &fakeReloader{})
	if err := s.Start(); err == nil {
		t.Fatalf("Start should fail when port is in use; got nil error")
	}
}

// TestAddrResolvesPortZero is a regression for Addr() echoing the
// configured address: a caller passing port 0 was told to hit port 0
// instead of the kernel-assigned port.
func TestAddrResolvesPortZero(t *testing.T) {
	s := NewServer("127.0.0.1:0", &fakeReloader{})
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer shutdownServer(t, s)

	_, port, err := net.SplitHostPort(s.Addr())
	if err != nil {
		t.Fatalf("Addr() = %q is not host:port: %v", s.Addr(), err)
	}
	if port == "0" || port == "" {
		t.Errorf("Addr() = %q still reports port 0; want the resolved port", s.Addr())
	}
}
