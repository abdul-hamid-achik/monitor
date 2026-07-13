package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

func TestValidateAnalyzeOptions(t *testing.T) {
	for _, tc := range []struct {
		name             string
		window, interval time.Duration
		pid              int32
		wantErr          bool
	}{
		{name: "valid", window: 10 * time.Second, interval: time.Second},
		{name: "focused", window: time.Second, interval: 100 * time.Millisecond, pid: 42},
		{name: "zero window", interval: time.Second, wantErr: true},
		{name: "too long", window: 61 * time.Second, interval: time.Second, wantErr: true},
		{name: "interval longer", window: time.Second, interval: 2 * time.Second, wantErr: true},
		{name: "negative pid", window: time.Second, interval: time.Second, pid: -1, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAnalyzeOptions(tc.window, tc.interval, tc.pid)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestPrintAnalyzeReport(t *testing.T) {
	report := analyzeReport{
		Window: "5s", PID: 42, Samples: 6,
		Diagnoses: []collector.Diagnosis{{
			Summary: "memory is rising", Confidence: "high",
			Evidence: []string{"RSS slope +10 MB/min"}, NextActions: []string{"monitor investigate 42"},
		}},
	}
	var buf bytes.Buffer
	if err := printAnalyzeReport(&buf, report); err != nil {
		t.Fatalf("printAnalyzeReport: %v", err)
	}
	for _, want := range []string{"6 samples", "pid 42", "[high]", "RSS slope", "monitor investigate 42"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q: %s", want, buf.String())
		}
	}

	buf.Reset()
	report.Healthy = true
	report.Diagnoses = nil
	if err := printAnalyzeReport(&buf, report); err != nil {
		t.Fatalf("printAnalyzeReport healthy: %v", err)
	}
	if !strings.Contains(buf.String(), "No cross-signal") {
		t.Fatalf("healthy output = %q", buf.String())
	}
}
