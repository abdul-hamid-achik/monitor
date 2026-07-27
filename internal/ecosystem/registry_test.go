package ecosystem

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeJSON(t *testing.T) {
	type rec struct {
		Name string `json:"name"`
	}
	got, err := decodeJSON[rec]([]byte(`{"name":"x"}`), "cmd")
	if err != nil || got.Name != "x" {
		t.Fatalf("decodeJSON valid = (%+v, %v)", got, err)
	}
	gotS, err := decodeJSON[[]rec]([]byte(`[{"name":"a"},{"name":"b"}]`), "cmd")
	if err != nil || len(gotS) != 2 {
		t.Fatalf("decodeJSON slice = (%+v, %v)", gotS, err)
	}

	// Malformed JSON must surface a *Wrap carrying the command and raw output,
	// and Unwrap must chain to the underlying json error.
	_, err = decodeJSON[rec]([]byte(`{bad`), "fcheap save")
	if err == nil {
		t.Fatal("expected an error on malformed JSON")
	}
	var w *Wrap
	if !errors.As(err, &w) {
		t.Fatalf("error should be *Wrap; got %T", err)
	}
	if w.Cmd != "fcheap save" || w.Output != "{bad" {
		t.Errorf("Wrap = {Cmd:%q Output:%q}, want {fcheap save, {bad}", w.Cmd, w.Output)
	}
	if w.Unwrap() == nil {
		t.Error("Wrap.Unwrap() should return the underlying json error")
	}
}

// TestSymbolAtDecode locks the SymbolAt struct tags against the real
// `codemap symbol-at --json` output shape.
func TestSymbolAtDecode(t *testing.T) {
	js := `{"file":"a.go","line":45,"symbol":"Alert","fqn":"collector.Alert","kind":"type","start_line":43,"end_line":49,"resolution":"enclosing"}`
	got, err := decodeJSON[SymbolAt]([]byte(js), "codemap symbol-at")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.FQN != "collector.Alert" || got.Kind != "type" || got.Resolution != "enclosing" || got.StartLine != 43 || got.EndLine != 49 {
		t.Errorf("decoded = %+v", got)
	}
	// The "none" shape (unindexed/unresolved) must decode cleanly too.
	none, err := decodeJSON[SymbolAt]([]byte(`{"file":"a.go","line":1,"resolution":"none"}`), "codemap symbol-at")
	if err != nil || none.Resolution != "none" || none.FQN != "" {
		t.Errorf("none decode = %+v, err %v", none, err)
	}
}

// TestImpactDecode locks the Impact struct tags against the real
// `codemap impact --json` shape (only the array counts matter to callers).
func TestImpactDecode(t *testing.T) {
	js := `{"symbol":"Collect","found":true,"direct_callers":[1,2,3],"blast_radius":[1,2,3,4,5],"tests":[1],"untested":false}`
	imp, err := decodeJSON[Impact]([]byte(js), "codemap impact")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !imp.Found || len(imp.DirectCallers) != 3 || len(imp.BlastRadius) != 5 || len(imp.Tests) != 1 || imp.Untested {
		t.Errorf("decoded = %+v", imp)
	}
}

func TestProbeRunsEvenWithMissingTools(t *testing.T) {
	// Probe must never panic and must return a Status struct.
	st := Probe(context.Background())
	// At least one of these should be populated; we don't assert which.
	any := st.Codemap.Available || st.Fcheap.Available || st.Veclite.Available || st.Tmux.Available
	if !any {
		t.Log("Probe found no tools on PATH (acceptable in minimal envs)")
	}
}

func TestFirstLine(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"only", "only"},
		{"first\nsecond", "first"},
		{"first\rsecond", "first"},
		{"", ""},
	}
	for _, tt := range tests {
		got := firstLine(tt.in)
		if got != tt.want {
			t.Errorf("firstLine(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestRunMissingBinary(t *testing.T) {
	_, err := run(context.Background(), "definitely-not-on-path-12345")
	if err == nil {
		t.Error("expected error for missing binary")
	}
}

func TestFcheapWireContracts(t *testing.T) {
	saveJSON := []byte(`{"schema_version":"1.0","id":"stash-1","name":"incident","source_path":"/tmp/bundle","file_count":2,"total_size":42,"content_hash":"abc","files":[{"path":"manifest.json","size":20}],"status":"saved"}`)
	got, err := decodeJSON[StashSaveResult](saveJSON, "fcheap save")
	if err != nil {
		t.Fatal(err)
	}
	if got.SourcePath != "/tmp/bundle" || got.TotalSize != 42 || got.FileCount != 2 || len(got.Files) != 1 {
		t.Fatalf("save payload decoded incorrectly: %+v", got)
	}

	listJSON := []byte(`[{"id":"stash-1","name":"incident","file_count":2,"total_size":42,"created_at":"2026-01-01T00:00:00Z"}]`)
	list, err := decodeJSON[[]StashListEntry](listJSON, "fcheap list")
	if err != nil || len(list) != 1 || list[0].TotalSize != 42 || list[0].FileCount != 2 {
		t.Fatalf("list payload decoded incorrectly: %+v, %v", list, err)
	}
}

func TestArtifactRefV1Validate(t *testing.T) {
	valid := ArtifactRefV1{
		Schema: artifactRefSchema, Version: 1, Provider: artifactRefProvider,
		URI: "fcheap://stash/stash-1", ArtifactID: "stash-1", Kind: "monitor.incident",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid ref rejected: %v", err)
	}
	for name, mutate := range map[string]func(*ArtifactRefV1){
		"schema":   func(r *ArtifactRefV1) { r.Schema = "wrong" },
		"provider": func(r *ArtifactRefV1) { r.Provider = "link" },
		"uri":      func(r *ArtifactRefV1) { r.URI = "fcheap://stash/other" },
		"id":       func(r *ArtifactRefV1) { r.ArtifactID = "../escape" },
	} {
		t.Run(name, func(t *testing.T) {
			ref := valid
			mutate(&ref)
			if err := ref.Validate(); err == nil {
				t.Fatalf("invalid ref accepted: %+v", ref)
			}
		})
	}
}

func TestStashSaveUsesRealWireContractAndNoCompress(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$FCHEAP_ARGS_FILE"
printf '%s' '{"schema_version":"1.0","id":"stash-1","name":"incident","source_path":"/tmp/source","file_count":2,"total_size":42,"content_hash":"abc","files":[{"path":"manifest.json","size":20}],"status":"saved"}'
`
	if err := os.WriteFile(filepath.Join(dir, "fcheap"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FCHEAP_ARGS_FILE", argsPath)
	got, err := StashSave(context.Background(), "/tmp/source", "incident", []string{"monitor-incident"}, "7d")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "stash-1" || got.TotalSize != 42 || got.SizeBytes != 42 || got.SourcePath != "/tmp/source" || got.Path != "/tmp/source" {
		t.Fatalf("decoded save = %+v", got)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "--no-compress\n") || !strings.Contains(string(args), "--tag\nmonitor-incident\n") {
		t.Fatalf("fcheap args = %s", args)
	}
}

func TestCodemapAdaptersBindProjectAndPreserveMachineArguments(t *testing.T) {
	binDir := t.TempDir()
	project := t.TempDir()
	receipt := filepath.Join(binDir, "codemap-args")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$CODEMAP_ARGS_FILE"
case " $* " in
  *" symbol-at "*) printf '%s' '{"file":"internal/a.go","line":7,"fqn":"pkg.Run","kind":"function","resolution":"enclosing"}' ;;
  *" impact "*) printf '%s' '{"symbol":"pkg.Run","found":true,"direct_callers":[],"blast_radius":[],"tests":[],"untested":true}' ;;
  *) exit 9 ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "codemap"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CODEMAP_ARGS_FILE", receipt)

	symbol, err := CodemapSymbolAtPath(context.Background(), "internal/a.go", 7, CodemapOpts{Path: project})
	if err != nil || symbol.FQN != "pkg.Run" {
		t.Fatalf("symbol-at = (%+v, %v)", symbol, err)
	}
	assertArgLines(t, receipt, []string{"-C", project, "symbol-at", "internal/a.go:7", "--json"})

	impact, err := CodemapImpactAtPath(context.Background(), "internal/a.go", 7, 2, CodemapOpts{Path: project})
	if err != nil || !impact.Found || !impact.Untested {
		t.Fatalf("impact = (%+v, %v)", impact, err)
	}
	assertArgLines(t, receipt, []string{"-C", project, "impact", "--at", "internal/a.go:7", "--json", "--depth", "2"})
}

func TestVecgrepEnvelopeContractAndWarnings(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' 'Warning: hybrid fallback to keyword' >&2
printf '%s' '{"schema_version":1,"index":{"indexed":true,"fresh":false,"chunks":12},"hits":[]}'
`
	if err := os.WriteFile(filepath.Join(dir, "vecgrep"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	env, err := VecgrepSearchWithReadiness(context.Background(), "auth", VecgrepSearchOpts{Dir: dir, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !env.Index.Indexed || env.Index.Fresh || env.Index.Chunks != 12 || !strings.Contains(env.Warning, "fallback") || env.Hits == nil {
		t.Fatalf("envelope = %+v", env)
	}
	for _, invalid := range [][]byte{
		[]byte(`{"schema_version":1,"hits":[]}`),
		[]byte(`{"schema_version":1,"index":{"indexed":true,"fresh":true},"hits":[]}`),
	} {
		if err := validateVecgrepEnvelopeShape(invalid); err == nil {
			t.Fatalf("invalid envelope accepted: %s", invalid)
		}
	}
}

func TestVecgrepSearchUsesEnvelopeFormatProjectCWDAndBounds(t *testing.T) {
	binDir := t.TempDir()
	project := t.TempDir()
	receipt := filepath.Join(binDir, "vecgrep-receipt")
	script := `#!/bin/sh
{
  pwd
  printf '%s\n' "$@"
} > "$VECGREP_RECEIPT"
printf '%s' '{"schema_version":1,"index":{"indexed":true,"fresh":true,"chunks":7},"hits":[{"relative_path":"internal/a.go","start_line":7,"end_line":9,"score":0.8}]}'
`
	if err := os.WriteFile(filepath.Join(binDir, "vecgrep"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VECGREP_RECEIPT", receipt)

	hits, err := VecgrepSearch(context.Background(), "request timeout", VecgrepSearchOpts{
		Dir: project, Limit: 5, Mode: "hybrid", MinScore: 0.4, Lang: "go", Symbol: "pkg.Run",
	})
	if err != nil || len(hits) != 1 || hits[0].RelativePath != "internal/a.go" {
		t.Fatalf("search = (%+v, %v)", hits, err)
	}
	assertArgLines(t, receipt, []string{
		project, "search", "request timeout", "-f", "json-envelope", "-n", "5",
		"-m", "hybrid", "--min-score", "0.4", "-l", "go", "--symbol", "pkg.Run",
	})
}

func TestArtifactRefInvocationAndPortableResult(t *testing.T) {
	binDir := t.TempDir()
	receipt := filepath.Join(binDir, "fcheap-args")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$FCHEAP_ARGS_FILE"
printf '%s' '{"$schema":"urn:filecheap.dev:artifact-ref:v1","version":1,"provider":"fcheap-local","uri":"fcheap://stash/stash-1","artifact_id":"stash-1","kind":"monitor.incident","producer":{"tool":"monitor","version":"v1.15.0","native_schema":"urn:monitor.dev:incident:v1","native_id":"abcdef","entrypoint":"manifest.json"}}'
`
	if err := os.WriteFile(filepath.Join(binDir, "fcheap"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FCHEAP_ARGS_FILE", receipt)

	ref, err := ArtifactRef(context.Background(), "stash-1", ArtifactRefOpts{
		Kind: "monitor.incident", ProducerTool: "monitor", ProducerVersion: "v1.15.0",
		NativeSchema: "urn:monitor.dev:incident:v1", NativeID: "abcdef", Entrypoint: "manifest.json",
	})
	if err != nil || ref.URI != "fcheap://stash/stash-1" || ref.Producer == nil || ref.Producer.Tool != "monitor" {
		t.Fatalf("artifact ref = (%+v, %v)", ref, err)
	}
	assertArgLines(t, receipt, []string{
		"artifact-ref", "stash-1", "--json", "--kind", "monitor.incident",
		"--producer-tool", "monitor", "--producer-version", "v1.15.0",
		"--native-schema", "urn:monitor.dev:incident:v1", "--native-id", "abcdef",
		"--entrypoint", "manifest.json",
	})
}

func TestArtifactRefStrictDecodeAndProducerValidation(t *testing.T) {
	valid := []byte(`{"$schema":"urn:filecheap.dev:artifact-ref:v1","version":1,"provider":"fcheap-local","uri":"fcheap://stash/stash-1","artifact_id":"stash-1","kind":"monitor.incident","producer":{"tool":"monitor","native_schema":"urn:monitor.dev:incident:v1","entrypoint":"manifest.json"}}`)
	ref, err := decodeArtifactRef(valid)
	if err != nil || ref.Validate() != nil {
		t.Fatalf("valid ref = %+v, decode=%v validate=%v", ref, err, ref.Validate())
	}
	for _, invalid := range [][]byte{
		append(append([]byte(nil), valid...), []byte(` {}`)...),
		[]byte(`{"$schema":"urn:filecheap.dev:artifact-ref:v1","version":1,"provider":"fcheap-local","uri":"fcheap://stash/stash-1","artifact_id":"stash-1","kind":"monitor.incident","token":"secret"}`),
	} {
		if _, err := decodeArtifactRef(invalid); err == nil {
			t.Fatalf("strict decoder accepted %s", invalid)
		}
	}
	ref.Kind = "Bad Kind"
	if err := ref.Validate(); err == nil {
		t.Fatal("invalid kind accepted")
	}
	ref.Kind = "monitor.incident"
	ref.Producer.Entrypoint = "../secret"
	if err := ref.Validate(); err == nil {
		t.Fatal("unsafe entrypoint accepted")
	}
}

func TestRunGlyphrunInjectsMonitorEnvironment(t *testing.T) {
	dir := t.TempDir()
	receipt := filepath.Join(dir, "env")
	script := `#!/bin/sh
if [ "$MONITOR" = "1" ] && [ -n "$MONITOR_RUN_DIR" ] && [ -d "$MONITOR_RUN_DIR" ]; then
  printf '%s' "$MONITOR|$MONITOR_RUN_DIR" > "$GLYPH_ENV_RECEIPT"
  printf '%s' '{}'
  exit 0
fi
exit 9
`
	if err := os.WriteFile(filepath.Join(dir, "glyph"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GLYPH_ENV_RECEIPT", receipt)
	t.Setenv("MONITOR_RUN_DIR", "")
	if _, err := RunGlyphrun(context.Background(), "spec.yml"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(receipt)
	if err != nil || !strings.HasPrefix(string(got), "1|") {
		t.Fatalf("receipt = %q, %v", got, err)
	}
}

func assertArgLines(t *testing.T, path string, want []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(got) != len(want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q; all=%q", i, got[i], want[i], got)
		}
	}
}
