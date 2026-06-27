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

// TestSymbolAtDecode locks the SymbolAt struct tags against the real
// `codemap symbol-at --json` output shape.
func TestSymbolAtDecode(t *testing.T) {
	js := `{"file":"a.go","line":45,"symbol":"Alert","fqn":"collector.Alert","kind":"type","start_line":43,"end_line":49,"resolution":"enclosing"}`
	got, err := decodeJSON[SymbolAt]([]byte(js), "codemap symbol-at")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.FQN != "collector.Alert" || got.Kind != "type" || got.Resolution != "enclosing" || got.StartLine != 43 || got.EndLine != 49 {
		t.Errorf("decoded = %+v", got)
	}
	// The "none" shape (unindexed/unresolved) must decode cleanly too.
	none, err := decodeJSON[SymbolAt]([]byte(`{"file":"a.go","line":1,"resolution":"none"}`), "codemap symbol-at")
	if err != nil || none.Resolution != "none" || none.FQN != "" {
		t.Errorf("none decode = %+v, err %v", none, err)
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