// inspect.go implements E3.3b's `monitor run --inspect`: appending
// --inspect=127.0.0.1:0 to NODE_OPTIONS (node and deno both honor it --
// verified live, see docs/contracts/local-sentry-naming.md §8) and
// watching the scanned stream(s) for every "Debugger listening on
// ws://host:port/uuid" banner it produces, mapping each one to its owning
// pid via the listening port (never trusted from the banner text itself,
// which names no pid) so the launch registry (registry.go) can record it
// for a later `monitor hot <service>`.
package devrun

import (
	"bytes"
	"context"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"sync"

	"github.com/abdul-hamid-achik/monitor/internal/procbind"
)

// inspectorBannerPattern matches Node/Deno's inspector startup banner. Both
// runtimes print an identical shape (verified live: Node 26.7.0 and Deno
// 2.9.7 under NODE_OPTIONS=--inspect=127.0.0.1:0); a wrapper that itself
// re-execs into node (yarn/npm spawning a node child) produces one banner
// PER process that actually opened an inspector, since each is a real,
// separate "Debugger listening" print from its own V8 instance.
var inspectorBannerPattern = regexp.MustCompile(`Debugger listening on (ws://\S+)`)

// InspectorBanner is one parsed banner line, with its owning pid resolved
// from the port it names.
type InspectorBanner struct {
	WS   string
	Port int
	// PID and PIDKnown: see portOwnerFunc's doc comment on bannerScanningWriter.
	PID      int32
	PIDKnown bool
}

// portOwnerFunc matches procbind.FindListenerPID's signature; tests inject
// a fake one instead of touching the real process/socket table.
type portOwnerFunc func(ctx context.Context, port int) (int32, bool)

// bannerScanCarryMax bounds bannerScanningWriter's carry buffer -- the
// tail of the most recent write that had no newline yet, kept only so a
// banner line split across two chunk boundaries is still recognized.
// Real inspector banners are under 100 bytes; this is generous headroom,
// not a real limit on anything a monitored process legitimately prints
// (ordinary, non-banner output past this bound is simply never
// pattern-matched, which is correct -- it was never a banner to begin with).
const bannerScanCarryMax = 4096

// bannerScanningWriter tees every byte written to Real UNCHANGED -- the
// terminal passthrough this whole package exists to never slow down or
// alter -- while separately watching for a complete "Debugger listening on
// ws://..." line, reporting each one found to OnBanner. This is
// deliberately NOT hung off pump.go's line-splitting (extractLines/
// sendLine, which feeds the crash detector and can DROP a line under
// backpressure): an inspector banner must never be missed just because the
// detector's bounded channel happened to be full at that exact moment, so
// this scans directly off the same bytes being written to the terminal,
// independent of the detector's channel entirely.
//
// FindPID and OnBanner are both called on a SEPARATE goroutine (see
// scanLine), never inline within Write: FindPID (production: procbind.
// FindListenerPID) enumerates the host's TCP connection table, which on
// macOS gopsutil implements by shelling out to lsof (internal/profiler/
// ownership.go's own listTCPConnections doc comment says as much) --
// tens of milliseconds of subprocess/syscall latency this package's own
// golden rule ("monitoring must never be able to make the monitored
// process slower") cannot pay for on the copy goroutine's hot path, the
// exact same reasoning detector.go's own record/flush already keeps off
// the line-consuming loop via its independent goroutine. WG, when set,
// is used to let a caller (devrun.go's Run) wait out every still-running
// banner-handling goroutine before doing anything that assumes they have
// all finished (removing the launch registry entry on exit, in
// particular -- without this, a banner detected right as the child exits
// could still be mid-write to a file Run's own cleanup just deleted,
// resurrecting it with no one left to clean it up again).
type bannerScanningWriter struct {
	Real     io.Writer
	OnBanner func(InspectorBanner)
	FindPID  portOwnerFunc
	Ctx      context.Context
	WG       *sync.WaitGroup

	carry []byte
}

func (w *bannerScanningWriter) Write(p []byte) (int, error) {
	n, err := w.Real.Write(p)
	if w.OnBanner != nil {
		w.scan(p)
	}
	return n, err
}

func (w *bannerScanningWriter) scan(p []byte) {
	buf := append(w.carry, p...)
	for {
		idx := bytes.IndexByte(buf, '\n')
		if idx < 0 {
			break
		}
		line := buf[:idx]
		buf = buf[idx+1:]
		w.scanLine(line)
	}
	if len(buf) > bannerScanCarryMax {
		buf = buf[len(buf)-bannerScanCarryMax:]
	}
	w.carry = append([]byte(nil), buf...)
}

func (w *bannerScanningWriter) scanLine(line []byte) {
	m := inspectorBannerPattern.FindSubmatch(line)
	if m == nil {
		return
	}
	ws := string(m[1])
	port, _ := parseWSPort(ws) // 0 when unparseable; still reported below
	if w.WG != nil {
		w.WG.Add(1)
	}
	go func() {
		if w.WG != nil {
			defer w.WG.Done()
		}
		banner := InspectorBanner{WS: ws, Port: port}
		if port > 0 && w.FindPID != nil {
			if pid, ok := w.FindPID(w.Ctx, port); ok {
				banner.PID = pid
				banner.PIDKnown = true
			}
		}
		w.OnBanner(banner)
	}()
}

// parseWSPort extracts the port from an inspector ws:// URL
// ("ws://127.0.0.1:9229/uuid" -> 9229).
func parseWSPort(ws string) (int, bool) {
	u, err := url.Parse(ws)
	if err != nil {
		return 0, false
	}
	portStr := u.Port()
	if portStr == "" {
		return 0, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, false
	}
	return port, true
}

// defaultPortOwner is procbind.FindListenerPID, the production portOwnerFunc.
func defaultPortOwner(ctx context.Context, port int) (int32, bool) {
	return procbind.FindListenerPID(ctx, port)
}
