package profiler

import (
	"encoding/json"
	"testing"
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
