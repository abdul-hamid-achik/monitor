//go:build darwin

package profiler

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// captureSample runs the real macOS `sample <pid> 1 -mayDie` and parses its
// call-graph output with parseSampleTree. This is the only file that execs
// `sample`; the pure tree parser lives in sample_parse.go (no build tag) so
// it's exercised on every OS CI runs on.
func captureSample(ctx context.Context, pid int32) (Profile, error) {
	p := Profile{PID: pid, Type: ProfileSample, Method: "sample", Taken: time.Now()}
	out, err := exec.CommandContext(ctx, "sample", fmt.Sprintf("%d", pid), "1", "-mayDie").CombinedOutput()
	if err != nil {
		return p, fmt.Errorf("sample: %w", err)
	}
	p.Text = string(out)
	syms, stats := parseSampleTree(string(out))
	p.Symbols = syms
	if stats.Samples > 0 {
		p.Stats = &stats
	}
	return p, nil
}
