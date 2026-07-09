package baseline

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/monitor/internal/analyzer"
	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

const (
	mib uint64 = 1024 * 1024
	gib uint64 = 1024 * mib
)

// bl builds a Baseline named "b" for verdict tests.
func bl(cpu, mem, load float64, swapUsed, swapTotal, diskUsed, diskTotal uint64, procs map[int32]ProcSnap) *Baseline {
	return &Baseline{
		Name:      "b",
		CPUUsage:  cpu,
		MemUsage:  mem,
		Load1:     load,
		SwapUsed:  swapUsed,
		SwapTotal: swapTotal,
		DiskUsed:  diskUsed,
		DiskTotal: diskTotal,
		Processes: procs,
	}
}

// procMap builds an n-entry process map with distinct PIDs and zero memory,
// for tests where only the process COUNT matters.
func procMap(n int) map[int32]ProcSnap {
	m := make(map[int32]ProcSnap, n)
	for i := 0; i < n; i++ {
		m[int32(i+1)] = ProcSnap{Name: "p", Memory: 0}
	}
	return m
}

func TestComputeVerdicts(t *testing.T) {
	tests := []struct {
		name        string
		from, to    *Baseline
		wantMetrics []string
		wantConf    string // checked against the FIRST verdict, when set
		wantSubstr  string // checked against the FIRST verdict's Summary, when set
	}{
		{
			name:        "no changes",
			from:        bl(10, 30, 1, 0, 0, 0, 0, map[int32]ProcSnap{1: {Name: "a", Memory: 100 * mib}}),
			to:          bl(10, 30, 1, 0, 0, 0, 0, map[int32]ProcSnap{1: {Name: "a", Memory: 100 * mib}}),
			wantMetrics: nil,
		},
		{
			name:        "total rss grows 36% over floor",
			from:        bl(10, 30, 1, 0, 0, 0, 0, map[int32]ProcSnap{1: {Name: "a", Memory: 1500 * mib}}),
			to:          bl(10, 30, 1, 0, 0, 0, 0, map[int32]ProcSnap{1: {Name: "a", Memory: 2044 * mib}}),
			wantMetrics: []string{MetricTotalRSS},
			wantSubstr:  "total RSS +36%",
		},
		{
			name:        "tiny base big percent is noise",
			from:        bl(10, 30, 1, 0, 0, 0, 0, map[int32]ProcSnap{1: {Name: "a", Memory: 100 * mib}}),
			to:          bl(10, 30, 1, 0, 0, 0, 0, map[int32]ProcSnap{1: {Name: "a", Memory: 150 * mib}}),
			wantMetrics: nil,
		},
		{
			name:        "huge base small percent is noise",
			from:        bl(10, 30, 1, 0, 0, 0, 0, map[int32]ProcSnap{1: {Name: "a", Memory: 20 * gib}}),
			to:          bl(10, 30, 1, 0, 0, 0, 0, map[int32]ProcSnap{1: {Name: "a", Memory: 20*gib + 600*mib}}),
			wantMetrics: nil,
		},
		{
			name:        "memory points significant high confidence",
			from:        bl(10, 30, 1, 0, 0, 0, 0, nil),
			to:          bl(10, 55, 1, 0, 0, 0, 0, nil),
			wantMetrics: []string{MetricMemory},
			wantConf:    analyzer.ConfidenceHigh,
		},
		{
			name:        "memory points just under floor",
			from:        bl(10, 30, 1, 0, 0, 0, 0, nil),
			to:          bl(10, 39.9, 1, 0, 0, 0, 0, nil),
			wantMetrics: nil,
		},
		{
			name:        "cpu capped at medium",
			from:        bl(10, 30, 1, 0, 0, 0, 0, nil),
			to:          bl(65, 30, 1, 0, 0, 0, 0, nil),
			wantMetrics: []string{MetricCPU},
			wantConf:    analyzer.ConfidenceMedium,
		},
		{
			name:        "pre-schema baseline suppresses swap and disk",
			from:        bl(10, 30, 1, 0, 0, 0, 0, nil),
			to:          bl(10, 30, 1, 4*gib, 8*gib, 0, 0, nil),
			wantMetrics: nil,
		},
		{
			name:        "swap from zero fires on absolute floor",
			from:        bl(10, 30, 1, 0, 500*gib, 0, 500*gib, nil),
			to:          bl(10, 30, 1, 2*gib, 500*gib, 0, 500*gib, nil),
			wantMetrics: []string{MetricSwap},
			wantConf:    analyzer.ConfidenceHigh,
		},
		{
			name:        "disk delta over floor but under relative gate is noise",
			from:        bl(10, 30, 1, 0, 500*gib, 100*gib, 500*gib, nil),
			to:          bl(10, 30, 1, 0, 500*gib, 103*gib, 500*gib, nil),
			wantMetrics: nil,
		},
		{
			name:        "disk delta clears both gates",
			from:        bl(10, 30, 1, 0, 500*gib, 100*gib, 500*gib, nil),
			to:          bl(10, 30, 1, 0, 500*gib, 130*gib, 500*gib, nil),
			wantMetrics: []string{MetricDisk},
		},
		{
			name:        "proc count relative gate fires",
			from:        bl(10, 30, 1, 0, 0, 0, 0, procMap(250)),
			to:          bl(10, 30, 1, 0, 0, 0, 0, procMap(350)),
			wantMetrics: []string{MetricProcCount},
		},
		{
			name:        "proc count absolute delta but tiny fraction is noise",
			from:        bl(10, 30, 1, 0, 0, 0, 0, procMap(10000)),
			to:          bl(10, 30, 1, 0, 0, 0, 0, procMap(10100)),
			wantMetrics: nil,
		},
		{
			name:        "improvement direction",
			from:        bl(10, 30, 1, 2*gib, 500*gib, 0, 500*gib, nil),
			to:          bl(10, 30, 1, 64*mib, 500*gib, 0, 500*gib, nil),
			wantMetrics: []string{MetricSwap},
			wantSubstr:  "— improved",
		},
		{
			name:        "old proc mem zero skipped",
			from:        bl(10, 30, 1, 0, 0, 0, 0, map[int32]ProcSnap{1: {Name: "a", Memory: 0}}),
			to:          bl(10, 30, 1, 0, 0, 0, 0, map[int32]ProcSnap{1: {Name: "a", Memory: 1 * gib}}),
			wantMetrics: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := Compute(tc.from, tc.to, 1)
			verdicts := ComputeVerdicts(tc.from, tc.to, d, DefaultThresholds())
			var got []string
			for _, v := range verdicts {
				got = append(got, v.Metric)
			}
			if !equalStrings(got, tc.wantMetrics) {
				t.Fatalf("metrics = %v, want %v (verdicts: %+v)", got, tc.wantMetrics, verdicts)
			}
			if len(verdicts) == 0 {
				return
			}
			first := verdicts[0]
			if tc.wantConf != "" && first.Confidence != tc.wantConf {
				t.Errorf("Confidence = %q, want %q", first.Confidence, tc.wantConf)
			}
			if tc.wantSubstr != "" && !strings.Contains(first.Summary, tc.wantSubstr) {
				t.Errorf("Summary = %q, want substring %q", first.Summary, tc.wantSubstr)
			}
		})
	}
}

// TestComputeVerdictsPerProcCappedAndOrdered exercises the per-process
// proc_rss path separately: 5 processes all clear the significance gates,
// but MaxProcVerdicts caps output at 3, biggest mover first.
func TestComputeVerdictsPerProcCappedAndOrdered(t *testing.T) {
	deltas := map[int32]uint64{1: 1000 * mib, 2: 1010 * mib, 3: 1020 * mib, 4: 1030 * mib, 5: 1040 * mib}
	fromProcs := make(map[int32]ProcSnap, 5)
	toProcs := make(map[int32]ProcSnap, 5)
	for pid, delta := range deltas {
		fromProcs[pid] = ProcSnap{Name: "p", Memory: 512 * mib}
		toProcs[pid] = ProcSnap{Name: "p", Memory: 512*mib + delta}
	}
	from := bl(10, 30, 1, 0, 0, 0, 0, fromProcs)
	to := bl(10, 30, 1, 0, 0, 0, 0, toProcs)
	d := Compute(from, to, 1)
	verdicts := ComputeVerdicts(from, to, d, DefaultThresholds())

	if len(verdicts) != 4 { // total_rss + 3 capped proc_rss
		t.Fatalf("got %d verdicts, want 4: %+v", len(verdicts), verdicts)
	}
	if verdicts[0].Metric != MetricTotalRSS {
		t.Errorf("verdicts[0].Metric = %q, want %q", verdicts[0].Metric, MetricTotalRSS)
	}
	wantPIDOrder := []int32{5, 4, 3} // biggest |delta| first
	for i, wantPID := range wantPIDOrder {
		v := verdicts[i+1]
		if v.Metric != MetricProcRSS {
			t.Fatalf("verdicts[%d].Metric = %q, want %q", i+1, v.Metric, MetricProcRSS)
		}
		if v.PID != wantPID {
			t.Errorf("verdicts[%d].PID = %d, want %d", i+1, v.PID, wantPID)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestVerdictsJSONAdditive(t *testing.T) {
	data, err := json.Marshal(Diff{From: "a", To: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "verdicts") {
		t.Errorf("empty Diff JSON should omit verdicts: %s", data)
	}

	d := Diff{From: "a", To: "b", Verdicts: []Verdict{{
		Metric:    MetricSwap,
		Diagnosis: collector.Diagnosis{Summary: "s", Confidence: analyzer.ConfidenceHigh},
	}}}
	data, err = json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{"verdicts", "metric", "summary", "confidence"} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON missing %q: %s", want, s)
		}
	}
}

func TestPreSchemaBaselineUnmarshal(t *testing.T) {
	var b Baseline
	raw := `{"name":"old","cpu_usage":10,"mem_usage":30,"load1":1,"processes":{"1":{"name":"a","memory":1000}}}`
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatal(err)
	}
	if b.DiskTotal != 0 || b.SwapTotal != 0 {
		t.Errorf("pre-schema baseline should unmarshal DiskTotal/SwapTotal as 0: %+v", b)
	}
}
