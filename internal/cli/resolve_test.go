package cli

import (
	"strings"
	"testing"
)

// TestNewResolveCmdRejectsUnsupportedRuntime pins down the "resolve
// --runtime jruby is rejected" half of the done-when criterion for
// RuntimeRuby: the validation switch in newResolveCmd's RunE must refuse an
// unknown enum value before ever calling procbind.Resolve (which would
// otherwise scan every live process for no reason).
func TestNewResolveCmdRejectsUnsupportedRuntime(t *testing.T) {
	cmd := newResolveCmd()
	if err := cmd.Flags().Set("runtime", "jruby"); err != nil {
		t.Fatal(err)
	}
	err := cmd.RunE(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported runtime") {
		t.Fatalf("jruby should be rejected up front (before scanning any process), got %v", err)
	}
}

// TestNewResolveCmdAcceptsRubyRuntime pins down the "resolve --runtime ruby
// works" half: "ruby" must pass the same validation switch and reach
// procbind.Resolve, never be rejected as an unsupported runtime the way an
// unknown value like "jruby" is. This was previously only checked by hand.
func TestNewResolveCmdAcceptsRubyRuntime(t *testing.T) {
	cmd := newResolveCmd()
	if err := cmd.Flags().Set("runtime", "ruby"); err != nil {
		t.Fatal(err)
	}
	// A plain test sandbox is not expected to have a live ruby process
	// matching zero other selectors, so procbind.Resolve legitimately
	// returns a "no process matched" (or, rarely, "ambiguous") error here.
	// The only thing this test pins down is that "ruby" clears validation.
	if err := cmd.RunE(cmd, nil); err != nil && strings.Contains(err.Error(), "unsupported runtime") {
		t.Fatalf("ruby runtime should pass validation, got %v", err)
	}
}
