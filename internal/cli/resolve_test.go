package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/procbind"
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

// TestNewResolveCmdDescendantOfAloneRunsLeafResolution is the E3.2 CLI-level
// happy path for `monitor resolve --descendant-of <pid>` with no other
// selector: it must resolve through procbind.ResolveLeaf and print the
// node child's identity, not the sh wrapper's. specs/resolve_descendant.yml
// covers the same shape as a black-box subprocess spec; this pins it down
// at the Go level too, in-process (no os.Exit -- the ambiguous/exit-2 path
// is deliberately NOT exercised here; see TestPrintAmbiguousLeaf* below for
// that path's output, and specs/resolve_descendant.yml for its exit code).
func TestNewResolveCmdDescendantOfAloneRunsLeafResolution(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}
	workload, err := filepath.Abs(filepath.Join("..", "..", "examples", "polyglot", "js", "workload.js"))
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(workload); statErr != nil {
		t.Skipf("workload.js fixture not found: %v", statErr)
	}

	// "& wait" forces sh to fork a genuine child instead of exec-optimizing
	// into node with the SAME pid -- see internal/procbind/tree_test.go for
	// how this was verified live on this project's own dev box.
	cmd := exec.Command(shPath, "-c", nodePath+" "+workload+" & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sh: %v", err)
	}
	root := cmd.Process.Pid
	t.Cleanup(func() {
		if killErr := syscall.Kill(-root, syscall.SIGKILL); killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
			t.Logf("kill process group %d: %v", root, killErr)
		}
		_ = cmd.Wait()
	})

	deadline := time.Now().Add(5 * time.Second)
	var out string
	for {
		resolveCmd := newResolveCmd()
		var buf strings.Builder
		resolveCmd.SetOut(&buf)
		if setErr := resolveCmd.Flags().Set("descendant-of", fmt.Sprintf("%d", root)); setErr != nil {
			t.Fatal(setErr)
		}
		runErr := resolveCmd.RunE(resolveCmd, nil)
		if runErr == nil {
			out = buf.String()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("resolve --descendant-of %d: %v", root, runErr)
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !strings.Contains(out, "runtime=node") {
		t.Fatalf("resolve --descendant-of output = %q, want it to name runtime=node", out)
	}
	if strings.Contains(out, fmt.Sprintf("pid %d ", root)) {
		t.Fatalf("resolve --descendant-of output = %q, resolved the sh wrapper itself instead of its node child", out)
	}
}

func TestPrintAmbiguousLeafHumanFormat(t *testing.T) {
	cmd := newResolveCmd()
	var buf strings.Builder
	cmd.SetErr(&buf)
	printAmbiguousLeaf(cmd, 123, []procbind.Candidate{
		{PID: 456, Name: "node", Runtime: procbind.RuntimeNode, MainScript: "/app/a.js"},
		{PID: 789, Name: "node", Runtime: procbind.RuntimeNode, MainScript: "/app/b.js"},
	})
	out := buf.String()
	if !strings.Contains(out, "ambiguous leaf process under pid 123") {
		t.Fatalf("human ambiguous output = %q, want the pid named", out)
	}
	if !strings.Contains(out, "pid=456") || !strings.Contains(out, "pid=789") {
		t.Fatalf("human ambiguous output = %q, want both candidates listed", out)
	}
}

func TestPrintAmbiguousLeafJSONFormat(t *testing.T) {
	cmd := newResolveCmd()
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	// printAmbiguousLeaf's JSON branch writes through WriteJSON, which (like
	// every other --json command in this CLI) writes straight to os.Stdout
	// rather than cmd.OutOrStdout() -- so this redirects os.Stdout for the
	// duration of the call, matching the same pattern doctor_test.go uses
	// for `doctor --json`.
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	printAmbiguousLeaf(cmd, 123, []procbind.Candidate{{PID: 456, Name: "node", Runtime: procbind.RuntimeNode, MainScript: "/app/a.js"}})
	_ = w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)

	if !strings.Contains(string(out), `"error": "ambiguous"`) {
		t.Fatalf("json ambiguous output = %s, want an \"ambiguous\" error field", out)
	}
	if !strings.Contains(string(out), `"pid": 456`) {
		t.Fatalf("json ambiguous output = %s, want candidate pid 456 listed", out)
	}
}
