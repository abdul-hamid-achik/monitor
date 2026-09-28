// Package tuistudio is an EXPERIMENTAL alternate front-end for `monitor
// studio`, rendered by Tuimark (github.com/abdul-hamid-achik/tuimark)
// instead of Bubble Tea. It reuses the exact same data sources as the
// production studio (internal/collector, internal/kill,
// internal/config, internal/history, internal/temperature,
// internal/analyzer) read-only: this package holds no layout, width,
// padding, truncation, or column code of its own. The view lives entirely
// in the embedded studio.tui/studio.tcss; this Go code only binds derived
// data (booleans, labels, pre-formatted numbers) and implements the named
// actions the document references.
//
// It is reached with `monitor studio --tuimark` and is additive: the
// default Bubble Tea studio (internal/ui/studio) is unchanged.
package tuistudio

import (
	"context"
	"embed"
	"fmt"
	"os"
	"sync"

	"github.com/abdul-hamid-achik/tuimark"
)

//go:embed studio.tui studio.tcss
var viewFS embed.FS

// Options controls the experimental Tuimark studio, mirroring
// internal/ui/studio.Options where the same policy applies.
type Options struct {
	// DisableTemperatureSource prevents the privileged powermetrics-backed
	// source from starting; the collector's own CPU-load estimate remains
	// available. Same flag as `monitor studio --no-temperature-source`.
	DisableTemperatureSource bool
	// Reloader, when non-nil, is attached to the running app so an
	// external `monitor reload` (via --reload-server) can inject a real
	// refresh, exactly like internal/ui/studio.Options.Reloader.
	Reloader *Reloader
	// Fixture swaps every data source (metrics, kill, settings storage,
	// history) for a deterministic, offline fixture: no real syscalls
	// beyond what math/rand needs, no ~/.config/monitor/config.json
	// read or written, no history.veclite store opened, and no real
	// process ever signaled. Used by `--fixture`, by Go tests, and by the
	// glyphrun specs.
	Fixture bool
}

// Reloader safely bridges an external /reload request (internal/reload,
// via `monitor studio --tuimark --reload-server`) to a live studio, the
// same role internal/ui/studio.ProgramReloader plays for the Bubble Tea
// studio. It is inactive before RunWithOptions starts and after it
// returns.
type Reloader struct {
	mu sync.RWMutex
	s  *studio
}

// NewReloader creates a bridge suitable for internal/reload.Server.
func NewReloader() *Reloader { return &Reloader{} }

// Reload takes an immediate sample and republishes every bound field, the
// same effect as pressing "r" inside the running app. tuimark's Set is
// goroutine-safe, so this may run concurrently with the app's own input
// loop.
func (r *Reloader) Reload() error {
	r.mu.RLock()
	s := r.s
	r.mu.RUnlock()
	if s == nil {
		return fmt.Errorf("tuistudio: the app is not running")
	}
	return s.refreshNow()
}

func (r *Reloader) attach(s *studio) {
	r.mu.Lock()
	r.s = s
	r.mu.Unlock()
}

func (r *Reloader) detach(s *studio) {
	r.mu.Lock()
	if r.s == s {
		r.s = nil
	}
	r.mu.Unlock()
}

// Run launches the experimental studio with the interactive defaults.
func Run() error { return RunWithOptions(Options{}) }

// RunWithOptions launches the experimental studio, honoring the same
// --no-temperature-source policy and --reload-server bridge as the
// default studio. A failure (no terminal on stdin, a view that does not
// load) is reported on stderr, as internal/ui/studio.RunWithOptions does:
// `monitor studio` itself exits 0 either way.
func RunWithOptions(opts Options) error {
	if err := run(opts); err != nil {
		fmt.Fprintf(os.Stderr, "Error running monitor studio --tuimark: %v\n", err)
		return err
	}
	return nil
}

func run(opts Options) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s, err := newStudio(ctx, opts)
	if err != nil {
		return err
	}
	s.cancel = cancel
	s.start(ctx)

	if opts.Reloader != nil {
		opts.Reloader.attach(s)
		defer opts.Reloader.detach(s)
	}
	return s.ui.Run(os.Stdout)
}

// Dump builds the studio (binding one immediate sample, exactly as
// RunWithOptions would before its first frame) and renders it at cols x
// rows without starting the background sampling loop or reading stdin.
// This is `monitor studio --tuimark --dump COLSxROWS`'s test hook: pass
// Fixture: true for a result that never depends on the host it runs on.
// The temperature source is always disabled here (never shell out to
// `sudo powermetrics` for a one-shot dump).
func Dump(opts Options, cols, rows int) (*tuimark.Dump, error) {
	opts.DisableTemperatureSource = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s, err := newStudio(ctx, opts)
	if err != nil {
		return nil, err
	}
	defer func() {
		if s.cancel != nil {
			s.cancel()
		}
	}()
	return s.ui.Dump(cols, rows)
}

// loadEmbeddedView loads studio.tui straight out of the embedded file
// system with tuimark.LoadFS (0.3a): a relative <style src="studio.tcss"/>
// resolves inside viewFS itself, next to the document, so this no longer
// needs to spill the embedded files to a throwaway temp directory first.
func loadEmbeddedView() (*tuimark.App, error) {
	return tuimark.LoadFS(viewFS, "studio.tui")
}
