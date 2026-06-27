package v2

import (
	"fmt"
	"strings"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/history"
	"github.com/abdul-hamid-achik/monitor/internal/widgets"
)

// renderTrends shows longer-range sparklines from the persistent history store
// (populated by `monitor history record`), beyond the in-memory tick window.
func (m Model) renderTrends() string {
	path, err := history.DefaultPath()
	if err != nil {
		return m.panelStyle.Width(m.width - 4).Render(m.titleStyle.Render(" Trends ") + "\n\n" + err.Error())
	}
	store, err := history.OpenReadOnly(path)
	if err != nil {
		return m.panelStyle.Width(m.width - 4).Render(m.titleStyle.Render(" Trends ") + "\n\n" +
			"No recorded history yet.\nRun `monitor history record` to capture metrics over time.")
	}
	defer store.Close()

	since := time.Now().Add(-time.Hour)
	var panels []string
	for _, metric := range []string{"cpu.usage", "mem.usage"} {
		pts, _ := store.Query(metric, since)
		panels = append(panels, m.trendPanel(metric, pts))
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
