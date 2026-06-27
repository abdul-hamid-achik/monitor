package v2

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

var tabLabels = []string{
	" 1:Overview ", " 2:CPU ", " 3:Memory ", " 4:Temperature ", " 5:Disk ",
	" 6:Network ", " 7:Processes ", " 8:Settings ", " 9:Trends ",
}

// tabWidths returns the rendered width of each tab, in view order. It mirrors
// renderHeader so click hit-testing matches what is drawn.
func (m Model) tabWidths() []int {
	w := make([]int, len(tabLabels))
	for i, l := range tabLabels {
		w[i] = lipgloss.Width(m.tabInactive.Render(l))
	}
	return w
}

// titleWidth is the rendered width of the header title that precedes the tabs.
func (m Model) titleWidth() int {
	return lipgloss.Width(m.titleStyle.Render(fmt.Sprintf(" MONITOR (v2)  %s ", m.last.LastUpdate.Format("15:04:05"))))
}
