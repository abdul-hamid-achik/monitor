package cli

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/procbind"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
)

// cairntrace parses `monitor process|tree|resolve|profile --json` by hand
// (~/projects/cairntrace/src/core/monitor/monitorClient.ts:17-71), with no
// schema: a renamed key there fails silently as undefined. These are the
// keys it reads; renaming one breaks cairntrace's process sampler, its
// `monitor:` spec step, or its service resolution.
func TestCairntraceReadsTheseJSONKeys(t *testing.T) {
	keys := func(t *testing.T, v any) map[string]json.RawMessage {
		t.Helper()
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	require := func(t *testing.T, m map[string]json.RawMessage, names ...string) {
		t.Helper()
		for _, n := range names {
			if _, ok := m[n]; !ok {
				t.Errorf("missing key %q that cairntrace reads; got %v", n, m)
			}
		}
	}

	proc := collector.ProcessInfo{PID: 42, Name: "node", CPUPercent: 12.5, Memory: 1 << 20, MemoryPercent: 1.5, Threads: 7, Parent: 1, IsSystem: false, IsProtected: false}
	t.Run("process", func(t *testing.T) {
		require(t, keys(t, proc), "pid", "name", "cpu_percent", "memory", "memory_percent", "threads")
	})
	t.Run("tree", func(t *testing.T) {
		node := &treeNode{ProcessInfo: proc, Children: []*treeNode{{ProcessInfo: collector.ProcessInfo{PID: 43, Parent: 42}}}}
		require(t, keys(t, node), "pid", "name", "cpu_percent", "memory", "memory_percent", "threads", "parent", "is_system", "is_protected", "children")
	})
	t.Run("resolve", func(t *testing.T) {
		b := procbind.Binding{PID: 42, Name: "node", Runtime: procbind.RuntimeNode, CodebaseRoot: "/repo", MainScript: "/repo/server.js", InspectAddr: "127.0.0.1:9229"}
		require(t, keys(t, b), "pid", "name", "runtime", "codebase_root", "main_script", "inspect_addr")
	})
	t.Run("profile", func(t *testing.T) {
		p := profiler.Profile{
			PID: 42, Type: profiler.ProfileCPU, Method: "inspector_cpu", Taken: time.Now(), Path: "/tmp/x.cpuprofile",
			Symbols: []profiler.Symbol{{Func: "hot", File: "server.js", Line: 17}},
			Receipt: &profiler.Receipt{Verified: true, SizeBytes: 10, Limitation: "x"},
		}
		m := keys(t, p)
		require(t, m, "pid", "type", "method", "taken", "symbols", "path", "receipt")
		var syms []map[string]json.RawMessage
		if err := json.Unmarshal(m["symbols"], &syms); err != nil || len(syms) != 1 {
			t.Fatalf("symbols = %s", m["symbols"])
		}
		require(t, syms[0], "func", "file", "line")
		var receipt map[string]json.RawMessage
		if err := json.Unmarshal(m["receipt"], &receipt); err != nil {
			t.Fatal(err)
		}
		require(t, receipt, "verified", "size_bytes", "limitation")
	})
}
