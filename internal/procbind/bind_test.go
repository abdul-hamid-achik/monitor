package procbind

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBindingJSONNeverReturnsArgv(t *testing.T) {
	b, err := json.Marshal(Binding{PID: 42, Cmdline: []string{"server", "--token", "secret"}, ArgvRedacted: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), "cmdline") || !strings.Contains(string(b), "argv_redacted") {
		t.Fatalf("binding JSON leaked argv or omitted redaction signal: %s", b)
	}
}

func TestClassifyRuntime(t *testing.T) {
	tests := []struct {
		name, exe string
		cmdline   []string
		want      Runtime
	}{
		{"node", "", nil, RuntimeNode},
		{"nodejs", "", nil, RuntimeNode},
		{"node20", "", nil, RuntimeNode},
		{"bun", "", nil, RuntimeBun},
		{"deno", "", nil, RuntimeDeno},
		{"python3", "", nil, RuntimePython},
		{"go", "", nil, RuntimeGo},
		{"myapp", "", []string{"/usr/bin/node", "server.js"}, RuntimeNode},
		{"api", "/usr/local/bin/api", nil, RuntimeUnknown},
	}
	for _, tt := range tests {
		if got := classifyRuntime(tt.name, tt.exe, tt.cmdline); got != tt.want {
			t.Errorf("classifyRuntime(%q,%q,%v)=%q want %q", tt.name, tt.exe, tt.cmdline, got, tt.want)
		}
	}
}

func TestExtractMainScript(t *testing.T) {
	cwd := "/app"
	got := extractMainScript(RuntimeNode, []string{"node", "--enable-source-maps", "dist/server.js"}, cwd)
	if got != "/app/dist/server.js" {
		t.Fatalf("main = %q", got)
	}
	got = extractMainScript(RuntimeNode, []string{"node", "--inspect=9230", "index.mjs"}, cwd)
	if got != "/app/index.mjs" {
		t.Fatalf("main with inspect = %q", got)
	}
	got = extractMainScript(RuntimeNode, []string{"node", "-r", "dotenv/config", "src/main.ts"}, cwd)
	if got != "/app/src/main.ts" {
		t.Fatalf("main with -r = %q", got)
	}
	if extractMainScript(RuntimeGo, []string{"./api"}, cwd) != "" {
		t.Fatal("go runtime should not invent a main script")
	}
}

func TestExtractInspectAddr(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"node", "a.js"}, ""},
		{[]string{"node", "--inspect", "a.js"}, "127.0.0.1:9229"},
		{[]string{"node", "--inspect=9230", "a.js"}, "127.0.0.1:9230"},
		{[]string{"node", "--inspect=0.0.0.0:9230", "a.js"}, "0.0.0.0:9230"},
		{[]string{"node", "--inspect-brk=9240", "a.js"}, "127.0.0.1:9240"},
		{[]string{"node", "--inspect-port", "9250", "a.js"}, "127.0.0.1:9250"},
	}
	for _, tt := range tests {
		if got := extractInspectAddr(tt.args); got != tt.want {
			t.Errorf("extractInspectAddr(%v)=%q want %q", tt.args, got, tt.want)
		}
	}
}

func TestFindCodebaseRoot(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "packages", "api", "src")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"monorepo"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, markers := FindCodebaseRoot(nested)
	if got != root {
		t.Fatalf("root = %q want %q", got, root)
	}
	if len(markers) == 0 || markers[0] != "package.json" {
		t.Fatalf("markers = %v", markers)
	}

	empty := t.TempDir()
	if r, m := FindCodebaseRoot(empty); r != "" || m != nil {
		t.Fatalf("empty tree should yield no root; got %q %v", r, m)
	}
}
