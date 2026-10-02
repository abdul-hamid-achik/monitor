package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/appserver"
	"github.com/abdul-hamid-achik/monitor/internal/contextids"
	"github.com/abdul-hamid-achik/monitor/internal/devrun"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/project"
	"github.com/abdul-hamid-achik/monitor/internal/stacktrace"
)

// appClient drives the real app service (newAppService) over pipes.
type appClient struct {
	t       *testing.T
	in      *io.PipeWriter
	msgs    chan map[string]any
	pending []map[string]any
	nextID  int
}

func startAppClient(t *testing.T, opts appserver.Options) *appClient {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &appClient{t: t, in: inW, msgs: make(chan map[string]any, 64)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = appserver.New(newAppService(), opts).Run(context.Background(), inR, outW)
		_ = outW.Close()
	}()
	go func() {
		defer close(c.msgs)
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err == nil {
				c.msgs <- m
			}
		}
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		<-done
	})
	c.wait(func(m map[string]any) bool { return m["method"] == "hello" })
	return c
}

func (c *appClient) wait(pred func(map[string]any) bool) map[string]any {
	c.t.Helper()
	for i, m := range c.pending {
		if pred(m) {
			c.pending = append(c.pending[:i], c.pending[i+1:]...)
			return m
		}
	}
	timeout := time.After(10 * time.Second)
	for {
		select {
		case m, ok := <-c.msgs:
			if !ok {
				c.t.Fatal("server output closed")
			}
			if pred(m) {
				return m
			}
			c.pending = append(c.pending, m)
		case <-timeout:
			c.t.Fatalf("timed out; buffered %v", c.pending)
		}
	}
}

// call sends one request and returns its result, failing on an error.
func (c *appClient) call(method string, params any) map[string]any {
	c.t.Helper()
	resp := c.callRaw(method, params)
	if resp["error"] != nil {
		c.t.Fatalf("%s failed: %v", method, resp["error"])
	}
	result, _ := resp["result"].(map[string]any)
	return result
}

func (c *appClient) callRaw(method string, params any) map[string]any {
	c.t.Helper()
	c.nextID++
	id := c.nextID
	line, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err := c.in.Write(append(line, '\n')); err != nil {
		c.t.Fatal(err)
	}
	return c.wait(func(m map[string]any) bool { v, ok := m["id"].(float64); return ok && int(v) == id })
}

// recordAppException writes one exception occurrence the way `monitor run`
// does, through the short-lived writer.
func recordAppException(t *testing.T, storePath, typ, value, fn string, at time.Time) {
	t.Helper()
	ex := stacktrace.Exception{
		Type: typ, Value: value, Runtime: "node", Parser: "js",
		Frames: []stacktrace.Frame{{Function: fn, Filename: "/repo/src/app.js", AbsPath: "/repo/src/app.js", Lineno: 42}},
	}
	id := project.Identity{Slug: "shop", Service: "api", Root: "/repo", GitRoot: "/repo"}
	_, err := issues.RecordException(context.Background(), storePath, issues.DefaultWriterWait, ex, id, contextids.IDs{},
		issues.RecordExceptionOptions{ObservedAt: at})
	if err != nil {
		t.Fatalf("record %s: %v", typ, err)
	}
}

func TestServeAppServiceEndToEnd(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "issues.veclite")
	t.Setenv("MONITOR_ISSUES_STORE", storePath)
	t.Setenv("MONITOR_LOG_STORE", filepath.Join(dir, "logs.veclite"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	secret := "sk_" + "live_" + "AbCdEf0123456789xyz"
	t.Setenv("STRIPE_API_KEY", secret)

	c := startAppClient(t, appserver.Options{Version: "test", IssuesPoll: 20 * time.Millisecond})

	// No store yet: every read answers empty, not an error.
	if got := c.call("issues.list", nil)["items"].([]any); len(got) != 0 {
		t.Fatalf("empty store listed %v", got)
	}
	if got := c.call("issues.get", map[string]any{"id": "latest"}); got["not_found"] != true {
		t.Fatalf("latest on an empty store = %v", got)
	}

	now := time.Now().UTC()
	recordAppException(t, storePath, "TypeError", "charge failed with key "+secret, "chargeCard", now.Add(-time.Minute))

	list := c.call("issues.list", map[string]any{"query": "chargecard"})
	items := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("query chargecard matched %d issues, want 1", len(items))
	}
	issue := items[0].(map[string]any)
	if strings.Contains(issue["message"].(string), secret) {
		t.Fatalf("issues.list leaked a secret env value: %q", issue["message"])
	}
	if list["privacy"].(map[string]any)["text_is_untrusted"] != true {
		t.Fatal("issues.list must mark its text untrusted")
	}
	id := issue["id"].(string)

	got := c.call("issues.get", map[string]any{"id": id[4:8], "markdown": true})
	ctxDoc := got["context"].(map[string]any)
	if ctxDoc["schema"] != "monitor.issue_context.v1" || ctxDoc["budget"] != "full" {
		t.Fatalf("issues.get context = %v", ctxDoc)
	}
	if got["root"] != "/repo" {
		t.Fatalf("root = %v, want the recorded checkout /repo", got["root"])
	}
	if md, _ := got["markdown"].(string); md == "" || strings.Contains(md, secret) {
		t.Fatalf("markdown empty or leaking: %q", md)
	}

	occ := c.call("issues.occurrences", map[string]any{"id": id})
	if occ["total"].(float64) != 1 {
		t.Fatalf("occurrences = %v", occ)
	}

	hist := c.call("issues.histogram", map[string]any{"ids": []string{id, "ISS-NONE"}, "since": "1h", "buckets": 4})
	series := hist["series"].(map[string]any)
	var sum float64
	for _, v := range series[id].([]any) {
		sum += v.(float64)
	}
	if sum != 1 || len(series["ISS-NONE"].([]any)) != 4 {
		t.Fatalf("histogram = %v", series)
	}

	projects := c.call("projects.list", nil)["projects"].([]any)
	if len(projects) != 1 || projects[0].(map[string]any)["project"] != "shop" || projects[0].(map[string]any)["root"] != "/repo" {
		t.Fatalf("projects = %v, want shop recorded under /repo", projects)
	}
	if resp := c.callRaw("doctor", map[string]any{"dir": "relative/dir"}); resp["error"].(map[string]any)["code"].(float64) != appserver.CodeInvalidParams {
		t.Fatalf("doctor with a relative dir = %v", resp)
	}

	// Live events: subscribe, then a new issue and a regression.
	c.call("subscribe", map[string]any{"topics": []string{"issues"}})
	set := c.call("issues.set_status", map[string]any{"ids": []string{id}, "status": "resolved"})
	if len(set["updated"].([]any)) != 1 {
		t.Fatalf("set_status = %v", set)
	}
	statusEv := c.wait(func(m map[string]any) bool { return m["method"] == "issue.event" })["params"].(map[string]any)
	if statusEv["type"] != "status" {
		t.Fatalf("after resolve got %v, want a status event", statusEv)
	}

	recordAppException(t, storePath, "RangeError", "index out of range", "readRow", time.Now().UTC())
	newEv := c.wait(func(m map[string]any) bool { return m["method"] == "issue.event" })["params"].(map[string]any)
	if newEv["type"] != "new" || newEv["issue"].(map[string]any)["exception_type"] != "RangeError" {
		t.Fatalf("got %v, want a new RangeError", newEv)
	}

	recordAppException(t, storePath, "TypeError", "charge failed again", "chargeCard", time.Now().UTC())
	regEv := c.wait(func(m map[string]any) bool { return m["method"] == "issue.event" })["params"].(map[string]any)
	if regEv["type"] != "regressed" || regEv["issue"].(map[string]any)["id"] != id {
		t.Fatalf("got %v, want %s regressed", regEv, id)
	}

	if amb := c.callRaw("issues.get", map[string]any{"id": "ISS-NOPE"}); amb["error"].(map[string]any)["code"].(float64) != appserver.CodeNotFound {
		t.Fatalf("unknown id = %v", amb)
	}
}

func TestServeLaunchesNeverExposeInspectorURLs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("MONITOR_ISSUES_STORE", filepath.Join(dir, "issues.veclite"))
	ws := "ws://127.0.0.1:9229/" + "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"
	if _, err := devrun.WriteRegistryEntry(devrun.RegistryEntry{
		Schema: devrun.RegistrySchema, LaunchID: "L-1", PID: 1, Name: "api", Project: "shop",
		StartedAt: time.Now(), Scan: "stderr",
		Inspectors: []devrun.RegistryInspector{{PID: 1, Port: 9229, WS: ws}},
	}); err != nil {
		t.Fatal(err)
	}
	c := startAppClient(t, appserver.Options{})
	resp := c.callRaw("launches.list", nil)
	raw, _ := json.Marshal(resp)
	if strings.Contains(string(raw), "ws://") {
		t.Fatalf("launches.list leaked an inspector URL: %s", raw)
	}
	launches := resp["result"].(map[string]any)["launches"].([]any)
	if len(launches) != 1 || launches[0].(map[string]any)["inspector_ports"].([]any)[0].(float64) != 9229 {
		t.Fatalf("launches = %v", launches)
	}
}

func TestServeRefusesProtectedKillAndReadOnlyWrites(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MONITOR_ISSUES_STORE", filepath.Join(dir, "issues.veclite"))
	c := startAppClient(t, appserver.Options{})
	resp := c.callRaw("process.kill", map[string]any{"pid": 1, "confirm": true})
	if code := resp["error"].(map[string]any)["code"].(float64); code != appserver.CodeRefused {
		t.Fatalf("killing pid 1 answered %v, want CodeRefused", resp)
	}

	ro := startAppClient(t, appserver.Options{ReadOnly: true})
	resp = ro.callRaw("issues.set_status", map[string]any{"ids": []string{"ISS-1"}, "status": "resolved"})
	if code := resp["error"].(map[string]any)["code"].(float64); code != appserver.CodeReadOnly {
		t.Fatalf("read-only set_status answered %v", resp)
	}
}

func TestServeHeatmapFileRequiresAbsolutePath(t *testing.T) {
	c := startAppClient(t, appserver.Options{})
	resp := c.callRaw("heatmap.file", map[string]any{"path": "relative.cpuprofile"})
	if code := resp["error"].(map[string]any)["code"].(float64); code != appserver.CodeInvalidParams {
		t.Fatalf("relative path answered %v", resp)
	}
	abs, err := filepath.Abs(filepath.Join("..", "profiler", "testdata", "v8-hot.cpuprofile"))
	if err != nil {
		t.Fatal(err)
	}
	hm := c.call("heatmap.file", map[string]any{"path": abs})
	if hm["schema"] != "monitor.line_heatmap.v1" || len(hm["functions"].([]any)) == 0 {
		t.Fatalf("heatmap.file = %v", hm)
	}
}
