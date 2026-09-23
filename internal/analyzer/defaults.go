package analyzer

import "github.com/abdul-hamid-achik/monitor/internal/config"

// NewDefaultEngine returns an Engine registered with the same rule set
// `monitor watch` uses, so watch, Studio, and the MCP/CLI analyze window
// (`monitor_analyze` / `monitor analyze`) can never drift apart again.
//
// Before this existed, Studio's engine had no ThresholdRule and the MCP
// analyze window (internal/cli/mcp.go's analyzeWindow) registered no rules
// at all — the same anomaly could be flagged by `monitor watch` and silently
// missed by Studio or an agent calling monitor_analyze (bug 17).
//
// cfg supplies the config.json cpu/memory alert thresholds. A ThresholdRule
// is registered only when at least one threshold is configured (a threshold
// of 0 means "disabled"), matching watch's pre-existing behavior.
func NewDefaultEngine(cfg config.Settings) *Engine {
	e := NewEngine()
	e.AddRule(&CPUSpikeRule{Factor: 3.0})
	e.AddRule(&RSSGrowthRule{})
	e.AddRule(&DiskFillRule{})
	e.AddRule(&SwapPressureRule{})
	e.AddRule(&ZombieRule{})
	if cfg.CPUAlertThreshold > 0 || cfg.MemoryAlertThreshold > 0 {
		e.AddRule(&ThresholdRule{CPUPercent: cfg.CPUAlertThreshold, MemPercent: cfg.MemoryAlertThreshold})
	}
	return e
}
