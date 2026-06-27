package ecosystem

import (
	"context"
	"testing"
)

func TestProbeRunsEvenWithMissingTools(t *testing.T) {
	// Probe must never panic and must return a Status struct.
	st := Probe(context.Background())
	// At least one of these should be populated; we don't assert which.
	any := st.Codemap.Available || st.Fcheap.Available || st.Veclite.Available || st.Tmux.Available
	if !any {
		t.Log("Probe found no tools on PATH (acceptable in minimal envs)")
	}
}

func TestFirstLine(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"only", "only"},
		{"first\nsecond", "first"},
		{"first\rsecond", "first"},
		{"", ""},
	}
	for _, tt := range tests {
		got := firstLine(tt.in)
		if got != tt.want {
			t.Errorf("firstLine(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestRunMissingBinary(t *testing.T) {
	_, err := run(context.Background(), "definitely-not-on-path-12345")
	if err == nil {
		t.Error("expected error for missing binary")
	}
}