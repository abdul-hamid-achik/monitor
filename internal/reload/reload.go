// Package reload exposes a tiny HTTP endpoint inside the running TUI
// binary that external processes can hit to trigger an in-process
// refresh (e.g. re-read the log store after `monitor logs capture`).
//
// The endpoint is a POST to /reload on 127.0.0.1:7351 (a memorable
// port; not configurable today — it's a fixed local-only endpoint, not
// a network service). The handler closes over a Reloader; the binary
// that embeds this package registers a Reloader that knows what to
// invalidate. The endpoint is idempotent and safe to spam.
//
// SECURITY: the listener binds to 127.0.0.1, not 0.0.0.0; remote
// processes cannot reach it. No authentication is needed because the
// attacker model is "local user can already run any monitor command."
// If that changes, add a shared-secret header before exposing publicly.
package reload

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

const (
	// DefaultAddr is the listen address for the /reload endpoint. The
	// port (7351) is memorable and easy to remember for scripting.
	DefaultAddr = "127.0.0.1:7351"
)

// Reloader is the contract an embedding binary implements to react to
// /reload hits. The implementation runs synchronously inside the HTTP
// handler goroutine, so it must be fast (re-read a file, clear a
// cache, etc.) — never block on long-running I/O.
type Reloader interface {
	Reload() error
}

// NoopReloader is a Reloader that succeeds without doing anything.
// Useful for the bare TUI which doesn't (yet) cache anything that
// needs a manual refresh: the /reload endpoint still serves, the
// counter still increments, and operators can confirm the wire is
// working end-to-end without affecting the TUI's behavior.
type NoopReloader struct{}

func (NoopReloader) Reload() error { return nil }

// Server is a small HTTP server that exposes POST /reload. Embed it
// in the TUI binary via NewServer + Start; the endpoint stays up for
// the lifetime of the surrounding program.
type Server struct {
	addr   string
	srv    *http.Server
	loaded atomic.Int64 // count of /reload hits (visible via /healthz)
}

// NewServer builds a Server bound to addr (use DefaultAddr unless
// embedding in a context that needs a different port).
func NewServer(addr string, r Reloader) *Server {
	mux := http.NewServeMux()
	s := &Server{addr: addr}

	mux.HandleFunc("/reload", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed; use POST", http.StatusMethodNotAllowed)
			return
		}
		n := s.loaded.Add(1)
		if err := r.Reload(); err != nil {
			http.Error(w, fmt.Sprintf("reload failed: %v", err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// Use the stdlib fmt import above; explicit body so the JSON
		// is always valid even when a future change adds fields.
		if _, err := fmt.Fprintf(w, `{"ok":true,"count":%d}`, n); err != nil {
			return
		}
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := fmt.Fprintf(w, `{"ok":true,"count":%d}`, s.loaded.Load()); err != nil {
			return
		}
	})

	s.srv = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,
	}
	return s
}

// Start launches the listener in a background goroutine. Returns
// immediately. The first /reload hit's response time is bounded by
// the kernel's accept queue; the handler itself is O(1) for
// well-behaved Reloader implementations.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.addr, err)
	}
	// Record the resolved address so Addr() reports the kernel-assigned
	// port when the caller passed port 0.
	s.addr = ln.Addr().String()
	go func() {
		// http.ErrServerClosed is the expected shutdown signal; ignore.
		_ = s.srv.Serve(ln)
	}()
	return nil
}

// Addr returns the actual listen address (useful when the configured
// addr used port 0 to get a free one).
func (s *Server) Addr() string { return s.addr }

// Shutdown gracefully stops the server. Idempotent.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}
