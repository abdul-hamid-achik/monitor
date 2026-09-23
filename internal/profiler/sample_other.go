//go:build !darwin

package profiler

import (
	"context"
	"fmt"
	"time"
)

// captureSample is a stub on non-darwin hosts: macOS `sample` doesn't exist
// there. capability.ProfileSample is Unsupported on every non-darwin GOOS
// (see internal/capability), so Capture already refuses ProfileSample
// before reaching this function on those hosts; this stub exists only so
// the package builds everywhere and direct callers get an honest error
// instead of a platform-specific compile failure.
func captureSample(_ context.Context, pid int32) (Profile, error) {
	return Profile{PID: pid, Type: ProfileSample, Method: "sample", Taken: time.Now()},
		fmt.Errorf("sample profiling requires macOS")
}
