package analyzer

import (
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

// pushSeries drives an Engine's history with one synthetic process, 1s apart.
func pushSeries(e *Engine, pid int32, name string, rss []uint64, cpu []float64) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range rss {
		e.Observe(collector.Event{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Processes: []collector.ProcessInfo{{PID: pid, Name: name, Memory: rss[i], CPUPercent: cpu[i]}},
		})
	}
}

const mb = 1024 * 1024

func rampU(start, step uint64, n int) []uint64 {
	out := make([]uint64, n)
	for i := range out {
		out[i] = start + step*uint64(i)
	}
	return out
}

func flatU(v uint64, n int) []uint64 {
	out := make([]uint64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func rampF(start, step float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = start + step*float64(i)
	}
	return out
}

func flatF(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// sawtoothU produces a triangle wave: climbs for period/2 samples, falls for period/2.
func sawtoothU(base, amp uint64, period, n int) []uint64 {
	out := make([]uint64, n)
	half := period / 2
	for i := 0; i < n; i++ {
		pos := i % period
		if pos < half {
			out[i] = base + amp*uint64(pos)/uint64(half)
		} else {
			out[i] = base + amp*uint64(period-pos)/uint64(half)
		}
	}
	return out
}

// alternatingU alternates +amp/-amp every sample.
func alternatingU(base, amp uint64, n int) []uint64 {
	out := make([]uint64, n)
	for i := range out {
		out[i] = base + amp*uint64(i%2)
	}
	return out
}

func TestDiagnosePatterns(t *testing.T) {
	tests := []struct {
		name            string
		rss             []uint64
		cpu             []float64
		wantMatch       bool
		wantSummarySub  string // substring the Summary must contain; "" if !wantMatch
		wantConfidence  string
		wantFirstAction string // substring NextActions[0] must contain
	}{
		{
			name:            "leak_rss_rising_cpu_flat",
			rss:             rampU(100*mb, 1*mb, 20),
			cpu:             flatF(10, 20),
			wantMatch:       true,
			wantSummarySub:  "memory leak",
			wantConfidence:  ConfidenceHigh, // r2~1.0, n=20
			wantFirstAction: "type:heap",
		},
		{
			name: "leak_with_subthreshold_jitter",
			rss: func() []uint64 {
				s := rampU(100*mb, 2*mb, 20)
				for i := range s {
					s[i] += uint64(i%2) * mb
				}
				return s
			}(),
			cpu:             flatF(15, 20),
			wantMatch:       true,
			wantSummarySub:  "memory leak", // 1MB jitter < minAmp(~3.3MB) -> 0 reversals
			wantConfidence:  ConfidenceHigh,
			wantFirstAction: "type:heap",
		},
		{
			name:            "spin_cpu_high_flat_rss_flat",
			rss:             flatU(100*mb, 12),
			cpu:             flatF(95, 12),
			wantMatch:       true,
			wantSummarySub:  "spin/hot loop",
			wantConfidence:  ConfidenceHigh, // mean 95 >= 90, n=12 >= 10
			wantFirstAction: "type:cpu",
		},
		{
			name:            "spin_cpu_rising_rss_flat",
			rss:             flatU(100*mb, 15),
			cpu:             rampF(20, 4, 15), // ends at 76%, slope 4pp/sample, r2~1
			wantMatch:       true,
			wantSummarySub:  "spin/hot loop",
			wantConfidence:  ConfidenceHigh,
			wantFirstAction: "type:cpu",
		},
		{
			name:            "load_both_rising",
			rss:             rampU(100*mb, 1*mb, 20),
			cpu:             rampF(10, 3, 20),
			wantMatch:       true,
			wantSummarySub:  "increased load",
			wantConfidence:  ConfidenceHigh,
			wantFirstAction: "monitor_processes",
		},
		{
			name:            "gc_sawtooth_triangle",
			rss:             sawtoothU(100*mb, 20*mb, 6, 30), // ~9 reversals
			cpu:             flatF(30, 30),
			wantMatch:       true,
			wantSummarySub:  "GC pressure",
			wantConfidence:  ConfidenceHigh, // reversals >= 8, n=30 >= 16
			wantFirstAction: "type:heap",
		},
		{
			name:            "gc_sawtooth_alternating_short",
			rss:             alternatingU(100*mb, 10*mb, 10), // 8 reversals, n=10
			cpu:             flatF(30, 10),
			wantMatch:       true,
			wantSummarySub:  "GC pressure",
			wantConfidence:  ConfidenceMedium, // n=10 < 16 blocks high
			wantFirstAction: "type:heap",
		},
		{
			name:      "quiet_flat_flat",
			rss:       flatU(100*mb, 20),
			cpu:       flatF(5, 20),
			wantMatch: false,
		},
		{
			name:      "too_few_samples",
			rss:       rampU(100*mb, 10*mb, 3),
			cpu:       flatF(10, 3),
			wantMatch: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEngine() // no alert rules needed; Observe just feeds History
			pushSeries(e, 42, "proc", tc.rss, tc.cpu)
			diags := e.Diagnose()
			if !tc.wantMatch {
				if len(diags) != 0 {
					t.Fatalf("expected no diagnosis, got %+v", diags)
				}
				return
			}
			if len(diags) != 1 {
				t.Fatalf("expected 1 diagnosis, got %d: %+v", len(diags), diags)
			}
			d := diags[0]
			if !strings.Contains(d.Summary, tc.wantSummarySub) {
				t.Errorf("Summary = %q, want substring %q", d.Summary, tc.wantSummarySub)
			}
			if d.Confidence != tc.wantConfidence {
				t.Errorf("Confidence = %q, want %q", d.Confidence, tc.wantConfidence)
			}
			if len(d.NextActions) == 0 || len(d.NextActions) > 2 {
				t.Fatalf("NextActions len = %d, want 1..2: %v", len(d.NextActions), d.NextActions)
			}
			if !strings.Contains(d.NextActions[0], tc.wantFirstAction) {
				t.Errorf("NextActions[0] = %q, want substring %q", d.NextActions[0], tc.wantFirstAction)
			}
			for _, a := range d.NextActions {
				if !strings.HasPrefix(a, "monitor_") {
					t.Errorf("NextAction %q does not reference a monitor tool", a)
				}
			}
			if len(d.Evidence) < 2 {
				t.Fatalf("Evidence too thin: %v", d.Evidence)
			}
			if !strings.Contains(d.Evidence[0], "R²=") || !strings.Contains(d.Evidence[0], "slope") {
				t.Errorf("Evidence[0] must preserve slope and R²: %q", d.Evidence[0])
			}
			if !strings.Contains(d.Summary, "proc (pid 42)") {
				t.Errorf("Summary should name the process: %q", d.Summary)
			}
		})
	}
}

// IMPORTANT: if a sawtooth row's confidence assertion above fails, re-derive
// the expected reversal count from the TestCountReversals semantics below
// and fix the ROW, not the algorithm.
func TestCountReversals(t *testing.T) {
	tests := []struct {
		name   string
		y      []uint64
		minAmp float64
		want   int
	}{
		{"monotone", rampU(0, 10, 10), 5, 0},
		{"single_peak", []uint64{0, 10, 20, 10, 0}, 5, 1},
		{"peak_and_trough", []uint64{0, 20, 0, 20, 0}, 5, 3},
		{"jitter_below_amp", []uint64{100, 102, 100, 102, 100, 102}, 5, 0},
		{"alternating_big", alternatingU(100, 50, 6), 10, 4}, // dir set at i=1, reversals at i=2..5
		{"too_short", []uint64{1, 2}, 1, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := countReversals(tc.y, tc.minAmp); got != tc.want {
				t.Errorf("countReversals = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestConfidenceMapping(t *testing.T) {
	fitCases := []struct {
		r2   float64
		n    int
		want string
	}{
		{0.95, 12, ConfidenceHigh},
		{0.95, 9, ConfidenceMedium},
		{0.75, 6, ConfidenceMedium},
		{0.75, 4, ConfidenceLow},
		{0.50, 50, ConfidenceLow},
	}
	for _, tc := range fitCases {
		if got := confidenceFromFit(tc.r2, tc.n); got != tc.want {
			t.Errorf("confidenceFromFit(%v,%d) = %s, want %s", tc.r2, tc.n, got, tc.want)
		}
	}
	if got := confidenceFromLevel(95, 12); got != ConfidenceHigh {
		t.Errorf("confidenceFromLevel(95,12) = %s, want high", got)
	}
	if got := confidenceFromLevel(70, 6); got != ConfidenceMedium {
		t.Errorf("confidenceFromLevel(70,6) = %s, want medium", got)
	}
	if got := confidenceFromReversals(9, 20); got != ConfidenceHigh {
		t.Errorf("confidenceFromReversals(9,20) = %s, want high", got)
	}
	if got := confidenceFromReversals(4, 8); got != ConfidenceMedium {
		t.Errorf("confidenceFromReversals(4,8) = %s, want medium", got)
	}
	if got := confidenceFromReversals(3, 30); got != ConfidenceLow {
		t.Errorf("confidenceFromReversals(3,30) = %s, want low", got)
	}
}

func TestObserveAttachesDiagnosisToAlerts(t *testing.T) {
	e := NewEngine()
	e.AddRule(&RSSGrowthRule{MinBytesPerSample: 1})
	rss := rampU(100*mb, 1*mb, 10)
	cpu := flatF(10, 10)
	pushSeries(e, 7, "leaky", rss[:9], cpu[:9])
	alerts := e.Observe(collector.Event{
		Timestamp: time.Date(2026, 1, 1, 0, 0, 9, 0, time.UTC),
		Processes: []collector.ProcessInfo{{PID: 7, Name: "leaky", Memory: rss[9], CPUPercent: cpu[9]}},
	})
	if len(alerts) == 0 {
		t.Fatal("expected an rss_growth alert")
	}
	a := alerts[0]
	if a.Diagnosis == nil {
		t.Fatal("alert.Diagnosis is nil; Observe must enrich PID alerts")
	}
	if !strings.Contains(a.Diagnosis.Summary, "memory leak") {
		t.Errorf("Diagnosis.Summary = %q, want memory-leak classification", a.Diagnosis.Summary)
	}
	if !strings.Contains(a.Detail, "R²=") {
		t.Errorf("rss_growth Detail should preserve R²: %q", a.Detail)
	}
	// System-wide alerts (PID 0) must never be enriched.
	e2 := NewEngine()
	e2.AddRule(&DiskFillRule{MinUsagePercent: 90})
	out := e2.Observe(collector.Event{
		Timestamp: time.Now(),
		Disk:      collector.DiskInfo{Partitions: []collector.DiskPartitionInfo{{MountPoint: "/", UsagePercent: 95}}},
	})
	if len(out) != 1 || out[0].Diagnosis != nil {
		t.Errorf("PID-less alert must not carry a Diagnosis: %+v", out)
	}
}

func TestDiagnoseDeterministicOrder(t *testing.T) {
	e := NewEngine()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 12; i++ {
		e.Observe(collector.Event{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Processes: []collector.ProcessInfo{
				{PID: 9, Name: "spin", Memory: 100 * mb, CPUPercent: 95},
				{PID: 3, Name: "leak", Memory: uint64(100*mb + i*mb), CPUPercent: 10},
			},
		})
	}
	diags := e.Diagnose()
	if len(diags) != 2 {
		t.Fatalf("expected 2 diagnoses, got %d: %+v", len(diags), diags)
	}
	if !strings.Contains(diags[0].Summary, "pid 3") || !strings.Contains(diags[1].Summary, "pid 9") {
		t.Errorf("diagnoses not sorted by PID: [%q, %q]", diags[0].Summary, diags[1].Summary)
	}
}

func TestCPUForPIDAlignedWithRSS(t *testing.T) {
	h := NewHistory(10)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		procs := []collector.ProcessInfo{{PID: 1, Name: "a", Memory: uint64(i), CPUPercent: float64(i)}}
		if i == 2 { // PID absent this tick
			procs = nil
		}
		h.Push(collector.Event{Timestamp: base.Add(time.Duration(i) * time.Second), Processes: procs})
	}
	rss, cpu := h.RSSForPID(1), h.CPUForPID(1)
	if len(rss) != 4 || len(cpu) != 4 {
		t.Fatalf("len(rss)=%d len(cpu)=%d, want 4 and 4", len(rss), len(cpu))
	}
	ts, rss2, cpu2 := h.SeriesForPID(1)
	if len(ts) != 4 || len(rss2) != 4 || len(cpu2) != 4 {
		t.Fatalf("SeriesForPID lengths = %d/%d/%d, want 4", len(ts), len(rss2), len(cpu2))
	}
	for i := range rss2 {
		if float64(rss2[i]) != cpu2[i] { // series built with Memory==CPUPercent==i
			t.Errorf("misaligned at %d: rss=%d cpu=%v", i, rss2[i], cpu2[i])
		}
	}
	if h.Len() != 5 {
		t.Errorf("Len = %d, want 5", h.Len())
	}
	if got := h.PIDs(); len(got) != 1 || got[0] != 1 {
		t.Errorf("PIDs = %v, want [1]", got)
	}
}
