package studio

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

// Many mounted volumes (macOS exposes a dozen APFS system volumes) must not
// push the Disk I/O panel below the footer at any common terminal height.
func TestDiskViewFitsContentHeightWithManyVolumes(t *testing.T) {
	for _, size := range []struct{ width, height int }{{80, 24}, {80, 26}, {100, 30}, {120, 40}, {160, 50}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			m := operationalOverviewFixture(t, size.width, size.height)
			m.last.Disk.Partitions = nil
			for i := 0; i < 14; i++ {
				m.last.Disk.Partitions = append(m.last.Disk.Partitions, collector.DiskPartitionInfo{
					MountPoint: fmt.Sprintf("/System/Volumes/vol%02d", i), Filesystem: "apfs",
					TotalBytes: 500 << 30, UsedBytes: 250 << 30, UsagePercent: 50,
				})
			}
			m.last.Disk.ReadHistory = []float64{1, 2, 3}
			m.last.Disk.WriteHistory = []float64{3, 2, 1}
			got := m.renderDisk()
			budget := size.height - 4 // 3-row header + footer
			if h := lipgloss.Height(got); h > budget {
				t.Fatalf("disk view height = %d, content budget = %d:\n%s", h, budget, ansi.Strip(got))
			}
			if w := lipgloss.Width(got); w > size.width {
				t.Fatalf("disk view width = %d > terminal %d", w, size.width)
			}
		})
	}
}

// Every tab must fit the content area (between the 3-row header and the
// footer) and the terminal width at common sizes, so nothing is silently
// cropped by the frame.
func TestEveryViewFitsCommonTerminalSizes(t *testing.T) {
	views := map[string]func(Model) string{
		"overview": Model.renderOverview, "cpu": Model.renderCPU, "memory": Model.renderMemory,
		"thermal": Model.renderTemperature, "disk": Model.renderDisk, "network": Model.renderNetwork,
		"settings": Model.renderSettings,
	}
	for _, size := range []struct{ width, height int }{{80, 24}, {100, 30}, {120, 40}, {200, 60}} {
		for name, render := range views {
			t.Run(fmt.Sprintf("%s/%dx%d", name, size.width, size.height), func(t *testing.T) {
				m := operationalOverviewFixture(t, size.width, size.height)
				got := render(m)
				if h, budget := lipgloss.Height(got), size.height-4; h > budget {
					t.Fatalf("height = %d, content budget = %d:\n%s", h, budget, ansi.Strip(got))
				}
				for i, line := range strings.Split(got, "\n") {
					if w := lipgloss.Width(line); w > size.width {
						t.Fatalf("line %d width = %d > terminal %d", i, w, size.width)
					}
				}
			})
		}
	}
}
