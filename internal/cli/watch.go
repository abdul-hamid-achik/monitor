package cli

import (
	"context"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/analyzer"
	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/incidents"
)

// Event is the NDJSON event type emitted by `monitor watch --json`.
//
// Schema fields are stable; do not reorder without a migration note.
type Event struct {
	Type      string                 `json:"type"`
	Timestamp time.Time              `json:"timestamp"`
	CPU       *collector.CPUInfo     `json:"cpu,omitempty"`
	Memory    *collector.MemoryInfo  `json:"memory,omitempty"`
	Network   *collector.NetworkInfo `json:"network,omitempty"`
	Hostname  string                 `json:"hostname,omitempty"`
	Alert     *collector.Alert       `json:"alert,omitempty"`
	Stash     *incidents.CaptureResult `json:"stash,omitempty"`
	StashErr  string                 `json:"stash_error,omitempty"`
}

func newWatchCmd() *cobra.Command {
	var (
		interval time.Duration
		once     bool
		stash    bool
		stashTTL string
	)
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Stream NDJSON events of system metrics",
		Long: `Stream metrics as NDJSON to stdout.

Each line is a self-describing JSON object with a "type" discriminator
("tick", "alert", "process", "ecosystem"). Pipe into jq or any other tool:

  monitor watch --json | jq -c 'select(.type=="tick")'

With --stash, every alert fires the analyzer's OnAlert hook which captures
the incident via internal/incidents (fcheap stash, content-addressed).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := NewCollector(interval)
			ctx, cancel := Context()
			defer cancel()

			// Build the analyzer and wire the OnAlert hook.
			engine := analyzer.NewEngine()
			engine.AddRule(&analyzer.CPUSpikeRule{Factor: 3.0})
			engine.AddRule(&analyzer.RSSGrowthRule{})

			var stashFn func(ev collector.Event, a collector.Alert)
			if stash {
				// stashOnAlert runs in a goroutine so the watch loop
				// never blocks on fcheap I/O. The Capture call itself
				// is synchronous; if fcheap stalls the user sees the
				// warning in the JSON `stash_error` field rather than
				// a stalled stdout stream.
				stashFn = func(ev collector.Event, a collector.Alert) {
					go func(ev collector.Event, a collector.Alert) {
						ctx2, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						res, err := incidents.Capture(ctx2, incidents.CaptureRequest{
							Snapshot: eventToSystemInfo(ev),
							Alert: incidents.AlertDetail{
								Severity: a.Severity,
								Rule:     a.Rule,
								PID:      a.PID,
								Process:  a.Process,
								Detail:   a.Detail,
							},
							Trigger: "alert",
							TTL:     stashTTL,
						})
						// Write the stash outcome as a separate NDJSON line so
						// downstream consumers see when an incident landed.
						ev2 := Event{
							Type:      "stash",
							Timestamp: time.Now(),
							Stash:     &res,
						}
						if err != nil {
							ev2.StashErr = err.Error()
						}
						_ = WriteNDJSON(ev2)
					}(ev, a)
				}
			}
			engine.SetOnAlert(stashFn)

			if once {
				info := c.Collect(ctx)
				ev := collector.Event{
					Timestamp: info.LastUpdate,
					Hostname:  info.Hostname,
					CPU:       info.CPU,
					Memory:    info.Memory,
					Network:   info.Network,
				}
				// Run analyzer so once --json + --stash still captures.
				for _, a := range engine.Observe(ev) {
					alertEv := Event{
						Type:      "alert",
						Timestamp: ev.Timestamp,
						Alert:     &a,
					}
					if err := WriteNDJSON(alertEv); err != nil {
						return err
					}
				}
				return WriteNDJSON(Event{
					Type:      "tick",
					Timestamp: info.LastUpdate,
					CPU:       &info.CPU,
					Memory:    &info.Memory,
					Network:   &info.Network,
					Hostname:  info.Hostname,
				})
			}
			return watchLoop(ctx, c, engine, interval)
		},
	}
	cmd.Flags().DurationVarP(&interval, "interval", "i", time.Second, "tick interval")
	cmd.Flags().Bool("json", false, "emit NDJSON to stdout")
	cmd.Flags().BoolVar(&once, "once", false, "emit one event and exit")
	cmd.Flags().BoolVar(&stash, "stash", false,
		"on every alert, capture the incident to fcheap via internal/incidents")
	cmd.Flags().StringVar(&stashTTL, "stash-ttl", "7d",
		"TTL for incident stashes (passed to fcheap --ttl)")
	return cmd
}

func watchLoop(ctx context.Context, c *collector.Collector, engine *analyzer.Engine, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			info := c.Collect(ctx)
			ev := collector.Event{
				Timestamp: info.LastUpdate,
				Hostname:  info.Hostname,
				CPU:       info.CPU,
				Memory:    info.Memory,
				Network:   info.Network,
			}
			if err := WriteNDJSON(Event{
				Type:      "tick",
				Timestamp: info.LastUpdate,
				CPU:       &info.CPU,
				Memory:    &info.Memory,
				Network:   &info.Network,
				Hostname:  info.Hostname,
			}); err != nil {
				return err
			}
			for _, a := range engine.Observe(ev) {
				alertEv := Event{
					Type:      "alert",
					Timestamp: info.LastUpdate,
					Alert:     &a,
				}
				if err := WriteNDJSON(alertEv); err != nil {
					return err
				}
			}
		}
	}
}

// init ensures child processes observe MONITOR=1 when invoked under
// monitor run / monitor watch, mirroring glyphrun's GLYPHRUN pattern.
func init() {
	if os.Getenv("MONITOR_RUN_DIR") == "" {
		_ = os.Setenv("MONITOR", "1")
	}
}

// eventToSystemInfo flattens a collector.Event into the SystemInfo shape
// the incident stash needs. Process list is empty (stashing the snapshot
// at the moment an alert fired is more useful than the full process
// tree; PID-specific profiles are bundled separately).
func eventToSystemInfo(ev collector.Event) collector.SystemInfo {
	return collector.SystemInfo{
		CPU:         ev.CPU,
		Memory:      ev.Memory,
		Network:     ev.Network,
		Hostname:    ev.Hostname,
		LastUpdate:  ev.Timestamp,
	}
}