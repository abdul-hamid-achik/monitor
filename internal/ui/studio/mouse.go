package studio

import (
	"fmt"

	"charm.land/lipgloss/v2"
)

// tabHitTest maps a header click x-coordinate to a tab index, given the title
// width and each tab's rendered width (in view order). Returns false when x is
// over the title or past the last tab.
func tabHitTest(x, titleWidth int, tabWidths []int) (viewID, bool) {
	if x < titleWidth {
		return 0, false
	}
	cur := titleWidth
	for i, w := range tabWidths {
		if x >= cur && x < cur+w {
			return viewID(i), true
		}
		cur += w
	}
	return 0, false
}

var fullTabLabels = []string{
	"1:Overview", "2:CPU", "3:Memory", "4:Temperature", "5:Disk",
	"6:Network", "7:Processes", "8:Settings", "9:Trends",
}

var compactTabLabels = []string{
	"1:Ovr", "2:CPU", "3:Mem", "4:Tmp", "5:Dsk",
	"6:Net", "7:Proc", "8:Cfg", "9:Trend",
}

type headerTabs struct {
	title   string
	labels  []string
	views   []viewID
	compact bool
}

func allTabViews() []viewID {
	views := make([]viewID, viewCount)
	for i := range views {
		views[i] = viewID(i)
	}
	return views
}

// headerLayout selects the most informative tab row that fits. Full names are
// used on wide terminals, abbreviations around 80 columns, and a single active
// tab on very narrow terminals. This prevents the header wrapping into the
// content while keeping numeric shortcuts discoverable.
func (m Model) headerLayout() headerTabs {
	full := headerTabs{
		title:  fmt.Sprintf(" MONITOR  %s ", m.last.LastUpdate.Format("15:04:05")),
		labels: fullTabLabels,
		views:  allTabViews(),
	}
	if m.width <= 0 || m.headerLayoutWidth(full) <= m.width {
		return full
	}
	compact := headerTabs{title: " MONITOR ", labels: compactTabLabels, views: allTabViews(), compact: true}
	if m.headerLayoutWidth(compact) <= m.width {
		return compact
	}
	idx := int(m.view)
	if idx < 0 || idx >= len(fullTabLabels) {
		idx = 0
	}
	return headerTabs{
		title:  " MONITOR ‹tab› ",
		labels: []string{fullTabLabels[idx]},
		views:  []viewID{viewID(idx)},
	}
}

func (m Model) headerLayoutWidth(layout headerTabs) int {
	width := lipgloss.Width(m.titleStyle.Render(layout.title))
	for i, label := range layout.labels {
		if layout.views[i] == m.view {
			width += lipgloss.Width(m.headerTabStyle(true, layout.compact).Render("▸" + label))
		} else {
			width += lipgloss.Width(m.headerTabStyle(false, layout.compact).Render(" " + label))
		}
	}
	return width
}

func (m Model) headerTabStyle(active, compact bool) lipgloss.Style {
	style := m.tabInactive
	if active {
		style = m.tabActive
	}
	if compact {
		style = style.Padding(0)
	}
	return style
}

// tabWidths returns the actual rendered tab widths for the chosen responsive
// layout. Active and inactive labels intentionally have the same one-cell
// prefix so pointer hit boxes remain stable.
func (m Model) tabWidths() []int {
	layout := m.headerLayout()
	w := make([]int, len(layout.labels))
	for i, label := range layout.labels {
		if layout.views[i] == m.view {
			w[i] = lipgloss.Width(m.headerTabStyle(true, layout.compact).Render("▸" + label))
		} else {
			w[i] = lipgloss.Width(m.headerTabStyle(false, layout.compact).Render(" " + label))
		}
	}
	return w
}

// titleWidth is the rendered width of the header title that precedes the tabs.
func (m Model) titleWidth() int {
	return lipgloss.Width(m.titleStyle.Render(m.headerLayout().title))
}

func (m Model) headerTabAt(x int) (viewID, bool) {
	layout := m.headerLayout()
	idx, ok := tabHitTest(x, m.titleWidth(), m.tabWidths())
	if !ok || int(idx) >= len(layout.views) {
		return 0, false
	}
	return layout.views[int(idx)], true
}
