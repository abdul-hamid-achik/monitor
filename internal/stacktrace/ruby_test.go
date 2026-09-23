package stacktrace

import "testing"

func TestRubyFatalFields(t *testing.T) {
	block := Block{Lines: []string{
		`workload.rb:42:in 'Object#detonate': ruby workload: intentional uncaught failure at t=40.1s (RuntimeError)`,
		"\tfrom workload.rb:64:in '<main>'",
	}, LineStart: 1, LineEnd: 2}
	ex := parseRubyFatal(block)
	if ex == nil {
		t.Fatal("parseRubyFatal returned nil")
	}
	if ex.Type != "RuntimeError" || ex.Value != "ruby workload: intentional uncaught failure at t=40.1s" {
		t.Errorf("ex = %+v", ex)
	}
	if ex.Level != LevelFatal || ex.Handled == nil || *ex.Handled {
		t.Errorf("ex level/handled = %s/%v, want fatal/false", ex.Level, ex.Handled)
	}
	if len(ex.Frames) != 2 {
		t.Fatalf("got %d frames, want 2: %+v", len(ex.Frames), ex.Frames)
	}
	// Ruby prints the crash frame first ("detonate"), the caller
	// ("<main>") second; oldest-first with crash last means <main> comes
	// before detonate after reversal.
	if ex.Frames[0].Function != "<main>" || ex.Frames[1].Function != "Object#detonate" {
		t.Errorf("frames = %+v, want [<main>, Object#detonate]", ex.Frames)
	}
	if ex.Frames[1].Lineno != 42 {
		t.Errorf("crash frame line = %d, want 42", ex.Frames[1].Lineno)
	}
}

func TestRubyHandledNoTypeOrValue(t *testing.T) {
	block := Block{Lines: []string{
		"workload.rb:26:in 'Object#flaky_parse'",
		"workload.rb:31:in 'Object#tick_errors'",
	}}
	ex := parseRubyHandled(block)
	if ex == nil {
		t.Fatal("parseRubyHandled returned nil")
	}
	if ex.Type != "" || ex.Value != "" {
		t.Errorf("ex = %+v, want empty Type/Value (the format carries no message)", ex)
	}
	if ex.Level != LevelError || ex.Handled == nil || !*ex.Handled {
		t.Errorf("ex level/handled = %s/%v, want error/true", ex.Level, ex.Handled)
	}
	if len(ex.Frames) != 2 || ex.Frames[len(ex.Frames)-1].Function != "Object#flaky_parse" {
		t.Errorf("frames = %+v, want flaky_parse last (crash frame)", ex.Frames)
	}
}
