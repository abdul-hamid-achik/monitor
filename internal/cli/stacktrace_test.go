package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStacktraceParseFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.stderr.txt")
	content := "Error: flakyParse: boom\n    at flakyParse (/repo/app/workload.js:31:11)\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cmd := newStacktraceParseCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--file", path, "--project", "acme", "--service", "widget-api"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d NDJSON lines, want 1: %q", len(lines), out.String())
	}
	var got parsedEvent
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if got.Project != "acme" || got.Service != "widget-api" {
		t.Errorf("project/service = %q/%q, want acme/widget-api", got.Project, got.Service)
	}
	if got.Exception == nil || got.Type != "Error" || got.Value != "flakyParse: boom" {
		t.Fatalf("exception = %+v", got.Exception)
	}
	if len(got.Frames) != 1 || got.Frames[0].Function != "flakyParse" {
		t.Errorf("frames = %+v", got.Frames)
	}
}

func TestStacktraceParseStdin(t *testing.T) {
	cmd := newStacktraceParseCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetIn(strings.NewReader("panic: boom\n\ngoroutine 1 [running]:\nmain.main()\n\t/repo/app/main.go:10 +0x1\n"))
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d NDJSON lines, want 1: %q", len(lines), out.String())
	}
	var got parsedEvent
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if got.Project != "" || got.Service != "" {
		t.Errorf("expected no project/service tags, got %q/%q", got.Project, got.Service)
	}
	if got.Exception == nil || got.Parser != "gopanic" {
		t.Fatalf("exception = %+v", got.Exception)
	}
}

func TestStacktraceParseCleanInputProducesNoOutput(t *testing.T) {
	cmd := newStacktraceParseCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetIn(strings.NewReader("just a normal log line\nanother one\nno errors here\n"))
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("expected no output for clean input, got %q", out.String())
	}
}

func TestStacktraceParseMissingFile(t *testing.T) {
	cmd := newStacktraceParseCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--file", "/nonexistent/does-not-exist.txt"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error for a missing --file")
	}
}

func TestStacktraceCmdHidden(t *testing.T) {
	cmd := newStacktraceCmd()
	if !cmd.Hidden {
		t.Error("stacktrace command should be Hidden")
	}
}

func TestFindGitRoot(t *testing.T) {
	if _, ok := findGitRoot(t.TempDir()); ok {
		t.Error("a fresh temp dir outside any repo should not resolve a git root")
	}
	// The monitor repo itself (the CLI's own working directory when built
	// and run normally) does have one.
	if _, ok := findGitRoot("."); !ok {
		t.Skip("not running inside the monitor git checkout/worktree")
	}
}
