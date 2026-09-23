package stacktrace

import "testing"

func TestGopanicStopsAtFirstGoroutine(t *testing.T) {
	text := `fatal error: all goroutines are asleep - deadlock!

goroutine 1 [chan receive]:
main.main()
	/repo/app/main.go:10 +0x20

goroutine 2 [chan send]:
main.worker()
	/repo/app/main.go:20 +0x18
created by main.main in goroutine 1
	/repo/app/main.go:8 +0x30
`
	exs := Detect(text)
	if len(exs) != 1 {
		t.Fatalf("got %d exceptions, want 1: %+v", len(exs), exs)
	}
	ex := exs[0]
	if ex.Type != "fatal error" {
		t.Errorf("Type = %q, want %q", ex.Type, "fatal error")
	}
	if len(ex.Frames) != 1 || ex.Frames[0].Function != "main.main" {
		t.Errorf("frames = %+v, want only main.main from the first goroutine stanza", ex.Frames)
	}
}

func TestGopanicSkipsCreatedByWithinFirstStanza(t *testing.T) {
	text := `panic: boom

goroutine 5 [running]:
main.worker(...)
	/repo/app/main.go:30 +0x10
created by main.spawn
	/repo/app/main.go:12 +0x40
main.main()
	/repo/app/main.go:5 +0x8
`
	exs := Detect(text)
	if len(exs) != 1 {
		t.Fatalf("got %d exceptions, want 1: %+v", len(exs), exs)
	}
	ex := exs[0]
	if len(ex.Frames) != 2 {
		t.Fatalf("got %d frames, want 2 (created-by line skipped): %+v", len(ex.Frames), ex.Frames)
	}
	if ex.Frames[len(ex.Frames)-1].Function != "main.worker" {
		t.Errorf("crash frame = %+v, want main.worker last", ex.Frames[len(ex.Frames)-1])
	}
}
