// Package temperature provides real SMC temperature readings on macOS by
// wrapping `sudo powermetrics -i 5000 --show-extra-power-info`. When sudo
// credentials are available, the package spawns a long-running streaming
// subprocess and parses each emission into a Reading. When the binary is
// missing, sudo is unavailable, or the subprocess fails for any reason,
// the package transparently falls back to an estimated Reading derived from
// CPU load. Callers always see a Reading; they never block on the
// subprocess.
//
// This implements the BACKLOG item "real temperature via powermetrics" and
// the VISION §5.1 graceful-degradation recommendation.
package temperature

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Kind identifies where a Reading's data came from. Named Kind (not
// Source) to avoid colliding with the Source struct in this package.
type Kind string

const (
	// KindEstimated means the Reading is a CPU-load heuristic. Always
	// available, no privileges required.
	KindEstimated Kind = "estimated"
	// KindPowermetrics means the Reading came from a real `sudo
	// powermetrics` subprocess (real SMC sensors on Apple Silicon).
	KindPowermetrics Kind = "powermetrics"
)

// Reading is the package's view of a single temperature sample.
type Reading struct {
	CPUPackage float64
	CPUCores   float64
	GPU        float64
	ANE        float64
	Battery    float64
	Ambient    float64
	FanRPM     int
	FanMode    string
	Source     Kind
	Available  bool
	Timestamp  time.Time
}

// Options tunes the Source behavior.
type Options struct {
	// Bin is the path to the powermetrics binary. Empty means
	// exec.LookPath("powermetrics").
	Bin string
	// Interval is the powermetrics sampling interval. Default 5s.
	Interval time.Duration
	// SUDO is the path to the sudo binary. Empty means
	// exec.LookPath("sudo").
	SUDO string
	// ExtraArgs are appended after the standard args. Intended for tests
	// that swap the binary for a fake; production callers leave this nil.
	ExtraArgs []string
	// Logf captures error/diagnostic messages during startup and
	// streaming. nil disables capture (production callers leave this nil).
	Logf func(format string, args ...any)
}

// Source produces temperature Readings. The first Reading returned is
// always available (estimated at worst); the source upgrades from
// "estimated" to "powermetrics" as soon as the streaming subprocess emits
// a parseable sample.
//
// Source is safe for concurrent use; Latest can be called from any
// goroutine.
type Source struct {
	opts Options

	mu      sync.RWMutex
	current Reading
	cancel  context.CancelFunc
	cmd     *exec.Cmd
}

// New constructs a Source and immediately starts the powermetrics
// subprocess if possible. If the subprocess cannot be started (binary
// missing, sudo unavailable, etc.) New returns a Source that returns
// estimated readings. The subprocess lifecycle is bound to the given
// context; when ctx is canceled the subprocess is killed.
func New(ctx context.Context, opts Options) *Source {
	if opts.Interval == 0 {
		opts.Interval = 5 * time.Second
	}
	s := &Source{
		opts:    opts,
		current: Estimate(0),
	}
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	// Try to start powermetrics. If start fails we just keep the
	// estimated fallback.
	if err := s.start(ctx); err != nil {
		if opts.Logf != nil {
			opts.Logf("temperature: powermetrics unavailable, falling back to estimate: %v", err)
		}
	}
	return s
}

// Latest returns the most recent Reading. Safe for concurrent use.
func (s *Source) Latest() Reading {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// Started reports whether the source is delivering real powermetrics data
// (not the estimated fallback). Launching the subprocess isn't enough —
// `sudo -n` can succeed then exit immediately with no usable output — so
// this keys off whether a powermetrics line has actually been parsed.
func (s *Source) Started() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current.Source == KindPowermetrics
}

// Close stops the powermetrics subprocess (if any) and releases resources.
func (s *Source) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		// SIGTERM lets powermetrics flush its summary; the kernel will
		// clean up the child if it's still alive after a short grace
		// period.
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	return nil
}

// start launches the powermetrics subprocess. It is best-effort: any
// failure is returned to the caller (which logs and moves on).
func (s *Source) start(ctx context.Context) error {
	bin := s.opts.Bin
	if bin == "" {
		looked, err := exec.LookPath("powermetrics")
		if err != nil {
			return fmt.Errorf("powermetrics not on PATH: %w", err)
		}
		bin = looked
	}
	sudo := s.opts.SUDO
	if sudo == "" {
		looked, err := exec.LookPath("sudo")
		if err != nil {
			return fmt.Errorf("sudo not on PATH: %w", err)
		}
		sudo = looked
	}

	intervalMs := int(s.opts.Interval / time.Millisecond)
	if intervalMs < 1000 {
		intervalMs = 5000
	}
	args := []string{
		"-n", "-1", // infinite samples
		"-i", strconv.Itoa(intervalMs),
		"--show-extra-power-info",
		"-b", "0", // unbuffered line output
	}
	args = append(args, s.opts.ExtraArgs...)

	cmd := exec.CommandContext(ctx, sudo, append([]string{"-n", bin}, args...)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start powermetrics: %w", err)
	}

	s.mu.Lock()
	s.cmd = cmd
	s.mu.Unlock()

	go s.consume(stdout)
	return nil
}

// consume reads powermetrics output line-by-line, parsing each emission
// into a Reading. When a parse yields at least one temperature value the
// Source upgrades from estimated to powermetrics.
//
// powermetrics emits one sample block per interval; within each block
// it prints multiple lines (CPU die, GPU die, ANE die, ...). Each
// parseLine call returns a fresh Reading with only the field it parsed
// set, so we must MERGE each new partial Reading into s.current so
// fields from earlier lines in the same block aren't wiped.
func (s *Source) consume(r io.Reader) {
	scanner := bufio.NewScanner(r)
	// powermetrics lines are short; 1 MiB is plenty.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	// acc accumulates ONLY fields actually parsed from powermetrics; it
	// starts at the zero Reading (NOT the Estimate seed) so a field the
	// stream never emits (e.g. Battery on Apple Silicon) stays 0 instead
	// of bleeding an estimated value into a record stamped Source=powermetrics.
	var acc Reading
	for scanner.Scan() {
		line := scanner.Text()
		partial, ok := parseLine(line)
		if !ok {
			continue
		}
		// Carry each newly parsed (non-zero) field into the accumulator so
		// later lines in a block don't wipe earlier ones, and values persist
		// across sample blocks.
		if partial.CPUPackage != 0 {
			acc.CPUPackage = partial.CPUPackage
		}
		if partial.CPUCores != 0 {
			acc.CPUCores = partial.CPUCores
		}
		if partial.GPU != 0 {
			acc.GPU = partial.GPU
		}
		if partial.ANE != 0 {
			acc.ANE = partial.ANE
		}
		if partial.Battery != 0 {
			acc.Battery = partial.Battery
		}
		if partial.Ambient != 0 {
			acc.Ambient = partial.Ambient
		}
		if partial.FanRPM != 0 {
			acc.FanRPM = partial.FanRPM
		}
		if partial.FanMode != "" {
			acc.FanMode = partial.FanMode
		}
		acc.Source = KindPowermetrics
		acc.Available = true
		acc.Timestamp = time.Now()
		s.mu.Lock()
		s.current = acc
		s.mu.Unlock()
	}
	if err := scanner.Err(); err != nil && s.opts.Logf != nil {
		s.opts.Logf("temperature: powermetrics stream ended: %v", err)
	}
	// Reap the child so it doesn't linger as a zombie and the stdout pipe
	// fds are released. Safe to call here: the scanner loop above has
	// drained stdout, and Close()/ctx-cancel only signals — it never Waits.
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd != nil {
		_ = cmd.Wait()
	}
}

// -- Line parser ----------------------------------------------------------

// tempLineRe matches "Field name: 12.34 C" lines from powermetrics. The
// name is intentionally permissive (letters, digits, spaces, dashes,
// parentheses) so we match both "CPU die temperature" and "CPU Package
// temperature" and "GPU die temperature" variants across macOS versions.
var tempLineRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9 \-+()]*?)\s*temperature:\s+([0-9]+\.?[0-9]*)\s*C\s*$`)

// fanLineRe matches "Fan: 1234 rpm" lines (rare on Apple Silicon but
// emitted on Intel Macs).
var fanLineRe = regexp.MustCompile(`^Fan:\s*([0-9]+)\s*rpm\s*$`)

// fanModeRe matches "Fan mode: auto" / "Fan mode: forced" lines.
var fanModeRe = regexp.MustCompile(`^Fan mode:\s*(auto|forced|manual)\s*$`)

// parseLine extracts a partial Reading from one powermetrics line. ok is
// false when the line carries no temperature/fan data.
func parseLine(line string) (Reading, bool) {
	var r Reading
	if m := tempLineRe.FindStringSubmatch(line); m != nil {
		field, val := m[1], m[2]
		t, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return r, false
		}
		switch {
		case strings.HasPrefix(field, "CPU Package"):
			r.CPUPackage = t
		case strings.HasPrefix(field, "CPU die"), strings.HasPrefix(field, "CPU core"):
			r.CPUCores = t
		case strings.HasPrefix(field, "GPU"):
			r.GPU = t
		case strings.HasPrefix(field, "ANE"):
			r.ANE = t
		case strings.HasPrefix(field, "Battery"):
			r.Battery = t
		case strings.HasPrefix(field, "Ambient"), strings.HasPrefix(field, "Inlet"):
			r.Ambient = t
		default:
			return r, false
		}
		return r, true
	}
	if m := fanLineRe.FindStringSubmatch(line); m != nil {
		rpm, err := strconv.Atoi(m[1])
		if err != nil {
			return r, false
		}
		r.FanRPM = rpm
		return r, true
	}
	if m := fanModeRe.FindStringSubmatch(line); m != nil {
		r.FanMode = m[1]
		return r, true
	}
	return r, false
}

// -- Estimation fallback -------------------------------------------------

// Estimate produces a synthetic Reading derived from CPU load. Exposed
// as a package-level function so callers (and tests) can use the same
// heuristic that powers the fallback path. cpuLoad is 0..100.
func Estimate(cpuLoad float64) Reading {
	return estimate(cpuLoad)
}

// estimate is the internal estimation heuristic. It mirrors the
// pre-existing `collectTemperature` math in internal/collector so the
// fallback values match what the legacy code path produced.
func estimate(cpuLoad float64) Reading {
	base := 35.0
	load := cpuLoad * 0.5
	return Reading{
		CPUPackage: base + load,
		CPUCores:   base + load + 2,
		GPU:        base + cpuLoad*0.3,
		ANE:        base + cpuLoad*0.2,
		Battery:    38.0,
		Available:  true,
		Source:     KindEstimated,
		Timestamp:  time.Now(),
	}
}
