package tuistudio

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/analyzer"
	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/config"
	"github.com/abdul-hamid-achik/monitor/internal/kill"
	"github.com/abdul-hamid-achik/monitor/internal/temperature"
	"github.com/abdul-hamid-achik/tuimark"
)

// studioHistorySize matches the Bubble Tea studio's sparkline buffer (one
// minute of samples at the default 1s interval).
const studioHistorySize = 60

// studio is the host's state: the tuimark.App plus everything the named
// actions read and mutate. ui.Set is goroutine-safe (SPEC), so onSample
// (called from the metricSource's own goroutine) and the action handlers
// (called from tuimark's Run goroutine while it reads stdin) both write to
// it directly; mu guards only studio's own Go fields.
type studio struct {
	ui *tuimark.App
	mu sync.Mutex

	src           metricSource
	kill          killer
	settingsStore settingsStore
	history       historyReader
	analyzer      *analyzer.Engine

	settings *config.Settings
	last     collector.SystemInfo
	view     string
	paused   bool

	sortKey  string // "cpu" or "mem"
	sortAsc  bool
	lastSort string

	query, savedQuery string

	marked    map[int32]bool
	cursorPID int32

	showKill  bool
	forceKill bool
	killConf  kill.Confirmation

	detailOpen bool
	detailPID  int32

	settingsDirty map[string]bool

	trends   trendsCache
	trendsAt time.Time

	cancel context.CancelFunc
}

// newStudio builds the host: it loads the embedded view, loads settings
// (real ~/.config/monitor/config.json, or the built-in defaults in fixture
// mode), wires the metric source, and binds the first sample so the first
// frame is never empty.
func newStudio(ctx context.Context, opts Options) (*studio, error) {
	ui, err := loadEmbeddedView()
	if err != nil {
		return nil, fmt.Errorf("tuistudio: load view: %w", err)
	}

	s := &studio{
		ui: ui, sortKey: "cpu", marked: map[int32]bool{}, view: "overview",
		detailPID: -1,
	}

	if opts.Fixture {
		s.settingsStore = &fixtureSettingsStore{}
		s.history = fixtureHistoryReader{}
	} else {
		s.settingsStore = realSettingsStore{}
		s.history = realHistoryReader{}
	}

	settings, err := s.settingsStore.Load()
	if err != nil || settings == nil {
		settings = config.Default()
	}
	s.settings = settings
	interval := settings.UpdateInterval
	if interval <= 0 {
		interval = time.Second
	}

	if opts.Fixture {
		fx := newFixtureSource()
		s.src = fx
		s.kill = fixtureKiller{src: fx}
	} else {
		c := collector.New(collector.Options{Interval: interval, HistorySize: studioHistorySize})
		if !opts.DisableTemperatureSource {
			ts := temperature.New(ctx, temperature.Options{Interval: 5 * time.Second, Logf: func(string, ...any) {}})
			c.WithTemperatureHook(func() (float64, float64, float64, float64, float64, float64, int, string, string, bool) {
				r := ts.Latest()
				return r.CPUPackage, r.CPUCores, r.GPU, r.ANE, r.Battery, r.Ambient, r.FanRPM, r.FanMode, string(r.Source), r.Available
			})
		}
		s.src = c
		s.kill = realKiller{}
	}
	s.analyzer = analyzer.NewDefaultEngine(*s.settings)

	if _, err := s.src.Capture(ctx); err != nil {
		return nil, fmt.Errorf("tuistudio: initial sample: %w", err)
	}
	s.last = s.src.Snapshot()

	if err := s.bindInitial(); err != nil {
		return nil, err
	}
	s.register()
	return s, nil
}

// bindInitial sets the whole store once, from the first sample plus the
// action-independent defaults (settings rows, empty modals, ...).
func (s *studio) bindInitial() error {
	data := map[string]any{
		"view": s.view, "mouse_enabled": s.settings.MouseEnabled,
		"cursor_pid": 0.0, "marked_pids": []any{}, "filter_open": false, "query": "",
		"kill_open": false, "kill_force": false, "kill_rows": []any{},
		"detail_open": false, "detail": map[string]any{"title": "", "present": false, "rows": []any{}},
		"settings":        settingsRowsToAny(s.settingsRows()),
		"setting_cursor":  settingSpecs[0].key,
		"settings_status": "", "settings_saved": false, "settings_failed": false,
		"help_open": false,
		"h":         map[string]any{"name": "NAME", "cpu": "CPU%▼", "mem": "MEM"},
		"trends":    trendsCache{Unavailable: true, Message: "Loading…"}.toMap(),
	}
	if err := s.ui.Bind("", data); err != nil {
		return err
	}
	return s.publishAll()
}

// register wires every named action of studio.tui to its handler.
func (s *studio) register() {
	for name, h := range s.handlers() {
		s.ui.On(name, h)
	}
}

// handlers is the full action table, kept as one map (like
// examples/monitor's fixture host) so a Go test can enumerate every action
// tuimark.Catalog names and fire each handler directly, without a live
// Run/terminal.
func (s *studio) handlers() map[string]tuimark.Handler {
	hs := map[string]tuimark.Handler{}
	on := func(name string, h tuimark.Handler) { hs[name] = h }

	on("quit", func(tuimark.Event) error { return tuimark.ErrQuit })
	on("pause", func(tuimark.Event) error {
		s.mu.Lock()
		s.paused = !s.paused
		s.mu.Unlock()
		return s.publishIdentity()
	})
	on("refresh", func(tuimark.Event) error { return s.refreshNow() })
	on("view_changed", func(ev tuimark.Event) error {
		v, _ := ev.Value.(string)
		s.mu.Lock()
		s.view = v
		s.mu.Unlock()
		return s.onViewEntered(v)
	})
	on("help_open", func(tuimark.Event) error { return s.ui.Set("help_open", true) })
	on("help_close", func(tuimark.Event) error { return s.ui.Set("help_open", false) })

	for name, h := range s.processHandlers() {
		hs[name] = h
	}
	for name, h := range s.settingsHandlers() {
		hs[name] = h
	}
	return hs
}

// onViewEntered brings a freshly-activated tab's cached state up to date,
// mirroring Bubble Tea Studio's setView: the process table and the Trends
// cache are otherwise only refreshed on the next sample tick.
func (s *studio) onViewEntered(view string) error {
	switch view {
	case "processes":
		return s.publishProcs()
	case "trends":
		s.mu.Lock()
		stale := s.trendsAt.IsZero() || time.Since(s.trendsAt) > 5*time.Second
		s.mu.Unlock()
		if stale {
			return s.refreshTrends()
		}
	}
	return nil
}

// refreshNow takes an immediate sample (used by the "r" key and by an
// external /reload request) instead of waiting for the metric source's own
// ticker.
func (s *studio) refreshNow() error {
	info, err := s.src.Capture(context.Background())
	if err != nil {
		return err
	}
	return s.applySample(info, true)
}

// start launches the metric source's own sampling loop and subscribes
// onSample to it. It returns the unsubscribe/cancel the caller should run
// on shutdown.
func (s *studio) start(ctx context.Context) {
	unsub := s.src.Subscribe(func(collector.Event) { _ = s.applySample(s.src.Snapshot(), false) })
	go func() {
		_ = s.src.Run(ctx)
		unsub()
	}()
}

// applySample is the single place a new SystemInfo becomes visible: it
// updates s.last, feeds the analyzer, and republishes every bound field a
// live tick can change. It is called from onSample (the metric source's
// own goroutine, force=false: a sample is skipped while paused, matching
// Bubble Tea Studio's tick handler) and from refreshNow (the "r" key / an
// external reload, force=true: always takes the sample immediately,
// matching Bubble Tea Studio's refreshNow, which ignores paused). Both are
// safe to interleave with tuimark.Run's goroutine because every write goes
// through ui.Set.
func (s *studio) applySample(info collector.SystemInfo, force bool) error {
	s.mu.Lock()
	paused := s.paused
	view := s.view
	detailOpen := s.detailOpen
	s.mu.Unlock()
	if paused && !force {
		return nil
	}
	s.mu.Lock()
	s.last = info
	s.mu.Unlock()

	var alerts []collector.Alert
	if s.analyzer != nil && !info.LastUpdate.IsZero() {
		alerts = s.analyzer.Observe(collector.Event{
			Timestamp: info.LastUpdate, Hostname: info.Hostname, CPU: info.CPU,
			Memory: info.Memory, Network: info.Network, Disk: info.Disk, Processes: info.Processes,
		})
	}

	errs := []error{s.publishIdentity(), s.publishMetrics(info, alerts)}
	if view == "processes" {
		errs = append(errs, s.publishProcs())
	}
	if detailOpen {
		errs = append(errs, s.publishDetail())
	}
	if view == "trends" {
		s.mu.Lock()
		stale := s.trendsAt.IsZero() || time.Since(s.trendsAt) > 5*time.Second
		s.mu.Unlock()
		if stale {
			errs = append(errs, s.refreshTrends())
		}
	}
	return joinErrs(errs)
}

func (s *studio) publishAll() error {
	return s.applySample(s.last, true)
}

func joinErrs(errs []error) error {
	var kept []error
	for _, e := range errs {
		if e != nil {
			kept = append(kept, e)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	msg := make([]string, len(kept))
	for i, e := range kept {
		msg[i] = e.Error()
	}
	return fmt.Errorf("%s", strings.Join(msg, "; "))
}

// collectionState mirrors Bubble Tea Studio's Model.collectionState.
func (s *studio) collectionState() (state, sampled string) {
	s.mu.Lock()
	last := s.last
	paused := s.paused
	interval := time.Second
	if s.settings != nil && s.settings.UpdateInterval > 0 {
		interval = s.settings.UpdateInterval
	}
	s.mu.Unlock()

	state = "LIVE"
	sampled = "waiting"
	if !last.LastUpdate.IsZero() {
		sampled = last.LastUpdate.Format("15:04:05")
	}
	switch {
	case paused:
		state = "PAUSED"
	case last.LastUpdate.IsZero():
		state = "WAITING"
	default:
		staleAfter := 3 * time.Second
		if 2*interval > staleAfter {
			staleAfter = 2 * interval
		}
		if time.Since(last.LastUpdate) > staleAfter {
			state = "STALE"
		}
	}
	return state, sampled
}

func (s *studio) publishIdentity() error {
	state, sampled := s.collectionState()
	label := map[string]string{"LIVE": "● LIVE", "PAUSED": "‖ PAUSED", "WAITING": "○ WAITING", "STALE": "▲ STALE"}[state]
	s.mu.Lock()
	host := strings.TrimSpace(s.last.Hostname)
	s.mu.Unlock()
	if host == "" {
		host = "local"
	}
	return joinErrs([]error{
		s.ui.Set("host", host),
		s.ui.Set("sampled_at", sampled),
		s.ui.Set("status_label", label),
		s.ui.Set("live", state == "LIVE"),
		s.ui.Set("paused", state == "PAUSED"),
		s.ui.Set("waiting", state == "WAITING"),
		s.ui.Set("stale", state == "STALE"),
	})
}

func pct(v float64) string { return fmt.Sprintf("%.1f%%", v) }

// publishMetrics derives every Overview/CPU/Memory/Thermal/Disk/Network
// binding from one SystemInfo. Every value here is either a number tuimark
// lays out (a *_pct gauge fill) or a string the host has already
// formatted: the document does no width, padding, truncation, or unit
// math of its own.
func (s *studio) publishMetrics(info collector.SystemInfo, alerts []collector.Alert) error {
	var errs []error
	set := func(path string, v any) { errs = append(errs, s.ui.Set(path, v)) }
	// Settings change under s.mu from the Settings handlers; read a copy.
	s.mu.Lock()
	settings := *s.settings
	s.mu.Unlock()

	cpuNote := fmt.Sprintf("%d cores", info.CPU.CoreCount)
	if info.CPU.FrequencyMHz >= 100 {
		cpuNote += fmt.Sprintf(" · %.2f GHz", info.CPU.FrequencyMHz/1000)
	}
	set("cpu", map[string]any{
		"value": "cpu " + pct(info.CPU.UsagePercent), "pct": info.CPU.UsagePercent, "note": cpuNote,
		"hist": toAnySlice(info.CPU.History),
		"stats": fmt.Sprintf("load %.2f %.2f %.2f · %d threads",
			info.CPU.LoadAvg1, info.CPU.LoadAvg5, info.CPU.LoadAvg15, info.CPU.ThreadCount),
	})

	cores := make([]any, len(info.CPU.PerCoreUsage))
	for i, v := range info.CPU.PerCoreUsage {
		cores[i] = map[string]any{"id": fmt.Sprintf("c%d", i), "label": fmt.Sprintf("cpu%d %.0f%%", i, v), "pct": v}
	}
	set("cores", cores)

	memSuffix := ""
	if info.Cgroup.Limited && info.Cgroup.MemLimitBytes > 0 {
		memSuffix = " · cgroup"
	}
	set("mem", map[string]any{
		"value": "mem " + pct(info.Memory.UsagePercent), "pct": info.Memory.UsagePercent,
		"note":       fmt.Sprintf("%s / %s%s", collector.FormatBytes(info.Memory.UsedBytes), collector.FormatBytes(info.Memory.TotalBytes), memSuffix),
		"hist":       toAnySlice(info.Memory.History),
		"app":        collector.FormatBytes(info.Memory.AppMemory),
		"wired":      collector.FormatBytes(info.Memory.WiredMemory),
		"compressed": collector.FormatBytes(info.Memory.CompressedMemory),
		"cache":      collector.FormatBytes(info.Memory.CacheMemory),
		"purgeable":  collector.FormatBytes(info.Memory.PurgeableMemory),
		"swap_value": "swap " + pct(swapPercent(info.Memory)),
		"swap_pct":   swapPercent(info.Memory),
		"swap_note":  fmt.Sprintf("%s / %s", collector.FormatBytes(info.Memory.SwapUsed), collector.FormatBytes(info.Memory.SwapTotal)),
	})

	set("thermal", thermalView(info.Temperature, settings.TemperatureUnit))

	diskUnavailable := len(info.Disk.Partitions) == 0
	diskValue, diskNote, diskPct := "unavailable", "no mounted volumes", 0.0
	partitions := make([]any, 0, len(info.Disk.Partitions))
	if !diskUnavailable {
		root := info.Disk.Partitions[0]
		for _, p := range info.Disk.Partitions {
			if p.MountPoint == "/" {
				root = p
				break
			}
		}
		diskValue, diskPct = pct(root.UsagePercent), root.UsagePercent
		diskNote = fmt.Sprintf("%s · %s used", root.MountPoint, collector.FormatBytes(root.UsedBytes))
		for _, p := range info.Disk.Partitions {
			partitions = append(partitions, map[string]any{
				"mount": p.MountPoint, "pct": p.UsagePercent,
				"note": fmt.Sprintf("%s / %s", collector.FormatBytes(p.UsedBytes), collector.FormatBytes(p.TotalBytes)),
			})
		}
	}
	diskHist := sharedScale(info.Disk.ReadHistory, info.Disk.WriteHistory)
	set("disk", map[string]any{
		"value": diskValue, "pct": diskPct, "note": diskNote, "partitions": partitions,
		"unavailable": diskUnavailable, "reason": diskNote,
		"rate_note":  fmt.Sprintf("read %s/s · write %s/s", collector.FormatBytes(info.Disk.ReadPerSec), collector.FormatBytes(info.Disk.WritePerSec)),
		"read_hist":  diskHist[0],
		"write_hist": diskHist[1],
	})

	netUnavailable := info.Network.MetricStates != nil && metricIssue(info.Network.MetricStates, "io") != ""
	netHist := sharedScale(info.Network.DownloadHistory, info.Network.UploadHistory)
	set("network", map[string]any{
		"rate_note": fmt.Sprintf("download %s/s · upload %s/s",
			collector.FormatBytes(info.Network.BytesRecvPerSec), collector.FormatBytes(info.Network.BytesSentPerSec)),
		"down_hist":    netHist[0],
		"up_hist":      netHist[1],
		"total_note":   fmt.Sprintf("total down %s · up %s", collector.FormatBytes(info.Network.BytesRecv), collector.FormatBytes(info.Network.BytesSent)),
		"packets_note": fmt.Sprintf("packets down %d · up %d", info.Network.PacketsRecv, info.Network.PacketsSent),
		"unavailable":  netUnavailable, "reason": metricIssue(info.Network.MetricStates, "io"),
	})

	top := topByCPU(info.Processes, 5)
	topRows := make([]any, len(top))
	for i, p := range top {
		topRows[i] = map[string]any{"pid": float64(p.PID), "name": p.Name, "cpu": pct(p.CPUPercent)}
	}
	set("top", topRows)

	message := attentionMessage(alerts, &settings, info)
	set("attention", map[string]any{"has": message != "", "message": message})

	return joinErrs(errs)
}

func swapPercent(m collector.MemoryInfo) float64 {
	if m.SwapTotal == 0 {
		return 0
	}
	return float64(m.SwapUsed) / float64(m.SwapTotal) * 100
}

func metricIssue(states map[string]collector.MetricStatus, key string) string {
	st, ok := states[key]
	if !ok || st.State == "" || st.State == collector.MetricObserved {
		return ""
	}
	if r := strings.TrimSpace(st.Reason); r != "" {
		return r
	}
	return string(st.State)
}

func topByCPU(procs []collector.ProcessInfo, n int) []collector.ProcessInfo {
	out := append([]collector.ProcessInfo(nil), procs...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].CPUPercent > out[j].CPUPercent })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func attentionMessage(alerts []collector.Alert, settings *config.Settings, info collector.SystemInfo) string {
	var parts []string
	if len(alerts) > 0 {
		a := alerts[len(alerts)-1]
		label := strings.ToUpper(strings.ReplaceAll(a.Rule, "_", " "))
		if label == "" {
			label = "ANOMALY"
		}
		parts = append(parts, label+" | "+a.Detail)
	}
	if settings != nil {
		if t := settings.CPUAlertThreshold; t > 0 && info.CPU.UsagePercent >= t {
			parts = append(parts, fmt.Sprintf("CPU %.1f%% >= %.0f%% threshold", info.CPU.UsagePercent, t))
		}
		if t := settings.MemoryAlertThreshold; t > 0 && info.Memory.UsagePercent >= t {
			parts = append(parts, fmt.Sprintf("memory %.1f%% >= %.0f%% threshold", info.Memory.UsagePercent, t))
		}
	}
	return strings.Join(parts, " · ")
}

// thermalView is the "thermal" binding: readings in the configured unit
// (°F when Settings selects it), as the Bubble Tea studio's formatTemp and
// renderTemperature do. The *_pct gauge fills stay in Celsius.
func thermalView(temp collector.TemperatureInfo, unit string) map[string]any {
	unavailable := temp.State.State != "" && temp.State.State != collector.MetricObserved
	tempNote := "estimated from CPU load"
	if temp.Source == "powermetrics" {
		tempNote = "real sensor"
	}
	if unavailable {
		tempNote = strings.TrimSpace(temp.State.Reason)
	}
	fan := "unavailable · restricted SMC keys"
	if temp.FanRPM > 0 || temp.FanMode != "" {
		fan = fmt.Sprintf("%d RPM · %s", temp.FanRPM, temp.FanMode)
	}
	return map[string]any{
		"value": formatTemp(temp.CPUPackage, unit), "pct": temp.CPUPackage, "note": tempNote,
		"source": temp.Source, "fan": fan, "unavailable": unavailable, "reason": tempNote,
		"cpu_package_pct": temp.CPUPackage, "cpu_package": formatTemp(temp.CPUPackage, unit),
		"cpu_cores_pct": temp.CPUCores, "cpu_cores": formatTemp(temp.CPUCores, unit),
		"gpu_pct": temp.GPU, "gpu": formatTemp(temp.GPU, unit),
		"ane_pct": temp.ANE, "ane": formatTemp(temp.ANE, unit),
		"battery_pct": temp.Battery, "battery": formatTemp(temp.Battery, unit),
	}
}

// formatTemp renders a Celsius reading in unit ("F" converts; anything
// else is Celsius).
func formatTemp(c float64, unit string) string {
	if unit == "F" {
		return fmt.Sprintf("%.1f F", c*9/5+32)
	}
	return fmt.Sprintf("%.1f C", c)
}

// sharedScale maps rate histories (bytes per second) onto one 0-100 scale,
// the global min and max of all of them, as widgets.MultiSparkline does
// for the Bubble Tea studio's "recent rates · shared scale". The view's
// sparklines take min="0" max="100": a Tuimark sparkline's min and max are
// literals, so the shared scale has to be computed here.
func sharedScale(series ...[]float64) [][]any {
	lo, hi, seen := 0.0, 0.0, false
	for _, s := range series {
		for _, v := range s {
			if !seen || v < lo {
				lo = v
			}
			if !seen || v > hi {
				hi = v
			}
			seen = true
		}
	}
	if hi <= lo {
		hi = lo + 1
	}
	out := make([][]any, len(series))
	for i, s := range series {
		out[i] = make([]any, len(s))
		for j, v := range s {
			out[i][j] = (v - lo) / (hi - lo) * 100
		}
	}
	return out
}

func toAnySlice(v []float64) []any {
	out := make([]any, len(v))
	for i, x := range v {
		out[i] = x
	}
	return out
}
