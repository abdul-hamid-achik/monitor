package tuistudio

import (
	"fmt"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/history"
)

// trendsCache is the Trends tab's data, refreshed off the sample path
// (throttled to every 5s while the tab is visible) exactly like
// internal/ui/studio/trends.go's Model.trends: a render must never do
// blocking disk I/O, and here every render is just tuimark laying out
// already-bound data.
type trendsCache struct {
	Unavailable bool
	Message     string
	CPUHist     []float64
	CPUStats    string
	MemHist     []float64
	MemStats    string
}

func (t trendsCache) toMap() map[string]any {
	return map[string]any{
		"unavailable": t.Unavailable, "message": t.Message,
		"cpu": map[string]any{"hist": toAnySlice(t.CPUHist), "stats": t.CPUStats},
		"mem": map[string]any{"hist": toAnySlice(t.MemHist), "stats": t.MemStats},
	}
}

// refreshTrends loads the last hour of "cpu.usage"/"mem.usage" from the
// persistent history store (populated by `monitor history record`), or
// reports why it could not: no store yet ("norec", matching the Bubble Tea
// studio's sentinel) or a query error.
func (s *studio) refreshTrends() error {
	since := time.Now().Add(-time.Hour)
	points, err, noStore := s.history.recent([]string{"cpu.usage", "mem.usage"}, since)

	s.mu.Lock()
	s.trendsAt = time.Now()
	s.mu.Unlock()

	var cache trendsCache
	switch {
	case noStore:
		cache = trendsCache{Unavailable: true,
			Message: "No recorded history yet. Run `monitor history record` to capture metrics over time."}
	case err != nil:
		cache = trendsCache{Unavailable: true, Message: err.Error()}
	default:
		cpuPts, memPts := points["cpu.usage"], points["mem.usage"]
		cache = trendsCache{
			CPUHist: valuesOf(cpuPts), CPUStats: trendSummary(cpuPts),
			MemHist: valuesOf(memPts), MemStats: trendSummary(memPts),
		}
	}
	s.mu.Lock()
	s.trends = cache
	s.mu.Unlock()
	return s.ui.Set("trends", cache.toMap())
}

func valuesOf(pts []history.Point) []float64 {
	out := make([]float64, len(pts))
	for i, p := range pts {
		out[i] = p.Value
	}
	return out
}

func trendSummary(pts []history.Point) string {
	if len(pts) == 0 {
		return "No samples in the last hour · run `monitor history record` to collect them."
	}
	sum := history.Summarize(pts)
	return fmt.Sprintf("samples %d · min %.1f · avg %.1f · p95 %.1f · max %.1f · trend %+.1f",
		sum.Count, sum.Min, sum.Avg, sum.P95, sum.Max, sum.Trend)
}
