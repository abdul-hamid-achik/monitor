// Package sdk embeds monitor's own SDKs so `monitor run --probes` can load
// them into a launched process with nothing installed, and so `monitor sdk
// path` can hand them to npm or pip before they are published.
//
// sdk/node (Node, Bun) and sdk/python are embedded. sdk/go is a separate
// Go module and is not: a Go program cannot be instrumented from outside,
// so it imports github.com/abdul-hamid-achik/monitor/sdk/go itself.
package sdk

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"

	"github.com/abdul-hamid-achik/monitor/internal/statedir"
)

//go:embed node/index.cjs node/index.mjs node/index.d.ts node/auto.cjs node/package.json node/README.md
//go:embed python/monitorcli/__init__.py python/bootstrap/sitecustomize.py python/pyproject.toml python/README.md
var files embed.FS

// Paths locates one materialized copy of the SDKs.
type Paths struct {
	// Root is the content-addressed directory holding everything below.
	Root string `json:"root"`
	// Node is the Node SDK's package directory (npm install <Node>).
	Node string `json:"node"`
	// NodeAuto is the file NODE_OPTIONS=--require / BUN_OPTIONS=--preload
	// load in auto mode.
	NodeAuto string `json:"node_auto"`
	// Python is the Python SDK's project directory (pip install <Python>).
	Python string `json:"python"`
	// PythonBootstrap is the directory auto mode puts first on PYTHONPATH:
	// it holds only the bootstrap sitecustomize.py.
	PythonBootstrap string `json:"python_bootstrap"`
}

// completeMarker is written last, so a directory without it is a copy
// that was interrupted and gets rebuilt.
const completeMarker = ".complete"

// Materialize writes the embedded SDKs under base/<content hash> once and
// returns their paths; later calls with the same build reuse that copy.
// base "" means $XDG_STATE_HOME/monitor/sdk. Directories are 0700 and
// files 0600. The copy is built in a temporary directory and renamed into
// place, so concurrent launches never see half a copy.
func Materialize(base string) (Paths, error) {
	if base == "" {
		dir, err := statedir.Path("sdk")
		if err != nil {
			return Paths{}, err
		}
		base = dir
	}
	if err := statedir.Ensure(base); err != nil {
		return Paths{}, err
	}
	names, sum, err := inventory()
	if err != nil {
		return Paths{}, err
	}
	root := filepath.Join(base, sum)
	paths := pathsUnder(root)
	if _, err := os.Stat(filepath.Join(root, completeMarker)); err == nil {
		return paths, nil
	}

	tmp, err := os.MkdirTemp(base, ".sdk-*")
	if err != nil {
		return Paths{}, fmt.Errorf("materialize sdk: %w", err)
	}
	defer os.RemoveAll(tmp)
	for _, name := range names {
		data, err := files.ReadFile(name)
		if err != nil {
			return Paths{}, err
		}
		dst := filepath.Join(tmp, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return Paths{}, fmt.Errorf("materialize sdk: %w", err)
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			return Paths{}, fmt.Errorf("materialize sdk: %w", err)
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, completeMarker), nil, 0o600); err != nil {
		return Paths{}, fmt.Errorf("materialize sdk: %w", err)
	}
	if err := os.Rename(tmp, root); err != nil {
		// Another launch finished the same copy first: use it.
		if _, serr := os.Stat(filepath.Join(root, completeMarker)); serr == nil {
			return paths, nil
		}
		// A copy without its marker was interrupted: replace it.
		if rerr := os.RemoveAll(root); rerr != nil {
			return Paths{}, fmt.Errorf("materialize sdk: %w", errors.Join(err, rerr))
		}
		if err := os.Rename(tmp, root); err != nil {
			return Paths{}, fmt.Errorf("materialize sdk: %w", err)
		}
	}
	return paths, nil
}

func pathsUnder(root string) Paths {
	return Paths{
		Root:            root,
		Node:            filepath.Join(root, "node"),
		NodeAuto:        filepath.Join(root, "node", "auto.cjs"),
		Python:          filepath.Join(root, "python"),
		PythonBootstrap: filepath.Join(root, "python", "bootstrap"),
	}
}

// inventory lists every embedded file and hashes names and contents, so
// any change to an SDK lands in a new directory.
func inventory() ([]string, string, error) {
	var names []string
	err := fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			names = append(names, p)
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		data, err := files.ReadFile(name)
		if err != nil {
			return nil, "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", path.Clean(name), len(data))
		h.Write(data)
	}
	return names, hex.EncodeToString(h.Sum(nil))[:16], nil
}

// Version reports the embedded SDKs' content hash, the directory name
// Materialize uses.
func Version() string {
	_, sum, err := inventory()
	if err != nil {
		return ""
	}
	return sum
}
