package procbind

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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
	// -e (eval, skipped along with its code argument), -C (chdir). All four
	// are asserted together so this comment cannot drift from what the test
	// actually exercises again.
	got = extractMainScript(RuntimeRuby, []string{"ruby", "-r", "bundler/setup", "-I", "lib", "-e", "puts 1", "-C", "/tmp", "app.rb"}, cwd)
	if got != "/app/app.rb" {
		t.Fatalf("ruby with -r/-I/-e/-C = %q", got)
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
	// whitespace-joined string. The first token of that joined string is
	// the real, already-resolved script path. What follows argv[0] is
	// deliberately NOT asserted to be blank here: see the two live-shape
	// regression cases below, which pin down what this project's own
	// verification actually observed on a live process.
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

// TestExtractMainScriptRubyBundlerProctitleLiveShapes pins down the two
// actual live argv shapes observed by running real "bundle exec <script>
// arg1 arg2" processes on this project's own darwin dev box (killed
// immediately after inspection) — not synthetic guesses. A prior revision
// of extractBundlerProctitleScript required len(cmdline)>=2 with every
// element after index 0 equal to "", which rejected BOTH of these real
// shapes and never actually fired on any live Bundler-loaded process.
func TestExtractMainScriptRubyBundlerProctitleLiveShapes(t *testing.T) {
	cwd := "/app"
	// System Ruby 2.6.10 + Bundler 1.17.2: "bundle exec mysvc arg1 arg2"
	// collapses to a cmdline of length 1. Nothing at all follows argv[0] —
	// not even a blank element — because there is nothing left to blank.
	got := extractMainScript(RuntimeRuby, []string{"/app/bin/mysvc arg1 arg2"}, cwd)
	if got != "/app/bin/mysvc" {
		t.Fatalf("bundler proctitle len-1 cmdline main = %q, want /app/bin/mysvc", got)
	}
	// Ruby 3.4.8 + Bundler 2.6.9 on the same OS instead leaves trailing
	// elements that are NOT blank: they are leaked environment-variable
	// strings from past the end of the process's original argv
	// reservation (an emulated-setproctitle/gopsutil-parsing artifact).
	// These must be ignored, not treated as a disqualifying signal.
	got = extractMainScript(RuntimeRuby, []string{
		"/app/bin/mysvc arg1 arg2",
		"__CF_USER_TEXT_ENCODING=0x0:0:0",
		"PATH=/usr/bin:/bin",
		"PWD=/app",
	}, cwd)
	if got != "/app/bin/mysvc" {
		t.Fatalf("bundler proctitle leaked-env-tail main = %q, want /app/bin/mysvc", got)
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
		// The argv port (9230) deliberately differs from the default 9229:
		// the old lookahead treated "9230" as the port and returned
		// 127.0.0.1:9230, so this case fails against the pre-fix behavior
		// instead of passing either way.
		{RuntimeNode, []string{"node", "--inspect", "9230", "a.js"}, "127.0.0.1:9229"},
		// Bun's bare --inspect defaults to 6499, not the Node/Deno 9229, and
		// the hostname is "localhost" (matching Bun's own banner; on macOS
		// it in fact binds only the IPv6 loopback [::1]:6499, not
		// 127.0.0.1), not a hardcoded IPv4 literal.
		{RuntimeBun, []string{"bun", "--inspect", "app.ts"}, "localhost:6499"},
		{RuntimeBun, []string{"bun", "--inspect-brk", "app.ts"}, "localhost:6499"},
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

// TestResolveDescendantOfRestrictsToPidSubtree pins down the E3.2 addition
// to ResolveOptions: DescendantOf must restrict candidate processes to a
// live pid's descendants instead of scanning every process on the host.
// This starts a real "sh -c 'sleep 30 & wait'" (the "& wait" forces sh to
// fork a genuine child instead of exec-optimizing into "sleep" with the
// SAME pid — the behavior many sh implementations use for a single simple
// command with no further shell work left, verified live on this project's
// own dev box; see tree_test.go for the same note against node), then
// resolves with DescendantOf=<sh pid> and no OTHER selector. With no
// runtime, codebase-root or main-script-suffix filter, matchesBinding
// accepts ANY process, so the single result must be the "sleep" child --
// proving the DescendantOf restriction, not some other selector, is what
// narrowed the match down from every live process to exactly one.
func TestResolveDescendantOfRestrictsToPidSubtree(t *testing.T) {
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not on PATH")
	}
	cmd := exec.Command(shPath, "-c", sleepPath+" 30 & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sh: %v", err)
	}
	t.Cleanup(func() {
		if killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
			t.Logf("kill process group %d: %v", cmd.Process.Pid, killErr)
		}
		_ = cmd.Wait()
	})

	root := int32(cmd.Process.Pid)
	ctx := context.Background()
	deadline := time.Now().Add(5 * time.Second)
	var binding Binding
	for {
		// Runtime must be set explicitly to RuntimeUnknown ("unknown"), the
		// documented "no runtime filter" sentinel: the zero value of the
		// Runtime field is the empty string, which matchesBinding treats as
		// a (never-matching) filter for a runtime literally named "", not
		// as "no filter". The CLI's own --runtime flag defaults to the
		// string "unknown" for exactly this reason.
		binding, err = Resolve(ctx, ResolveOptions{Runtime: RuntimeUnknown, DescendantOf: root})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Resolve(DescendantOf=%d): %v", root, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if binding.PID == root {
		t.Fatal("Resolve(DescendantOf) matched the sh wrapper itself, not its sleep child")
	}
	if !strings.Contains(binding.Name, "sleep") {
		t.Fatalf("Resolve(DescendantOf) matched pid %d name=%q, want the sleep child", binding.PID, binding.Name)
	}
}

// TestResolveDescendantOfCountsAsASelector pins down that DescendantOf alone
// (no runtime/codebase-root/main-script-suffix) now satisfies the "at least
// one process selector is required" guard, matching --descendant-of being a
// valid `monitor resolve` invocation on its own.
func TestResolveDescendantOfCountsAsASelector(t *testing.T) {
	_, err := Resolve(context.Background(), ResolveOptions{DescendantOf: 1})
	if err != nil && strings.Contains(err.Error(), "at least one process selector is required") {
		t.Fatalf("DescendantOf alone should count as a selector, got %v", err)
	}
}
