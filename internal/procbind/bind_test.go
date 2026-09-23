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
		{"ruby", "", nil, RuntimeRuby},
		{"ruby3.4", "", nil, RuntimeRuby},
		{"ruby-3.1", "", nil, RuntimeRuby},
		{"myapp", "", []string{"/usr/bin/ruby", "worker.rb"}, RuntimeRuby},
		{"bundle", "", []string{"bundle", "exec", "ruby", "app.rb"}, RuntimeRuby},
		{"bundle", "", []string{"bundle", "exec", "rails", "server"}, RuntimeRuby},
		{"bundle", "", []string{"bundle", "exec", "rake", "db:migrate"}, RuntimeRuby},
		{"bundle", "", []string{"bundle", "exec", "puma", "-C", "config/puma.rb"}, RuntimeRuby},
		{"bundle", "", []string{"bundle", "--gemfile=Gemfile", "exec", "rails", "server"}, RuntimeRuby},
		{"bundle", "", []string{"bundle", "install"}, RuntimeUnknown},
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
	// Regression: a bare --inspect (no separate value in Node/Deno/Bun) must
	// not eat the main script argument that follows it.
	got = extractMainScript(RuntimeNode, []string{"node", "--inspect", "server.js"}, cwd)
	if got != "/app/server.js" {
		t.Fatalf("main with bare --inspect = %q, want /app/server.js", got)
	}
	got = extractMainScript(RuntimeNode, []string{"node", "--inspect-brk", "server.js"}, cwd)
	if got != "/app/server.js" {
		t.Fatalf("main with bare --inspect-brk = %q, want /app/server.js", got)
	}
}

func TestExtractMainScriptRuby(t *testing.T) {
	cwd := "/app"
	// Plain ruby script invocation.
	got := extractMainScript(RuntimeRuby, []string{"ruby", "workload.rb"}, cwd)
	if got != "/app/workload.rb" {
		t.Fatalf("ruby script main = %q", got)
	}
	// Ruby flags that take a separate value: -r (require), -I (load path),
	// -e (eval, skipped along with its code argument), -C (chdir).
	got = extractMainScript(RuntimeRuby, []string{"ruby", "-r", "bundler/setup", "-I", "lib", "app.rb"}, cwd)
	if got != "/app/app.rb" {
		t.Fatalf("ruby with -r/-I = %q", got)
	}
	// "bundle exec ruby <script>": bundle and exec are skipped, then the
	// repeated interpreter name "ruby" is not itself a script and is
	// skipped too, landing on the real entry point.
	got = extractMainScript(RuntimeRuby, []string{"bundle", "exec", "ruby", "app.rb"}, cwd)
	if got != "/app/app.rb" {
		t.Fatalf("bundle exec ruby main = %q", got)
	}
	// "bundle exec rails server": the bundler-installed bin script has no
	// .rb extension; main_script still resolves to it (not "server").
	got = extractMainScript(RuntimeRuby, []string{"bundle", "exec", "rails", "server"}, cwd)
	if got != "/app/rails" {
		t.Fatalf("bundle exec rails server main = %q", got)
	}
}

func TestExtractInspectAddr(t *testing.T) {
	tests := []struct {
		rt   Runtime
		args []string
		want string
	}{
		{RuntimeNode, []string{"node", "a.js"}, ""},
		{RuntimeNode, []string{"node", "--inspect", "a.js"}, "127.0.0.1:9229"},
		{RuntimeNode, []string{"node", "--inspect=9230", "a.js"}, "127.0.0.1:9230"},
		{RuntimeNode, []string{"node", "--inspect=0.0.0.0:9230", "a.js"}, "0.0.0.0:9230"},
		{RuntimeNode, []string{"node", "--inspect-brk=9240", "a.js"}, "127.0.0.1:9240"},
		{RuntimeNode, []string{"node", "--inspect-port", "9250", "a.js"}, "127.0.0.1:9250"},
		{RuntimeNode, []string{"node", "--inspect-wait", "a.js"}, "127.0.0.1:9229"},
		{RuntimeNode, []string{"node", "--inspect-wait=9260", "a.js"}, "127.0.0.1:9260"},
		// Bare --inspect takes no separate value: a following numeric-looking
		// token is the script (or a script argument), never an implicit port.
		{RuntimeNode, []string{"node", "--inspect", "9229", "a.js"}, "127.0.0.1:9229"},
		// Bun's bare --inspect defaults to 6499, not the Node/Deno 9229.
		{RuntimeBun, []string{"bun", "--inspect", "app.ts"}, "127.0.0.1:6499"},
		{RuntimeBun, []string{"bun", "--inspect-brk", "app.ts"}, "127.0.0.1:6499"},
		{RuntimeBun, []string{"bun", "--inspect=6500", "app.ts"}, "127.0.0.1:6500"},
	}
	for _, tt := range tests {
		if got := extractInspectAddr(tt.rt, tt.args); got != tt.want {
			t.Errorf("extractInspectAddr(%v, %v)=%q want %q", tt.rt, tt.args, got, tt.want)
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

func TestFindCodebaseRootRubyMarkers(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "app", "lib")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Gemfile"), []byte("source 'https://rubygems.org'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".ruby-version"), []byte("3.4.8\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, markers := FindCodebaseRoot(nested)
	if got != root {
		t.Fatalf("root = %q want %q", got, root)
	}
	found := map[string]bool{}
	for _, m := range markers {
		found[m] = true
	}
	if !found["Gemfile"] || !found[".ruby-version"] {
		t.Fatalf("markers = %v, want Gemfile and .ruby-version", markers)
	}
}

func TestMatchesBindingDisambiguatesRuntimeRootAndEntryPoint(t *testing.T) {
	opts := ResolveOptions{
		Runtime:          RuntimeNode,
		CodebaseRoot:     "/workspace/worker",
		MainScriptSuffix: "dist/worker/src/server.js",
	}
	wantRoot := canonicalPath(opts.CodebaseRoot)
	wantSuffix := filepath.Clean(opts.MainScriptSuffix)
	app := Binding{
		Runtime:      RuntimeNode,
		CodebaseRoot: "/workspace/worker",
		MainScript:   "/workspace/worker/dist/worker/src/server.js",
	}
	if !matchesBinding(app, opts, wantRoot, wantSuffix) {
		t.Fatal("expected the exact worker binding to match")
	}
	wrapper := app
	wrapper.MainScript = "/opt/yarn/lib/cli.js"
	if matchesBinding(wrapper, opts, wantRoot, wantSuffix) {
		t.Fatal("the yarn wrapper must not match the worker entrypoint")
	}
	otherRuntime := app
	otherRuntime.Runtime = RuntimeBun
	if matchesBinding(otherRuntime, opts, wantRoot, wantSuffix) {
		t.Fatal("a different runtime must not match")
	}
}
