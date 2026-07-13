package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
)

func TestNormalizeRequiredTools(t *testing.T) {
	got, err := normalizeRequiredTools([]string{"codemap,tvault", "glyph", "codemap"}, false)
	if err != nil {
		t.Fatalf("normalizeRequiredTools: %v", err)
	}
	want := []string{"codemap", "glyphrun", "tinyvault"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("required = %v, want %v", got, want)
	}

	strict, err := normalizeRequiredTools(nil, true)
	if err != nil || !reflect.DeepEqual(strict, doctorToolNames) {
		t.Fatalf("strict = %v, err %v; want %v", strict, err, doctorToolNames)
	}
	if _, err := normalizeRequiredTools([]string{"docker"}, false); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown tool error = %v", err)
	}
}

func TestMissingRequiredTools(t *testing.T) {
	status := ecosystem.Status{
		Codemap: ecosystem.ToolStatus{Available: true},
		Tmux:    ecosystem.ToolStatus{Available: true},
	}
	got := missingRequiredTools(status, []string{"tmux", "fcheap", "codemap", "veclite"})
	want := []string{"fcheap", "veclite"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("missing = %v, want %v", got, want)
	}
}

func TestDoctorHasDependencyGateFlags(t *testing.T) {
	cmd := newDoctorCmd()
	if cmd.Flags().Lookup("require") == nil || cmd.Flags().Lookup("strict") == nil || cmd.Flags().Lookup("json") == nil {
		t.Fatal("doctor should expose --require, --strict, and --json")
	}
}
