package ecosystem

import (
	"context"
	"errors"
	"testing"
)

func TestDecodeJSON(t *testing.T) {
	type rec struct {
		Name string `json:"name"`
	}
	got, err := decodeJSON[rec]([]byte(`{"name":"x"}`), "cmd")
	if err != nil || got.Name != "x" {
		t.Fatalf("decodeJSON valid = (%+v, %v)", got, err)
	}
	gotS, err := decodeJSON[[]rec]([]byte(`[{"name":"a"},{"name":"b"}]`), "cmd")
	if err != nil || len(gotS) != 2 {
		t.Fatalf("decodeJSON slice = (%+v, %v)", gotS, err)
	}

	// Malformed JSON must surface a *Wrap carrying the command and raw output,
	// and Unwrap must chain to the underlying json error.
	_, err = decodeJSON[rec]([]byte(`{bad`), "fcheap save")
	if err == nil {
		t.Fatal("expected an error on malformed JSON")
	}
	var w *Wrap
	if !errors.As(err, &w) {
		t.Fatalf("error should be *Wrap; got %T", err)
	}
	if w.Cmd != "fcheap save" || w.Output != "{bad" {
		t.Errorf("Wrap = {Cmd:%q Output:%q}, want {fcheap save, {bad}", w.Cmd, w.Output)
	}
	if w.Unwrap() == nil {
		t.Error("Wrap.Unwrap() should return the underlying json error")
	}
}

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