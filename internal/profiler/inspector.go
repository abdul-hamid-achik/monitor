package profiler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	if duration <= 0 {
		duration = 5 * time.Second
	}
	p := Profile{PID: pid, Type: ProfileCPU, Taken: time.Now()}

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
		Profile cdpProfile `json:"profile"`
	}
	if err := json.Unmarshal(result, &stopResult); err != nil {
		return p, fmt.Errorf("parse CDP profile: %w", err)
	}
	p.Symbols = flattenCDPProfile(stopResult.Profile)
	p.Text = string(result) // raw JSON for the bundle
	return p, nil
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
	Nodes     []cdpNode `json:"nodes"`
	Samples   []int64   `json:"samples"` // node IDs per sample tick
	StartTime float64   `json:"startTime"`
	EndTime   float64   `json:"endTime"`
}

type cdpNode struct {
	ID        int64    `json:"id"`
	CallFrame cdpFrame `json:"callFrame"`
	HitCount  int64    `json:"hitCount"`
	Children  []int64  `json:"children"`
}

type cdpFrame struct {
	FunctionName string `json:"functionName"`
	ScriptID     string `json:"scriptId"`
	URL          string `json:"url"`
	LineNumber   int64  `json:"lineNumber"`
	ColumnNumber int64  `json:"columnNumber"`
}

// flattenCDPProfile converts the hierarchical CDP profile into a flat
// []Symbol sorted by weight (hitCount as % of total hits). Only nodes
// with hitCount > 0 are kept; internal V8 frames (no url) are included
// but flagged with an empty File so callers can skip them.
func flattenCDPProfile(prof cdpProfile) []Symbol {
	totalHits := int64(0)
	for _, n := range prof.Nodes {
		totalHits += n.HitCount
	}
	if totalHits <= 0 {
		return nil
	}
	out := make([]Symbol, 0, len(prof.Nodes))
	for _, n := range prof.Nodes {
		if n.HitCount <= 0 {
			continue
		}
		f := n.CallFrame
		funcName := f.FunctionName
		if funcName == "" {
			funcName = "(anonymous)"
		}
		file := f.URL
		// file:// URLs → local paths for codemap correlation.
		file = strings.TrimPrefix(file, "file://")
		line := int(f.LineNumber + 1) // CDP is 0-indexed; our Symbol is 1-indexed
		sym := Symbol{
			Func:   funcName,
			File:   file,
			Line:   line,
			Weight: float64(n.HitCount) / float64(totalHits) * 100,
		}
		out = append(out, sym)
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
	// Cap to ~25 frames (same as pprof parser) so codemap correlation
	// stays bounded.
	if len(out) > 25 {
		out = out[:25]
	}
	return out
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
