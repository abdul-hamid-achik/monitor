package sdk

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestMaterializeIsContentAddressedAndPrivate(t *testing.T) {
	base := t.TempDir()
	var wg sync.WaitGroup
	results := make([]Paths, 4)
	errs := make([]error, 4)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = Materialize(base)
		}(i)
	}
	wg.Wait()
	for i := range results {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if results[i] != results[0] {
			t.Fatalf("concurrent launches disagree: %+v vs %+v", results[i], results[0])
		}
	}
	p := results[0]
	if filepath.Base(p.Root) != Version() {
		t.Fatalf("root %s is not named by content hash %s", p.Root, Version())
	}
	for _, f := range []string{p.NodeAuto, filepath.Join(p.Node, "index.cjs"), filepath.Join(p.Node, "package.json"),
		filepath.Join(p.PythonBootstrap, "sitecustomize.py"), filepath.Join(p.Python, "monitorcli", "__init__.py")} {
		info, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v, want 0600", f, info.Mode().Perm())
		}
	}
	// The bootstrap directory must hold nothing but sitecustomize.py: it
	// goes first on PYTHONPATH and must not shadow any other module.
	entries, err := os.ReadDir(p.PythonBootstrap)
	if err != nil || len(entries) != 1 || entries[0].Name() != "sitecustomize.py" {
		t.Fatalf("bootstrap dir holds %v (%v)", entries, err)
	}
	left, _ := filepath.Glob(filepath.Join(base, ".sdk-*"))
	if len(left) != 0 {
		t.Fatalf("temporary copies left behind: %v", left)
	}
	data, _ := os.ReadFile(p.NodeAuto)
	if !strings.Contains(string(data), "_auto()") {
		t.Fatal("auto.cjs does not start auto mode")
	}
}
