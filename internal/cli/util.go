package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/temperature"
)

var ndjsonWriteMu sync.Mutex

// parsePID parses a positive PID from s, rejecting trailing garbage and
// non-positive values. Unlike fmt.Sscanf("%d"), strconv.ParseInt fails on
// "123abc" instead of silently returning 123, so a typo'd argument can't
// quietly target the wrong process.
func parsePID(s string) (int32, error) {
	v, err := strconv.ParseInt(s, 10, 32)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("invalid pid %q", s)
	}
	return int32(v), nil
}

// disableTemperatureSource, when true, skips wiring the powermetrics
// subprocess and falls back to the estimation heuristic. Exposed via a
// persistent CLI flag (--no-temperature-source) so users on systems
// where sudo can't be obtained can opt out without recompiling. The flag
// itself is registered in Root() (root.go) so it appears under every
// subcommand.
var disableTemperatureSource bool

// JSONOutput returns true if the user requested JSON output via --json.
func JSONOutput(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("json")
	return v
}

// WriteJSON writes v as indented JSON to stdout.
func WriteJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// WriteNDJSON writes one JSON object per Write; flushes after each.
func WriteNDJSON(v any) error {
	ndjsonWriteMu.Lock()
	defer ndjsonWriteMu.Unlock()
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "")
	return enc.Encode(v)
}

// NewCollector creates a collector with the given interval and, unless
// --no-temperature-source is set, wires a powermetrics-backed temperature
// hook so temperature readings come from real SMC sensors when sudo
// credentials are available. Falls back to the legacy CPU-load
// estimation when powermetrics can't be started.
func NewCollector(interval time.Duration) *collector.Collector {
	c := collector.New(collector.Options{Interval: interval, HistorySize: 60})
	if disableTemperatureSource {
		return c
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	ts := temperature.New(ctx, temperature.Options{
		Interval: 5 * time.Second,
		Logf: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		},
	})
	c.WithTemperatureHook(func() (float64, float64, float64, float64, float64, float64, int, string, string, bool) {
		r := ts.Latest()
		return r.CPUPackage, r.CPUCores, r.GPU, r.ANE, r.Battery, r.Ambient, r.FanRPM, r.FanMode, string(r.Source), r.Available
	})
	// Each CLI invocation is short-lived; the OS reaps the powermetrics
	// subprocess on exit. Long-running commands (watch, v2) keep the
	// collector alive for the program lifetime.
	_ = cancel
	return c
}

// Context returns a context cancelled on SIGINT/SIGTERM.
func Context() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
