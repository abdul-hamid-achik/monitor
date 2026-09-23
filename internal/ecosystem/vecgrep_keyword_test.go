package ecosystem

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeywordSearchForcesKeywordModeAndReturnsHits(t *testing.T) {
	binDir := t.TempDir()
	project := t.TempDir()
	receipt := filepath.Join(binDir, "vecgrep-receipt")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$VECGREP_RECEIPT"
printf '%s' '{"schema_version":1,"index":{"indexed":true,"fresh":true,"chunks":3},"hits":[{"relative_path":"src/users.ts","start_line":40,"end_line":44,"content":"function loadUser(id) {\n  return db.users.find(id).name;\n}\n","score":1.0}]}'
`
	if err := os.WriteFile(filepath.Join(binDir, "vecgrep"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VECGREP_RECEIPT", receipt)

	hits, err := KeywordSearch(context.Background(), project, "loadUser malformed", 3)
	if err != nil {
		t.Fatalf("KeywordSearch error: %v", err)
	}
	if len(hits) != 1 || hits[0].RelativePath != "src/users.ts" || hits[0].StartLine != 40 {
		t.Fatalf("hits = %+v", hits)
	}
	assertArgLines(t, receipt, []string{
		"search", "loadUser malformed", "-f", "json-envelope", "-n", "3", "-m", "keyword",
	})
}

func TestKeywordSearchNeverUsesSemanticOrHybridMode(t *testing.T) {
	binDir := t.TempDir()
	receipt := filepath.Join(binDir, "vecgrep-receipt")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$VECGREP_RECEIPT"
printf '%s' '{"schema_version":1,"index":{"indexed":true,"fresh":true,"chunks":0},"hits":[]}'
`
	if err := os.WriteFile(filepath.Join(binDir, "vecgrep"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VECGREP_RECEIPT", receipt)

	if _, err := KeywordSearch(context.Background(), t.TempDir(), "connection refused", 0); err != nil {
		t.Fatalf("KeywordSearch error: %v", err)
	}
	args, err := os.ReadFile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	got := string(args)
	if !strings.Contains(got, "-m\nkeyword") {
		t.Fatalf("expected -m keyword in receipt, got %q", got)
	}
	if strings.Contains(got, "semantic") || strings.Contains(got, "hybrid") {
		t.Fatalf("message-search must never request semantic/hybrid mode: %q", got)
	}
}
