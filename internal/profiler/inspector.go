package profiler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// DefaultInspectAddr is the conventional Node inspector port.
const DefaultInspectAddr = "127.0.0.1:9229"

// ProfileInspector captures a CPU profile from a Node/Bun/Deno process via
// the Chrome DevTools Protocol (CDP) inspector. It requires the target to
// have been started with --inspect or --inspect-brk.
//
// The flow: GET /json/list → ws connect → Profiler.enable → Profiler.start
// → wait → Profiler.stop → flatten the hierarchical profile into []Symbol
// with file:line, so codemap correlation works for JS runtimes (macOS
// `sample` frames lack file:line).
func ProfileInspector(ctx context.Context, pid int32, addr string, duration time.Duration) (Profile, error) {
	if addr == "" {
		addr = DefaultInspectAddr
	}
	if err := ValidateInspectorAddr(addr); err != nil {
		return Profile{PID: pid, Type: ProfileCPU, Method: "inspector_cpu", Taken: time.Now()}, err
	}
	if duration <= 0 {
		duration = 5 * time.Second
	}
	p := Profile{PID: pid, Type: ProfileCPU, Method: "inspector_cpu", Taken: time.Now()}

	wsURL, err := inspectorWebSocketURL(ctx, addr)
	if err != nil {
		return p, fmt.Errorf("inspector discovery on %s: %w", addr, err)
	}

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return p, fmt.Errorf("inspector ws connect: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(maxRawProfileBytes)

	// CDP command/response with incremental message IDs.
	msgID := 0
	send := func(method string, params map[string]any) error {
		msgID++
		req := map[string]any{"id": msgID, "method": method}
		if params != nil {
			req["params"] = params
		}
		data, _ := json.Marshal(req)
		return conn.Write(ctx, websocket.MessageText, data)
	}

	// Read until we get a response matching our ID (skip events).
	readResponse := func(wantID int) (json.RawMessage, error) {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return nil, err
			}
			var hdr struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error,omitempty"`
			}
			if err := json.Unmarshal(data, &hdr); err != nil {
				continue
			}
			if hdr.Error != nil && hdr.ID == wantID {
				return nil, fmt.Errorf("cdp error: %s", hdr.Error.Message)
			}
			if hdr.ID == wantID {
				return hdr.Result, nil
			}
		}
	}

	// 1. Enable + start the profiler.
	if err := send("Profiler.enable", nil); err != nil {
		return p, fmt.Errorf("profiler enable: %w", err)
	}
	if _, err := readResponse(msgID); err != nil {
		return p, fmt.Errorf("profiler enable response: %w", err)
	}
	if err := send("Profiler.start", nil); err != nil {
		return p, fmt.Errorf("profiler start: %w", err)
	}
	if _, err := readResponse(msgID); err != nil {
		return p, fmt.Errorf("profiler start response: %w", err)
	}

	// 2. Sample for the requested duration.
	select {
	case <-time.After(duration):
	case <-ctx.Done():
		return p, ctx.Err()
	}

	// 3. Stop → receive the profile.
	if err := send("Profiler.stop", nil); err != nil {
		return p, fmt.Errorf("profiler stop: %w", err)
	}
	result, err := readResponse(msgID)
	if err != nil {
		return p, fmt.Errorf("profiler stop response: %w", err)
	}

	var stopResult struct {
		Profile json.RawMessage `json:"profile"`
	}
	if err := json.Unmarshal(result, &stopResult); err != nil {
		return p, fmt.Errorf("parse CDP profile: %w", err)
	}
	var rawProfile cdpProfile
	if err := json.Unmarshal(stopResult.Profile, &rawProfile); err != nil {
		return p, fmt.Errorf("parse CDP CPU profile: %w", err)
	}
	syms, stats := flattenCDPProfile(rawProfile)
	p.Symbols = syms
	if stats.Samples > 0 {
		p.Stats = &stats
	}
	// Persist the profile object itself (not the CDP response envelope), making
	// --output directly consumable by Chrome DevTools and other .cpuprofile tools.
	p.Text = string(stopResult.Profile)
	return p, nil
}

// ProfileInspectorHeap captures a V8 heap snapshot through the Chrome
// DevTools Protocol. Heap snapshots can be large, so chunks are streamed to a
// private temporary file instead of being accumulated in memory. The caller
// may move/copy Profile.Path into its own artifact directory.
func ProfileInspectorHeap(ctx context.Context, pid int32, addr string) (profile Profile, retErr error) {
	if addr == "" {
		addr = DefaultInspectAddr
	}
	profile = Profile{PID: pid, Type: ProfileHeap, Method: "inspector_heap", Taken: time.Now()}
	if err := ValidateInspectorAddr(addr); err != nil {
		return profile, err
	}

	wsURL, err := inspectorWebSocketURL(ctx, addr)
	if err != nil {
		return profile, fmt.Errorf("inspector discovery on %s: %w", addr, err)
	}
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return profile, fmt.Errorf("inspector ws connect: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(maxRawProfileBytes + (1 << 20))

	file, err := os.CreateTemp("", fmt.Sprintf("monitor-node-heap-%d-*.heapsnapshot", pid))
	if err != nil {
		return profile, fmt.Errorf("create heap snapshot: %w", err)
	}
	path := file.Name()
	defer func() {
		if closeErr := file.Close(); retErr == nil && closeErr != nil {
			retErr = fmt.Errorf("close heap snapshot: %w", closeErr)
		}
		if retErr != nil {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return profile, fmt.Errorf("protect heap snapshot: %w", err)
	}

	send := func(id int, method string, params map[string]any) error {
		req := map[string]any{"id": id, "method": method}
		if params != nil {
			req["params"] = params
		}
		data, marshalErr := json.Marshal(req)
		if marshalErr != nil {
			return marshalErr
		}
		return conn.Write(ctx, websocket.MessageText, data)
	}
	readUntilResponse := func(wantID int, collectChunks bool) error {
		var written int64
		for {
			_, data, readErr := conn.Read(ctx)
			if readErr != nil {
				return readErr
			}
			var message struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
				Params struct {
					Chunk string `json:"chunk"`
				} `json:"params"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error,omitempty"`
			}
			if err := json.Unmarshal(data, &message); err != nil {
				continue
			}
			if collectChunks && message.Method == "HeapProfiler.addHeapSnapshotChunk" {
				chunk := []byte(message.Params.Chunk)
				written += int64(len(chunk))
				if written > maxRawProfileBytes {
					return fmt.Errorf("heap snapshot exceeded %d bytes", maxRawProfileBytes)
				}
				if _, err := file.Write(chunk); err != nil {
					return fmt.Errorf("write heap snapshot: %w", err)
				}
			}
			if message.ID != wantID {
				continue
			}
			if message.Error != nil {
				return fmt.Errorf("cdp error: %s", message.Error.Message)
			}
			return nil
		}
	}

	if err := send(1, "HeapProfiler.enable", nil); err != nil {
		return profile, fmt.Errorf("heap profiler enable: %w", err)
	}
	if err := readUntilResponse(1, false); err != nil {
		return profile, fmt.Errorf("heap profiler enable response: %w", err)
	}
	if err := send(2, "HeapProfiler.takeHeapSnapshot", map[string]any{"reportProgress": false}); err != nil {
		return profile, fmt.Errorf("take heap snapshot: %w", err)
	}
	if err := readUntilResponse(2, true); err != nil {
		return profile, fmt.Errorf("take heap snapshot response: %w", err)
	}
	if err := file.Sync(); err != nil {
		return profile, fmt.Errorf("sync heap snapshot: %w", err)
	}
	profile.Path = path
	return profile, nil
}

// ValidateInspectorAddr refuses non-loopback CDP endpoints. Inspector access
// is effectively remote code execution and must never leave the local host.
func ValidateInspectorAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid inspector address %q: %w", addr, err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("refusing non-loopback inspector address %q", addr)
	}
	return nil
}

// inspectorWebSocketURL discovers the inspector WebSocket URL from
// http://host:port/json/list. Returns the first "node" type target's
// webSocketDebuggerUrl.
func inspectorWebSocketURL(ctx context.Context, addr string) (string, error) {
	url := fmt.Sprintf("http://%s/json/list", addr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var targets []struct {
		Type                 string `json:"type"`
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
		Title                string `json:"title"`
	}
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("inspector discovery returned status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&targets); err != nil {
		return "", fmt.Errorf("parse /json/list: %w", err)
	}
	if len(targets) == 0 {
		return "", fmt.Errorf("no inspector targets on %s (start node with --inspect)", addr)
	}
	// Prefer a "node" type target; fall back to the first.
	for _, t := range targets {
		if t.Type == "node" && t.WebSocketDebuggerURL != "" {
			return t.WebSocketDebuggerURL, nil
		}
	}
	if targets[0].WebSocketDebuggerURL == "" {
		return "", fmt.Errorf("inspector target has no webSocketDebuggerUrl")
	}
	return targets[0].WebSocketDebuggerURL, nil
}

// cdpProfile is the hierarchical CPU profile returned by Profiler.stop.
type cdpProfile struct {
	Nodes      []cdpNode `json:"nodes"`
	Samples    []int64   `json:"samples"` // node IDs per sample tick
	TimeDeltas []int64   `json:"timeDeltas,omitempty"`
	StartTime  float64   `json:"startTime"`
	EndTime    float64   `json:"endTime"`
}

type cdpNode struct {
	ID            int64             `json:"id"`
	CallFrame     cdpFrame          `json:"callFrame"`
	HitCount      int64             `json:"hitCount"`
	Children      []int64           `json:"children"`
	PositionTicks []cdpPositionTick `json:"positionTicks,omitempty"`
}

type cdpFrame struct {
	FunctionName string `json:"functionName"`
	ScriptID     string `json:"scriptId"`
	URL          string `json:"url"`
	LineNumber   int64  `json:"lineNumber"`
	ColumnNumber int64  `json:"columnNumber"`
}

// cdpPositionTick is one per-line sample count within a node's own function.
// V8 reports Line already 1-based (unlike callFrame.lineNumber, which is
// 0-based), so it needs no +1 adjustment.
type cdpPositionTick struct {
	Line  int   `json:"line"`
	Ticks int64 `json:"ticks"`
}

// isPseudoCDPFrame reports whether fn is one of V8's synthetic call-tree
// roots. These never correspond to a source line and must never dilute the
// weight denominator (a diffuse profile shouldn't hide the real hot line
// behind 50%+ "(idle)").
func isPseudoCDPFrame(fn string) bool {
	switch fn {
	case "(idle)", "(program)", "(garbage collector)", "(root)":
		return true
	default:
		return false
	}
}

// decodeCDPFileURL turns a CDP callFrame.url into a local path for codemap
// correlation. file:// URLs are percent-decoded via url.Parse (so a path
// with spaces or other escaped bytes resolves to the real filesystem path);
// anything else (node:internal/..., webpack://, or empty) passes through
// unchanged, matching the CDP convention that a non-file URL is already a
// human-readable module specifier, not a URL-escaped path.
func decodeCDPFileURL(raw string) string {
	if raw == "" || !strings.HasPrefix(raw, "file://") {
		return raw
	}
	if u, err := url.Parse(raw); err == nil && u.Path != "" {
		return u.Path
	}
	return strings.TrimPrefix(raw, "file://")
}

// flatKey identifies one aggregated (function, file, line) bucket. Two CDP
// nodes with the same key — because the function was sampled via two
// different call paths (e.g. recursion, or two call sites) — merge into one
// entry instead of appearing as separate, individually-diluted rows.
type flatKey struct {
	Func string
	File string
	Line int
}

// cdpNodeHits is the ONE shared per-node hit rule both CDP consumers —
// flattenCDPProfile (live `monitor profile`) and buildFromCDP
// (BuildHeatmap, `monitor hot`) — attribute samples by: the node's own
// hitCount when it carries a positive one, else — when the profile carries
// the flat per-tick samples array — the number of samples referencing that
// node's id, else 0. hitCount is optional in CDP's own ProfileNode shape
// (only nodes/samples/timeDeltas are guaranteed), and real captures are
// mixed: a writer may populate hitCount on some nodes only, or on none, so
// an all-or-nothing rule either drops genuinely-sampled nodes (their
// positionTicks and their share of Stats vanish) or ignores the samples
// array entirely. Sharing one helper is also what keeps a live capture and
// a file-loaded .cpuprofile reporting identical Stats for the same data.
func cdpNodeHits(prof *cdpProfile) map[int64]int64 {
	hits := make(map[int64]int64, len(prof.Nodes))
	var fromSamples map[int64]int64
	for i := range prof.Nodes {
		n := &prof.Nodes[i]
		if n.HitCount > 0 {
			hits[n.ID] = n.HitCount
			continue
		}
		if len(prof.Samples) == 0 {
			continue // no hitCount and no samples array: 0 stays 0.
		}
		if fromSamples == nil {
			fromSamples = make(map[int64]int64, len(prof.Samples))
			for _, id := range prof.Samples {
				fromSamples[id]++
			}
		}
		hits[n.ID] = fromSamples[n.ID]
	}
	return hits
}

// flattenCDPProfile converts the hierarchical CDP profile into a flat
// []Symbol carrying genuine statement-level attribution, plus the pseudo-
// frame breakdown as Stats.
//
// V8's per-node hitCount attributes time to the function's declaration line
// only; the real hot line lives in the node's positionTicks (already
// 1-based). Each node's sample count comes from cdpNodeHits — the shared
// per-node hit rule buildFromCDP (heat.go) also uses, so a live capture and
// a file-loaded .cpuprofile report identical Stats. This aggregates every
// node's positionTicks by (func, file, line) — merging call-path
// duplicates — and falls back to (lineNumber+1, hits) only for nodes that
// carry no positionTicks at all (e.g. Bun's native builtins, which have no
// url/positionTicks but do have a hitCount). (idle)/(program)/(garbage
// collector)/(root) are excluded from both the output and the weight
// denominator; their share is reported in Stats instead of being silently
// folded into "real" code.
func flattenCDPProfile(prof cdpProfile) ([]Symbol, Stats) {
	var totalHits, idleHits, gcHits, excludedHits int64
	totals := make(map[flatKey]int64)
	declLines := make(map[flatKey]int)
	var order []flatKey

	add := func(fn, file string, line int, ticks int64, funcLine int) {
		if ticks <= 0 {
			return
		}
		k := flatKey{Func: fn, File: file, Line: line}
		if _, ok := totals[k]; !ok {
			order = append(order, k)
			// First occurrence wins: every node that contributes to this
			// key is a sample of the exact same statement, so any of them
			// carries the same enclosing function's declaration line.
			declLines[k] = funcLine
		}
		totals[k] += ticks
	}

	hits := cdpNodeHits(&prof)

	for _, n := range prof.Nodes {
		hc := hits[n.ID]
		if hc <= 0 {
			continue
		}
		totalHits += hc
		f := n.CallFrame
		fn := f.FunctionName
		if fn == "" {
			fn = "(anonymous)"
		}
		if isPseudoCDPFrame(fn) {
			excludedHits += hc
			switch fn {
			case "(idle)":
				idleHits += hc
			case "(garbage collector)":
				gcHits += hc
			}
			continue
		}
		file := decodeCDPFileURL(f.URL)
		funcLine := int(f.LineNumber) + 1
		if len(n.PositionTicks) > 0 {
			for _, pt := range n.PositionTicks {
				add(fn, file, pt.Line, pt.Ticks, funcLine)
			}
			continue
		}
		// No positionTicks (older V8, or a native frame with no source
		// lines): fall back to the node's own declaration line.
		add(fn, file, funcLine, hc, funcLine)
	}

	if totalHits <= 0 {
		return nil, Stats{}
	}

	active := totalHits - excludedHits
	stats := Stats{
		Samples:       int(totalHits),
		ActiveSamples: int(active),
		IdlePct:       float64(idleHits) / float64(totalHits) * 100,
		GCPct:         float64(gcHits) / float64(totalHits) * 100,
	}

	out := make([]Symbol, 0, len(order))
	for _, k := range order {
		var weight float64
		if active > 0 {
			weight = float64(totals[k]) / float64(active) * 100
		}
		out = append(out, Symbol{Func: k.Func, File: k.File, Line: k.Line, Weight: weight, FuncLine: declLines[k]})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Weight != out[j].Weight {
			return out[i].Weight > out[j].Weight
		}
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	// Cap AFTER aggregation (top 50) so merging call paths/lines can't be
	// starved by a cap applied before duplicates were folded together.
	if len(out) > 50 {
		out = out[:50]
	}
	return out, stats
}

// VerifyInspectorOwnership checks whether the inspector at addr belongs to
// pid (same logic as VerifyListenerOwnership but on the inspector port).
func VerifyInspectorOwnership(ctx context.Context, pid int32, addr string) (PortOwnership, string) {
	if addr == "" {
		addr = DefaultInspectAddr
	}
	return VerifyListenerOwnership(ctx, pid, addr)
}

// InspectorAvailable reports whether an inspector is reachable at addr by
// hitting /json/version. Returns the node version when available.
func InspectorAvailable(ctx context.Context, addr string) (string, error) {
	if addr == "" {
		addr = DefaultInspectAddr
	}
	url := fmt.Sprintf("http://%s/json/version", addr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("inspector version returned status %d", resp.StatusCode)
	}
	var v struct {
		Browser string `json:"Browser"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&v); err != nil {
		return "", err
	}
	return v.Browser, nil
}
