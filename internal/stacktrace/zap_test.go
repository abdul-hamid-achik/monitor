package stacktrace

import "testing"

func TestZapJSONWithEmbeddedStacktrace(t *testing.T) {
	line := `{"level":"error","ts":"2026-09-22T10:04:37.123Z","msg":"request failed","request_id":"abc123","stacktrace":"work failed\nmain.doWork\n\t/repo/examples/polyglot/go-zap-stdout/main.go:20\nmain.main\n\t/repo/examples/polyglot/go-zap-stdout/main.go:10"}`
	exs := Detect(line)
	if len(exs) != 1 {
		t.Fatalf("got %d exceptions, want 1: %+v", len(exs), exs)
	}
	ex := exs[0]
	if ex.Parser != "zap" || ex.Value != "request failed" {
		t.Fatalf("ex = %+v", ex)
	}
	if ex.Level != LevelError || ex.Handled == nil || !*ex.Handled {
		t.Errorf("ex level/handled = %s/%v, want error/true", ex.Level, ex.Handled)
	}
	if len(ex.Frames) != 2 {
		t.Fatalf("got %d frames, want 2: %+v", len(ex.Frames), ex.Frames)
	}
	top := ex.Frames[len(ex.Frames)-1]
	if top.Function != "main.doWork" || top.Lineno != 20 {
		t.Errorf("top frame = %+v, want main.doWork:20", top)
	}
}

func TestZapConsoleFatalLevel(t *testing.T) {
	line := "2026-09-22T10:04:37.123Z\tfatal\tunrecoverable\t{}"
	exs := Detect(line)
	if len(exs) != 1 {
		t.Fatalf("got %d exceptions, want 1", len(exs))
	}
	if exs[0].Level != LevelFatal || exs[0].Handled == nil || *exs[0].Handled {
		t.Errorf("ex = %+v, want fatal/unhandled", exs[0])
	}
}

func TestZapInfoLineIsNotAnException(t *testing.T) {
	line := "2026-09-22T10:04:37.123Z\tinfo\tserver started\t{}"
	if exs := Detect(line); len(exs) != 0 {
		t.Fatalf("got %d exceptions from an info-level zap line, want 0: %+v", len(exs), exs)
	}
}
