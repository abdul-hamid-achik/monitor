// Package statedir resolves monitor's private state directory,
// $XDG_STATE_HOME/monitor (default ~/.local/state/monitor): the launch
// registry, --profile shims and profiles, SDK event inboxes and the
// materialized SDKs all live under it.
package statedir

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Root returns $XDG_STATE_HOME/monitor without creating it. A relative
// XDG_STATE_HOME is an error, as the XDG spec requires.
func Root() (string, error) {
	root := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		root = filepath.Join(home, ".local", "state")
	} else if !filepath.IsAbs(root) {
		return "", fmt.Errorf("XDG_STATE_HOME must be an absolute path: %q", root)
	}
	return filepath.Join(root, "monitor"), nil
}

// Path joins elems under Root without creating anything.
func Path(elems ...string) (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{root}, elems...)...), nil
}

// Ensure creates dir (and its parents) and leaves it at mode 0700, the
// only mode monitor's state directories ever have.
func Ensure(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure %s: %w", dir, err)
	}
	return nil
}
