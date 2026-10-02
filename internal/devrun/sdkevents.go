package devrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/events"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/stacktrace"
	"github.com/abdul-hamid-achik/monitor/sdk"
)

// eventPollInterval is how often a launch's events directory is read. An
// SDK writes each event synchronously, so the files are already there; this
// only bounds how late the detector sees them, well inside coalesceWindow,
// so an SDK event and the same crash parsed from stderr still meet in one
// coalescing window.
const eventPollInterval = 200 * time.Millisecond

// Notes printed when --probes cannot do everything it was asked to.
const (
	probesNoIssuesNote = "--probes has no effect with --no-issues: nothing would record what the SDK sees"
	probesBunSpaceNote = "monitor's state directory contains a space, which Bun's BUN_OPTIONS cannot carry as a path -- --probes covers Node and Python but not Bun this launch; move XDG_STATE_HOME to a path with no spaces"
)

// sdkAugmentation is applySDK's result: the child's env and the notes to
// print.
type sdkAugmentation struct {
	env   []string
	notes []string
}

// applySDK wires monitor's own SDKs into the child's environment:
//
//   - MONITOR_EVENTS_DIR always points at this launch's private events
//     directory, so an app that imports an SDK explicitly reports to this
//     launch (replacing whatever an outer launch exported);
//   - with --probes, the embedded SDKs load themselves at startup, by
//     environment only and only by appending: NODE_OPTIONS gets
//     `--require <auto.cjs>`, BUN_OPTIONS `--preload <auto.cjs>`, and
//     PYTHONPATH gets the bootstrap directory in FRONT (the one prepend:
//     the first sitecustomize on the path is the only one Python runs, and
//     the bootstrap runs the one it shadowed).
func applySDK(env []string, opts Options, eventsDir string) (sdkAugmentation, error) {
	out := sdkAugmentation{env: setEnvVar(env, events.EnvDir, eventsDir)}
	if !opts.Probes {
		return out, nil
	}
	paths, err := sdk.Materialize("")
	if err != nil {
		return out, err
	}
	out.env = appendRuntimeOption(out.env, "NODE_OPTIONS", "--require "+quoteForNodeOptions(paths.NodeAuto))
	if hasWhitespace(paths.NodeAuto) {
		out.notes = append(out.notes, probesBunSpaceNote)
	} else {
		out.env = appendRuntimeOption(out.env, "BUN_OPTIONS", "--preload "+paths.NodeAuto)
	}
	out.env = prependPathVar(out.env, "PYTHONPATH", paths.PythonBootstrap)
	return out, nil
}

// setEnvVar sets name=value in env, replacing every existing entry.
func setEnvVar(env []string, name, value string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); k == name {
			continue
		}
		out = append(out, kv)
	}
	return append(out, name+"="+value)
}

// prependPathVar puts dir first in the path-list variable name, keeping
// whatever it already held after it.
func prependPathVar(env []string, name, dir string) []string {
	for i, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if k != name {
			continue
		}
		if v == "" {
			env[i] = name + "=" + dir
		} else {
			env[i] = name + "=" + dir + string(os.PathListSeparator) + v
		}
		return env
	}
	return append(env, name+"="+dir)
}

// eventWatcher feeds the SDK events a launch's processes write into the
// detector while the child runs, and once more after it exits.
type eventWatcher struct {
	dir  string
	det  *detector
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

func startEventWatcher(ctx context.Context, dir string, det *detector) *eventWatcher {
	w := &eventWatcher{dir: dir, det: det, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(eventPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-ticker.C:
				w.scan(ctx)
			}
		}
	}()
	return w
}

// scan hands every committed event file to the detector and removes it. A
// file that can never be recorded is quarantined, never retried.
func (w *eventWatcher) scan(ctx context.Context) {
	paths, err := events.Pending(w.dir)
	if err != nil {
		return
	}
	for _, path := range paths {
		ev, err := events.Load(path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				_ = events.Reject(path)
			}
			continue
		}
		p, ok := events.Prepare(ev, w.det.scrubber)
		if !ok {
			_ = events.Reject(path)
			continue
		}
		w.det.observeEvent(ctx, p)
		_ = os.Remove(path)
	}
}

// finish stops the poller, reads what the child wrote before it exited,
// and removes the launch's directory. Anything that lands after that (a
// grandchild outliving the launch) goes to the global inbox instead: the
// SDKs fall back to it when this directory is gone.
func (w *eventWatcher) finish(ctx context.Context) {
	w.once.Do(func() {
		close(w.stop)
		<-w.done
		w.scan(ctx)
		handOff(w.dir)
	})
}

// handOff moves any event file still in dir to the global inbox, then
// removes dir.
func handOff(dir string) {
	if left, _ := events.Pending(dir); len(left) > 0 {
		if inbox, err := events.InboxDir(); err == nil && events.EnsureDir(inbox) == nil {
			for _, path := range left {
				_ = os.Rename(path, filepath.Join(inbox, filepath.Base(path)))
			}
		}
	}
	_ = os.RemoveAll(dir)
}

// observeEvent is observe's twin for an SDK event. It joins the same
// coalescing windows, keyed by the same FingerprintV2, so the crash an SDK
// saw and the one the process printed on stderr become ONE write: the
// count is the larger of the two sources' counts (each saw every repeat),
// and the SDK's data wins (its exact handled flag, unabridged frames,
// breadcrumbs and tags), while the stream's block still provides the
// DedupeKey that nested launches fold on.
func (d *detector) observeEvent(ctx context.Context, p events.Prepared) {
	ex := p.Exception
	stacktrace.ApplyGitRoot(&ex, d.opts.id.GitRoot)
	fingerprint := issues.FingerprintV2Exception(ex, d.opts.id.Slug)
	observedAt := ex.ObservedAt
	if observedAt.IsZero() {
		observedAt = time.Now()
	}

	d.mu.Lock()
	d.sdkEvents++
	if entry, exists := d.pending[fingerprint]; exists {
		entry.sdkCount++
		if entry.sdk == nil {
			entry.ex = ex
			entry.sdk = &p
		}
		d.mu.Unlock()
		return
	}
	d.openLocked(ctx, fingerprint, &coalesceEntry{ex: ex, sdk: &p, sdkCount: 1, observedAt: observedAt})
	d.mu.Unlock()
}
