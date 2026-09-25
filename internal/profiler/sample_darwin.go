//go:build darwin

package profiler

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// captureSample runs the real macOS `sample <pid> <secs> -mayDie` and
// parses its call-graph output with parseSampleTree. This is the only file
// that execs `sample`; the pure tree parser lives in sample_parse.go (no
// build tag) so it's exercised on every OS CI runs on.
//
// duration is the caller's requested window (LUX-15: `monitor hot <pid>
// --duration` used to be silently ignored here — `sample <pid> 1` ran
// regardless of what the banner promised). `sample`'s own seconds argument
// is an integer, so the request is clamped by SampleSeconds to [1, 120];
// the banner prints the same clamped value via SampleSeconds, so what a
// person reads is what actually ran.
func captureSample(ctx context.Context, pid int32, duration time.Duration) (Profile, error) {
	p := Profile{PID: pid, Type: ProfileSample, Method: "sample", Taken: time.Now()}
	secs := SampleSeconds(duration)
	out, err := exec.CommandContext(ctx, "sample", fmt.Sprintf("%d", pid), fmt.Sprintf("%d", secs), "-mayDie").CombinedOutput()
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
