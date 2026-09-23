package stacktrace

import (
	"os"
	"path/filepath"
	"testing"
)

func readDogfood(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "dogfood", name))
	if err != nil {
		t.Fatalf("read dogfood fixture %s: %v", name, err)
	}
	return string(b)
}

// distinctKinds groups a set of Exceptions by (Type, Value) so the golden
// table's "N raw occurrences -> M distinct exceptions" claim is testable:
// the dogfood captures repeat the same caught error many times before a
// single, differently-worded crash.
func distinctKinds(exs []*Exception) map[string]int {
	out := map[string]int{}
	for _, ex := range exs {
		out[ex.Type+"|"+ex.Value]++
	}
	return out
}

func TestDogfoodNode(t *testing.T) {
	exs := Detect(readDogfood(t, "node-inspect.stderr.txt"))
	if len(exs) != 10 {
		t.Fatalf("got %d exceptions, want 10 (9 caught + 1 uncaught)", len(exs))
	}
	kinds := distinctKinds(exs)
	if len(kinds) != 2 {
		t.Fatalf("got %d distinct exception kinds, want 2 (%v)", len(kinds), kinds)
	}
	last := exs[len(exs)-1]
	if last.Level != LevelFatal || last.Handled == nil || *last.Handled {
		t.Errorf("last node exception = %+v, want fatal/unhandled", last)
	}
	if len(last.Frames) == 0 || last.Frames[len(last.Frames)-1].Function != "detonate" {
		t.Errorf("last node exception crash frame = %+v, want detonate last", last.Frames)
	}
	for _, ex := range exs[:9] {
		if ex.Level != LevelError || ex.Handled == nil || !*ex.Handled {
			t.Errorf("caught node exception = %+v, want error/handled", ex)
		}
		if got := ex.Frames[len(ex.Frames)-1].Function; got != "flakyParse" {
			t.Errorf("caught node exception crash frame = %q, want flakyParse", got)
		}
	}
}

func TestDogfoodDeno(t *testing.T) {
	exs := Detect(readDogfood(t, "deno-inspect.stderr.txt"))
	if len(exs) != 10 {
		t.Fatalf("got %d exceptions, want 10", len(exs))
	}
	if len(distinctKinds(exs)) != 2 {
		t.Fatalf("got %d distinct kinds, want 2", len(distinctKinds(exs)))
	}
	last := exs[len(exs)-1]
	if last.Level != LevelFatal {
		t.Errorf("last deno exception level = %q, want fatal", last.Level)
	}
	if last.Value == "" || last.Type != "Error" {
		t.Errorf("last deno exception = %+v", last)
	}
}

func TestDogfoodBun(t *testing.T) {
	exs := Detect(readDogfood(t, "bun-inspect.stderr.txt"))
	if len(exs) != 10 {
		t.Fatalf("got %d exceptions, want 10", len(exs))
	}
	if len(distinctKinds(exs)) != 2 {
		t.Fatalf("got %d distinct kinds, want 2", len(distinctKinds(exs)))
	}
	last := exs[len(exs)-1]
	if last.Level != LevelFatal {
		t.Errorf("last bun exception level = %q, want fatal", last.Level)
	}
	if len(last.Frames) == 0 || last.Frames[len(last.Frames)-1].Function != "detonate" {
		t.Errorf("last bun exception crash frame = %+v, want detonate last", last.Frames)
	}
}

func TestDogfoodPython(t *testing.T) {
	exs := Detect(readDogfood(t, "python.stderr.txt"))
	if len(exs) != 11 {
		t.Fatalf("got %d exceptions, want 11 (10 caught + 1 uncaught)", len(exs))
	}
	if len(distinctKinds(exs)) != 2 {
		t.Fatalf("got %d distinct kinds, want 2 (%v)", len(distinctKinds(exs)), distinctKinds(exs))
	}
	last := exs[len(exs)-1]
	if last.Type != "RuntimeError" || last.Level != LevelFatal || last.Handled == nil || *last.Handled {
		t.Errorf("last python exception = %+v, want fatal RuntimeError", last)
	}
	for _, ex := range exs[:10] {
		if ex.Type != "ValueError" || ex.Level != LevelError || ex.Handled == nil || !*ex.Handled {
			t.Errorf("caught python exception = %+v, want handled ValueError", ex)
		}
	}
}

func TestDogfoodRuby(t *testing.T) {
	exs := Detect(readDogfood(t, "ruby.stderr.txt"))
	var fatal, handled int
	for _, ex := range exs {
		if ex.Parser != "ruby" {
			t.Fatalf("unexpected parser %q in ruby fixture", ex.Parser)
		}
		if ex.Level == LevelFatal {
			fatal++
		} else {
			handled++
		}
	}
	if fatal != 1 {
		t.Errorf("got %d fatal ruby exceptions, want 1", fatal)
	}
	// The dogfood capture has 10 rescue-printed backtraces before the
	// final uncaught one (the roadmap's "9 handled" was a rough estimate
	// written before this real capture existed).
	if handled != 10 {
		t.Errorf("got %d handled ruby exceptions, want 10", handled)
	}
}

func TestDogfoodGoPprof(t *testing.T) {
	exs := Detect(readDogfood(t, "go-pprof.stderr.txt"))
	if len(exs) != 1 {
		t.Fatalf("got %d exceptions, want 1 (only the panic; plain caught prints have no stack)", len(exs))
	}
	ex := exs[0]
	if ex.Type != "panic" || ex.Level != LevelFatal {
		t.Errorf("go panic exception = %+v", ex)
	}
	if len(ex.Frames) == 0 || ex.Frames[len(ex.Frames)-1].Function != "main.main" {
		t.Errorf("go panic crash frame = %+v, want main.main last", ex.Frames)
	}
}

func TestDogfoodGoCrash(t *testing.T) {
	exs := Detect(readDogfood(t, "go-crash.stderr.txt"))
	if len(exs) != 1 {
		t.Fatalf("got %d exceptions, want 1: %+v", len(exs), exs)
	}
	ex := exs[0]
	if ex.Type != "panic" || ex.Value != "go-crash workload: intentional uncaught failure" {
		t.Errorf("ex = %+v", ex)
	}
	if ex.Level != LevelFatal || ex.Handled == nil || *ex.Handled {
		t.Errorf("level/handled = %s/%v, want fatal/false", ex.Level, ex.Handled)
	}
	if len(ex.Frames) != 1 {
		t.Fatalf("got %d frames, want 1: %+v", len(ex.Frames), ex.Frames)
	}
	top := ex.Frames[0]
	if top.Function != "main.main" || top.Lineno != 11 {
		t.Errorf("crash frame = %+v, want main.main:11", top)
	}
	if !InApp(top, "/repo") {
		t.Errorf("crash frame %+v should be in_app under /repo", top)
	}
}

func TestDogfoodGoPlainHasNoPanic(t *testing.T) {
	// go-plain.stderr.txt was captured before the workload's 40s timer
	// fired, so it only has plain (non-stack) caught-error prints.
	exs := Detect(readDogfood(t, "go-plain.stderr.txt"))
	if len(exs) != 0 {
		t.Fatalf("got %d exceptions from go-plain (no panic in this capture), want 0: %+v", len(exs), exs)
	}
}
