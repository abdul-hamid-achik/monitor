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

func TestExtractMainScriptPythonIsolatedMode(t *testing.T) {
	cwd := "/app"
	// Regression: Python's "-I" (isolated mode) takes NO value, unlike
	// Ruby's "-I" (load path). Both runtimes share the same flag-walking
	// loop, so "-I" must only consume a value when the runtime is Ruby.
	got := extractMainScript(RuntimePython, []string{"python3", "-I", "app.py"}, cwd)
	if got != "/app/app.py" {
		t.Fatalf("python3 -I app.py = %q, want /app/app.py", got)
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
	// Regression: Ruby's "-p" (autoprint) and "-c" (syntax check only) take
	// NO separate value, unlike Node's "-p/--print" and Python's "-c"
	// (eval), which share the same flag switch. Consuming the next argv
	// element here used to eat Ruby's real script argument.
	got = extractMainScript(RuntimeRuby, []string{"ruby", "-p", "script.rb"}, cwd)
	if got != "/app/script.rb" {
		t.Fatalf("ruby -p script.rb = %q, want /app/script.rb", got)
	}
	got = extractMainScript(RuntimeRuby, []string{"ruby", "-c", "script.rb"}, cwd)
	if got != "/app/script.rb" {
		t.Fatalf("ruby -c script.rb = %q, want /app/script.rb", got)
	}
	// "bundle exec ruby <script>": bundle and exec are skipped, then the
	// repeated interpreter name "ruby" is not itself a script and is
	// skipped too, landing on the real entry point. This is Bundler's
	// Kernel#exec path (a real execve into the interpreter), so a live
	// process shows ordinary argv exactly like this.
	got = extractMainScript(RuntimeRuby, []string{"bundle", "exec", "ruby", "app.rb"}, cwd)
	if got != "/app/app.rb" {
		t.Fatalf("bundle exec ruby main = %q", got)
	}
	// "bundle exec rails server" with this synthetic argv shape yields no
	// main_script. A live process never actually preserves this shape: see
	// TestExtractMainScriptRubyBundlerProctitle below for what a real
	// "bundle exec rails server" process's argv looks like once Bundler's
	// kernel_load path rewrites the process title. Guessing a bare "rails"
	// basename against cwd only produced a path that does not exist, so
	// this case is now honestly empty instead.
	got = extractMainScript(RuntimeRuby, []string{"bundle", "exec", "rails", "server"}, cwd)
	if got != "" {
		t.Fatalf("bundle exec rails server (synthetic argv) main = %q, want \"\" (no invented path)", got)
	}
}

func TestExtractMainScriptRubyBundlerProctitle(t *testing.T) {
	cwd := "/app"
	// Bundler's kernel_load path (used for ruby-shebang bin scripts like
	// bin/rails, bin/rake, bin/puma) rewrites the live process's argv via
	// Process.setproctitle("#{file} #{args}"): argv[0] becomes one
	// whitespace-joined string and every later element is blanked. The
	// first token of that joined string is the real, already-resolved
	// script path.
	got := extractMainScript(RuntimeRuby, []string{"/app/bin/rails server", "", ""}, cwd)
	if got != "/app/bin/rails" {
		t.Fatalf("bundler proctitle main = %q, want /app/bin/rails", got)
	}
	// A relative script path in the rewritten title still resolves against cwd.
	got = extractMainScript(RuntimeRuby, []string{"bin/rake db:migrate", ""}, cwd)
	if got != "/app/bin/rake" {
		t.Fatalf("bundler proctitle relative main = %q, want /app/bin/rake", got)
	}
	// A normal (non-rewritten) argv must not be misread as a proctitle: no
	// element of a real argv legitimately contains internal whitespace.
	got = extractMainScript(RuntimeRuby, []string{"ruby", "app.rb"}, cwd)
	if got != "/app/app.rb" {
		t.Fatalf("plain ruby argv must not be treated as a rewritten proctitle, got %q", got)
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
