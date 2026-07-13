package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/analyzer"
	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/config"
	"github.com/abdul-hamid-achik/monitor/internal/incidents"
	"github.com/abdul-hamid-achik/monitor/internal/notify"
)

// Event is the NDJSON event type emitted by `monitor watch --json`.
//
// Schema fields are stable; do not reorder without a migration note.
// Additive nested fields are allowed: since sprint 4.1, "alert" objects may
// carry an optional "diagnosis" {summary, evidence, confidence, next_actions}
// built by the analyzer; consumers must ignore keys they don't know.
type Event struct {
	Type      string                   `json:"type"`
	Timestamp time.Time                `json:"timestamp"`
	CPU       *collector.CPUInfo       `json:"cpu,omitempty"`
	Memory    *collector.MemoryInfo    `json:"memory,omitempty"`
	Network   *collector.NetworkInfo   `json:"network,omitempty"`
	Hostname  string                   `json:"hostname,omitempty"`
	Alert     *collector.Alert         `json:"alert,omitempty"`
	Stash     *incidents.CaptureResult `json:"stash,omitempty"`
	StashErr  string                   `json:"stash_error,omitempty"`
}

const defaultAlertCooldown = time.Minute

// alertCooldownGate suppresses repeated alerts for the same stable subject.
// Alert detail is deliberately not part of the identity: percentages and
// regression evidence change on every sample even while the underlying
// condition remains active. The gate is owned by the watch loop goroutine, so
// it needs no locking.
type alertCooldownGate struct {
	cooldown time.Duration
	last     map[string]time.Time
	nextGC   time.Time
}

func newAlertCooldownGate(cooldown time.Duration) *alertCooldownGate {
	return &alertCooldownGate{
		cooldown: cooldown,
		last:     make(map[string]time.Time),
	}
}

// allow reports whether alert should be emitted and delivered at timestamp.
// A zero/negative cooldown disables suppression. A zero timestamp falls back
// to wall-clock time so hand-built collector.Events behave sensibly too.
func (g *alertCooldownGate) allow(alert collector.Alert, timestamp time.Time) bool {
	if g == nil || g.cooldown <= 0 {
		return true
	}
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	// Bound the identity map for long-running watches. Entries older than one
	// cooldown can no longer suppress anything and are safe to discard.
	if g.nextGC.IsZero() || !timestamp.Before(g.nextGC) {
		for key, seen := range g.last {
			if !timestamp.Before(seen.Add(g.cooldown)) {
				delete(g.last, key)
			}
		}
		g.nextGC = timestamp.Add(g.cooldown)
	}

	key := alertIdentity(alert)
	if seen, ok := g.last[key]; ok {
		elapsed := timestamp.Sub(seen)
		if elapsed >= 0 && elapsed < g.cooldown {
			return false
		}
	}
	// A backwards clock jump is treated as a new observation and resets this
	// identity's window instead of suppressing it indefinitely.
	g.last[key] = timestamp
	return true
}

// alertIdentity is stable across changing metric values. Per-process alerts
// are scoped by PID; disk alerts are scoped by mount point; system-wide rules
// (swap/cpu/memory thresholds) are scoped by rule.
func alertIdentity(alert collector.Alert) string {
	rule := alert.Rule
	if rule == "" {
		rule = "unknown"
	}
	subject := "system"
	switch {
	case alert.PID > 0:
		subject = "pid:" + strconv.FormatInt(int64(alert.PID), 10)
	case alert.Rule == "disk_fill":
		// DiskFillRule formats detail as "<mount> at <pct>% (...)". Use the
		// last delimiter so a mount point containing " at " still works.
		if i := strings.LastIndex(alert.Detail, " at "); i > 0 {
			subject = "filesystem:" + alert.Detail[:i]
		} else {
			subject = "filesystem:unknown"
		}
	case alert.Process != "":
		subject = "process:" + alert.Process
	}
	return rule + "|" + subject
}

type watchAlertHandler func(collector.Event, collector.Alert, *incidents.Diagnosis)

// deliveryLimiter bounds concurrent side-effecting alert deliveries. When all
// slots are occupied, submit returns false instead of spawning another
// goroutine; the watch loop remains responsive under a slow webhook/notifier.
type deliveryLimiter struct {
	slots chan struct{}
	wg    sync.WaitGroup
}

func newDeliveryLimiter(limit int) *deliveryLimiter {
	return &deliveryLimiter{slots: make(chan struct{}, limit)}
}

func (d *deliveryLimiter) submit(fn func()) bool {
	select {
	case d.slots <- struct{}{}:
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			defer func() { <-d.slots }()
			fn()
		}()
		return true
	default:
		return false
	}
}

func (d *deliveryLimiter) wait() { d.wg.Wait() }

// emitAlerts applies the cooldown before both observable paths: NDJSON output
// and side-effecting sinks. This keeps stashes, webhooks, and desktop notices
// in exact parity with the alerts a watch consumer sees.
func emitAlerts(ev collector.Event, alerts []collector.Alert, gate *alertCooldownGate, handlers []watchAlertHandler) error {
	for _, alert := range alerts {
		if !gate.allow(alert, ev.Timestamp) {
			continue
		}
		alertEvent := Event{Type: "alert", Timestamp: ev.Timestamp, Alert: &alert}
		if err := WriteNDJSON(alertEvent); err != nil {
			return err
		}
		diagnosis := diagnosisOf(alert)
		for _, handler := range handlers {
			handler(ev, alert, diagnosis)
		}
	}
	return nil
}

func newWatchCmd() *cobra.Command {
	var (
		interval      time.Duration
		alertCooldown time.Duration
		deliveryLimit int
		once          bool
		stash         bool
		stashTTL      string
		webhookURL    string
		notifyDesktop bool
	)
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Stream NDJSON events of system metrics",
		Long: `Stream metrics as NDJSON to stdout.

Each line is a self-describing JSON object with a "type" discriminator
("tick", "alert", "process", "ecosystem"). Pipe into jq or any other tool:

  monitor watch --json | jq -c 'select(.type=="tick")'

With --stash, each emitted alert captures an incident via internal/incidents
(fcheap stash, content-addressed). Sustained repeats are controlled by
		--alert-cooldown.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if interval <= 0 {
				return fmt.Errorf("--interval must be greater than zero")
			}
			if alertCooldown < 0 {
				return fmt.Errorf("--alert-cooldown must be zero or greater")
			}
			if deliveryLimit <= 0 {
				return fmt.Errorf("--delivery-limit must be greater than zero")
			}
			c := NewCollector(interval)
			ctx, cancel := Context()
			defer cancel()

			// Build the analyzer and wire the OnAlert hook.
			engine := analyzer.NewEngine()
			engine.AddRule(&analyzer.CPUSpikeRule{Factor: 3.0})
			engine.AddRule(&analyzer.RSSGrowthRule{})
			engine.AddRule(&analyzer.DiskFillRule{})
			engine.AddRule(&analyzer.SwapPressureRule{})
			engine.AddRule(&analyzer.ZombieRule{})
			// Give the config.json cpu/memory alert thresholds teeth.
			if cfg, err := config.Load(); err == nil && (cfg.CPUAlertThreshold > 0 || cfg.MemoryAlertThreshold > 0) {
				engine.AddRule(&analyzer.ThresholdRule{CPUPercent: cfg.CPUAlertThreshold, MemPercent: cfg.MemoryAlertThreshold})
			}

			// Each enabled sink is one alert handler; all run in goroutines so
			// the watch loop never blocks on I/O. A WaitGroup tracks every
			// in-flight delivery so the command can drain them before exiting
			// (otherwise `watch --once --stash/--webhook/--notify` would return
			// and kill the still-running goroutines mid-delivery).
			deliveries := newDeliveryLimiter(deliveryLimit)
			defer deliveries.wait()
			var handlers []watchAlertHandler
			if stash {
				handlers = append(handlers, func(ev collector.Event, a collector.Alert, d *incidents.Diagnosis) {
					if !deliveries.submit(func() {
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
							Diagnosis: d,
							Trigger:   "alert",
							TTL:       stashTTL,
						})
						ev2 := Event{Type: "stash", Timestamp: time.Now(), Stash: &res}
						if err != nil {
							ev2.StashErr = err.Error()
						}
						_ = WriteNDJSON(ev2)
					}) {
						fmt.Fprintln(os.Stderr, "monitor: alert delivery limit reached; dropped stash capture")
					}
				})
			}
			if webhookURL != "" {
				client := &http.Client{Timeout: 10 * time.Second}
				handlers = append(handlers, func(_ collector.Event, a collector.Alert, d *incidents.Diagnosis) {
					nd := toNotifyDiagnosis(d)
					if !deliveries.submit(func() {
						ctx2, cancel := context.WithTimeout(context.Background(), 12*time.Second)
						defer cancel()
						if err := notify.Webhook(ctx2, client, webhookURL, a, nd); err != nil {
							fmt.Fprintf(os.Stderr, "monitor: webhook failed: %v\n", err)
						}
					}) {
						fmt.Fprintln(os.Stderr, "monitor: alert delivery limit reached; dropped webhook")
					}
				})
			}
			if notifyDesktop {
				handlers = append(handlers, func(_ collector.Event, a collector.Alert, d *incidents.Diagnosis) {
					nd := toNotifyDiagnosis(d)
					if !deliveries.submit(func() {
						ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						if err := notify.Desktop(ctx2, a, nd); err != nil {
							fmt.Fprintf(os.Stderr, "monitor: desktop notify failed: %v\n", err)
						}
					}) {
						fmt.Fprintln(os.Stderr, "monitor: alert delivery limit reached; dropped desktop notification")
					}
				})
			}
			gate := newAlertCooldownGate(alertCooldown)

			if once {
				info := c.Collect(ctx)
				ev := collector.Event{
					Timestamp: info.LastUpdate,
					Hostname:  info.Hostname,
					CPU:       info.CPU,
					Memory:    info.Memory,
					Network:   info.Network,
					Disk:      info.Disk,
					Processes: info.Processes,
				}
				// Run analyzer so once --json + --stash still captures.
				if err := emitAlerts(ev, engine.Observe(ev), gate, handlers); err != nil {
					return err
				}
				err := WriteNDJSON(Event{
					Type:      "tick",
					Timestamp: info.LastUpdate,
					CPU:       &info.CPU,
					Memory:    &info.Memory,
					Network:   &info.Network,
					Hostname:  info.Hostname,
				})
				// Drain any alert deliveries spawned above before exiting,
				// otherwise --once would abandon them mid-flight.
				deliveries.wait()
				return err
			}
			err := watchLoop(ctx, c, engine, interval, gate, handlers)
			// On shutdown (ctx cancel), let the final tick's deliveries finish.
			deliveries.wait()
			return err
		},
	}
	cmd.Flags().DurationVarP(&interval, "interval", "i", time.Second, "tick interval")
	cmd.Flags().DurationVar(&alertCooldown, "alert-cooldown", defaultAlertCooldown,
		"minimum time before re-emitting the same sustained alert (0 disables suppression)")
	cmd.Flags().IntVar(&deliveryLimit, "delivery-limit", 8,
		"maximum concurrent stash/webhook/desktop alert deliveries")
	cmd.Flags().Bool("json", false, "emit NDJSON to stdout")
	cmd.Flags().BoolVar(&once, "once", false, "emit one event and exit")
	cmd.Flags().BoolVar(&stash, "stash", false,
		"on each emitted alert, capture the incident to fcheap via internal/incidents")
	cmd.Flags().StringVar(&stashTTL, "stash-ttl", "7d",
		"TTL for incident stashes (passed to fcheap --ttl)")
	cmd.Flags().StringVar(&webhookURL, "webhook", "",
		"POST each alert as JSON to this URL")
	cmd.Flags().BoolVar(&notifyDesktop, "notify", false,
		"show a desktop notification on each alert (osascript / notify-send)")
	return cmd
}

func watchLoop(ctx context.Context, c *collector.Collector, engine *analyzer.Engine, interval time.Duration, gate *alertCooldownGate, handlers []watchAlertHandler) error {
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
				Disk:      info.Disk,
				Processes: info.Processes,
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
			if err := emitAlerts(ev, engine.Observe(ev), gate, handlers); err != nil {
				return err
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

// eventToSystemInfo flattens a collector.Event into the SystemInfo shape the
// incident stash needs. Disk and Processes are intentionally retained: they
// are the primary evidence for disk_fill and per-process anomaly alerts.
func eventToSystemInfo(ev collector.Event) collector.SystemInfo {
	return collector.SystemInfo{
		CPU:                 ev.CPU,
		Memory:              ev.Memory,
		Network:             ev.Network,
		Disk:                ev.Disk,
		Processes:           ev.Processes,
		ProcessesLastUpdate: ev.Timestamp,
		Hostname:            ev.Hostname,
		LastUpdate:          ev.Timestamp,
	}
}

// diagnosisOf extracts the analyzer's diagnosis for an alert as the
// incidents.Diagnosis mirror. nil when the alert carries no diagnosis.
func diagnosisOf(a collector.Alert) *incidents.Diagnosis {
	if a.Diagnosis == nil {
		return nil
	}
	return &incidents.Diagnosis{
		Summary:     a.Diagnosis.Summary,
		Evidence:    a.Diagnosis.Evidence,
		Confidence:  a.Diagnosis.Confidence,
		NextActions: a.Diagnosis.NextActions,
	}
}

// toNotifyDiagnosis converts the incidents-side mirror into notify's. Both
// mirror analyzer.Diagnosis; the field copy keeps the two packages' JSON
// schemas independently stable.
func toNotifyDiagnosis(d *incidents.Diagnosis) *notify.Diagnosis {
	if d == nil {
		return nil
	}
	return &notify.Diagnosis{
		Summary:     d.Summary,
		Evidence:    d.Evidence,
		Confidence:  d.Confidence,
		NextActions: d.NextActions,
	}
}
