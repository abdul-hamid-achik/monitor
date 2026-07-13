package studio

import (
	"fmt"
	"strings"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/history"
	"github.com/abdul-hamid-achik/monitor/internal/widgets"
)

// refreshTrends loads longer-range history (populated by `monitor history
// record`) into the Model's cache. It performs blocking disk I/O (open + scan
// + close), so it MUST be called from Update — never from View()/renderTrends.
// It is throttled by the tickMsg handler to avoid re-opening the store every
// frame.
func (m *Model) refreshTrends() {
	path, err := history.DefaultPath()
	if err != nil {
		m.trendsErr = err.Error()
		m.trends = nil
		m.trendsAt = time.Now()
		return
	}
	store, err := history.OpenReadOnly(path)
	if err != nil {
		// No store yet, or a recorder holds the writer lock. Either way there's
		// nothing to plot right now.
		m.trendsErr = "norec"
		m.trends = nil
		m.trendsAt = time.Now()
		return
	}
	since := time.Now().Add(-time.Hour)
	series := make([]trendSeries, 0, 2)
	for _, metric := range []string{"cpu.usage", "mem.usage"} {
		pts, queryErr := store.Query(metric, since)
		if queryErr != nil {
			closeErr := store.Close()
			if closeErr != nil {
				m.trendsErr = fmt.Sprintf("%v; close history store: %v", queryErr, closeErr)
			} else {
				m.trendsErr = queryErr.Error()
			}
			m.trends = nil
			m.trendsAt = time.Now()
			return
		}
		series = append(series, trendSeries{metric: metric, pts: pts})
	}
	if err := store.Close(); err != nil {
		m.trendsErr = fmt.Sprintf("close history store: %v", err)
		m.trends = nil
		m.trendsAt = time.Now()
		return
	}
	m.trends = series
	m.trendsErr = ""
	m.trendsAt = time.Now()
}

// renderTrends formats the cached trend series. Pure: no I/O (see refreshTrends).
func (m Model) renderTrends() string {
	switch {
	case m.trendsErr == "norec":
		return m.panelStyle.Width(m.width - 4).Render(m.titleStyle.Render(" Trends ") + "\n\n" +
			"No recorded history yet.\nRun `monitor history record` to capture metrics over time.")
	case m.trendsErr != "":
		return m.panelStyle.Width(m.width - 4).Render(m.titleStyle.Render(" Trends ") + "\n\n" + m.trendsErr)
	case m.trends == nil:
		return m.panelStyle.Width(m.width - 4).Render(m.titleStyle.Render(" Trends ") + "\n\nLoading…")
	}
	var panels []string
	for _, s := range m.trends {
		panels = append(panels, m.trendPanel(s.metric, s.pts))
	}
	return strings.Join(panels, "\n")
}

func (m Model) trendPanel(metric string, pts []history.Point) string {
	width := m.width - 4
	if len(pts) == 0 {
		return m.panelStyle.Width(width).Render(m.titleStyle.Render(" "+metric+" (last 1h) ") + "\n\n(no samples in the last hour)")
	}
	vals := make([]float64, len(pts))
	for i, p := range pts {
		vals[i] = p.Value
	}
	s := history.Summarize(pts)
	spark := widgets.NewSparkline()
	spark.Data = vals
	if w := m.width - 10; w > 0 {
		spark.Width = w
	}
	spark.Height = 5
	body := spark.Render() + fmt.Sprintf(
		"\n  %d samples  │  min %.1f  avg %.1f  p95 %.1f  max %.1f  │  trend %+.1f",
		s.Count, s.Min, s.Avg, s.P95, s.Max, s.Trend)
	return m.panelStyle.Width(width).Render(m.titleStyle.Render(" "+metric+" (last 1h) ") + "\n\n" + body)
}
