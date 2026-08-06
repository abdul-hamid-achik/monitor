package profiler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

func TestFlattenCDPProfile(t *testing.T) {
	// Minimal CDP profile: root → foo (10 hits) → bar (5 hits).
	raw := `{
		"nodes": [
			{"id":1,"callFrame":{"functionName":"(root)","url":"","lineNumber":-1},"hitCount":0,"children":[2,3]},
			{"id":2,"callFrame":{"functionName":"foo","url":"file:///app/src/index.js","lineNumber":10},"hitCount":10,"children":[]},
			{"id":3,"callFrame":{"functionName":"bar","url":"file:///app/src/utils.js","lineNumber":42},"hitCount":5,"children":[]}
		],
		"samples": [2,2,2,3,2,3,2,2,2,3,2,3,2,2,2],
		"startTime": 1000,
		"endTime": 2000
	}`
	var prof cdpProfile
	if err := json.Unmarshal([]byte(raw), &prof); err != nil {
		t.Fatal(err)
	}
	syms := flattenCDPProfile(prof)
	if len(syms) != 2 {
		t.Fatalf("got %d symbols, want 2", len(syms))
	}
	// foo has 10 hits out of 15 total → 66.7%
	if syms[0].Func != "foo" {
		t.Errorf("first symbol = %q, want foo", syms[0].Func)
	}
	if syms[0].File != "/app/src/index.js" {
		t.Errorf("file = %q, want /app/src/index.js", syms[0].File)
	}
	if syms[0].Line != 11 { // 0-indexed + 1
		t.Errorf("line = %d, want 11", syms[0].Line)
	}
	if syms[0].Weight < 66.0 || syms[0].Weight > 67.0 {
		t.Errorf("weight = %.1f, want ~66.7", syms[0].Weight)
	}
	// bar is second (5/15 = 33.3%)
	if syms[1].Func != "bar" || syms[1].Line != 43 {
		t.Errorf("second symbol = %+v, want bar:43", syms[1])
	}
}

func TestFlattenCDPProfileEmpty(t *testing.T) {
	syms := flattenCDPProfile(cdpProfile{})
	if syms != nil {
		t.Errorf("empty profile should return nil, got %d symbols", len(syms))
	}
}

func TestFlattenCDPProfileCapsFrames(t *testing.T) {
	var prof cdpProfile
	for i := range 30 {
		prof.Nodes = append(prof.Nodes, cdpNode{
			ID:        int64(i + 1),
			CallFrame: cdpFrame{FunctionName: "fn", URL: "file:///x.js", LineNumber: int64(i)},
			HitCount:  int64(30 - i),
		})
	}
	syms := flattenCDPProfile(prof)
	if len(syms) != 25 {
		t.Errorf("got %d symbols, want capped at 25", len(syms))
	}
}

func TestFlattenCDPProfileStripsFileScheme(t *testing.T) {
	raw := `{"nodes":[{"id":1,"callFrame":{"functionName":"main","url":"file:///home/app/server.js","lineNumber":0},"hitCount":1,"children":[]}],"samples":[1],"startTime":0,"endTime":1}`
	var prof cdpProfile
	_ = json.Unmarshal([]byte(raw), &prof)
	syms := flattenCDPProfile(prof)
	if len(syms) != 1 || syms[0].File != "/home/app/server.js" {
		t.Fatalf("expected file:/// stripped; got %+v", syms)
	}
}

func TestValidateInspectorAddrRefusesRemoteListeners(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:9229", "localhost:9231", "[::1]:9229"} {
		if err := ValidateInspectorAddr(addr); err != nil {
			t.Errorf("loopback %s rejected: %v", addr, err)
		}
	}
	for _, addr := range []string{"0.0.0.0:9229", "10.0.0.5:9229", "example.com:9229"} {
		if err := ValidateInspectorAddr(addr); err == nil {
			t.Errorf("non-loopback %s was accepted", addr)
		}
	}
}

func TestProfileInspectorHeapStreamsSnapshotToPrivateFile(t *testing.T) {
	var server *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `[{"type":"node","webSocketDebuggerUrl":"%s/ws"}]`, strings.Replace(server.URL, "http://", "ws://", 1))
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		for _, id := range []int{1, 2} {
			_, _, err := conn.Read(r.Context())
			if err != nil {
				t.Errorf("read command %d: %v", id, err)
				return
			}
			if id == 2 {
				for _, chunk := range []string{"{\"snapshot\":", "true}"} {
					payload, _ := json.Marshal(map[string]any{
						"method": "HeapProfiler.addHeapSnapshotChunk",
						"params": map[string]string{"chunk": chunk},
					})
					if err := conn.Write(r.Context(), websocket.MessageText, payload); err != nil {
						t.Errorf("write heap chunk: %v", err)
						return
					}
				}
			}
			response, _ := json.Marshal(map[string]any{"id": id, "result": map[string]any{}})
			if err := conn.Write(r.Context(), websocket.MessageText, response); err != nil {
				t.Errorf("write response: %v", err)
				return
			}
		}
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	profile, err := ProfileInspectorHeap(context.Background(), 4242, strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(profile.Path)
	if profile.Method != "inspector_heap" {
		t.Fatalf("method = %q", profile.Method)
	}
	content, err := os.ReadFile(profile.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != `{"snapshot":true}` {
		t.Fatalf("snapshot = %q", content)
	}
	info, err := os.Stat(profile.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode = %o, want 600", info.Mode().Perm())
	}
}
