package tuistudio

import (
	"context"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/config"
	"github.com/abdul-hamid-achik/monitor/internal/history"
	"github.com/abdul-hamid-achik/monitor/internal/kill"
)

// metricSource is the seam between the studio host and where its samples
// come from: the real collector.Collector in production, or the
// deterministic fixture in tests and glyphrun specs. *collector.Collector
// already satisfies this (Run, Snapshot, SetInterval, Subscribe all match),
// so production wiring needs no adapter.
type metricSource interface {
	Run(ctx context.Context) error
	Snapshot() collector.SystemInfo
	SetInterval(d time.Duration)
	Subscribe(fn collector.Subscriber) func()
	Capture(ctx context.Context) (collector.SystemInfo, error)
}

var _ metricSource = (*collector.Collector)(nil)

// killer is the seam over internal/kill, so a glyphrun spec (or a Go test)
// can drive the exact same confirm/cancel/force flow the real kill package
// offers without ever sending a real signal to a real process.
type killer interface {
	CheckSafety(pids []int32) kill.Confirmation
	Verify(pid int32, force bool) (kill.Result, error)
}

type realKiller struct{}

func (realKiller) CheckSafety(pids []int32) kill.Confirmation { return kill.CheckSafety(pids) }
func (realKiller) Verify(pid int32, force bool) (kill.Result, error) {
	return kill.KillVerified(pid, force)
}

// settingsStore is the seam over internal/config, so fixture/test runs
// never read or write the user's real ~/.config/monitor/config.json.
type settingsStore interface {
	Load() (*config.Settings, error)
	Save(*config.Settings) error
	Path() (string, error)
}

type realSettingsStore struct{}

func (realSettingsStore) Load() (*config.Settings, error) { return config.Load() }
func (realSettingsStore) Save(s *config.Settings) error   { return s.Save() }
func (realSettingsStore) Path() (string, error)           { return config.Path() }

// historyReader is the seam over internal/history. recent reports
// noStore=true when there is no history store yet (or a recorder holds the
// writer lock), matching the Bubble Tea studio's "norec" sentinel.
type historyReader interface {
	recent(metrics []string, since time.Time) (points map[string][]history.Point, err error, noStore bool)
}

type realHistoryReader struct{}

func (realHistoryReader) recent(metrics []string, since time.Time) (map[string][]history.Point, error, bool) {
	path, err := history.DefaultPath()
	if err != nil {
		return nil, err, false
	}
	store, err := history.OpenReadOnly(path)
	if err != nil {
		return nil, nil, true
	}
	defer store.Close()
	out := make(map[string][]history.Point, len(metrics))
	for _, m := range metrics {
		pts, err := store.Query(m, since)
		if err != nil {
			return nil, err, false
		}
		out[m] = pts
	}
	return out, nil, false
}
