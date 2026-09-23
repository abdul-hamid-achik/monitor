package analyzer

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/monitor/internal/config"
)

func ruleNames(e *Engine) []string {
	names := make([]string, len(e.rules))
	for i, r := range e.rules {
		names[i] = r.Name()
	}
	return names
}

// TestNewDefaultEngineMatchesWatchRuleSet is the parity regression for bug
// 17: watch, Studio, and the MCP analyze window must all register the exact
// same base rule set (in the exact same order the alerts are evaluated),
// with CPUSpikeRule's factor pinned to 3.0 like watch's pre-existing engine.
func TestNewDefaultEngineMatchesWatchRuleSet(t *testing.T) {
	e := NewDefaultEngine(config.Settings{})
	got := ruleNames(e)
	want := []string{"cpu_spike", "rss_growth", "disk_fill", "swap_pressure", "zombie_process"}
	if !slices.Equal(got, want) {
		t.Fatalf("rule set = %v, want %v", got, want)
	}
	spike, ok := e.rules[0].(*CPUSpikeRule)
	if !ok {
		t.Fatalf("rules[0] = %T, want *CPUSpikeRule", e.rules[0])
	}
	if spike.Factor != 3.0 {
		t.Errorf("cpu_spike Factor = %v, want 3.0", spike.Factor)
	}
}

// TestNewDefaultEngineOmitsThresholdRuleWhenUnconfigured verifies a zero-value
// config (both thresholds disabled) does not register ThresholdRule, matching
// watch's existing "give the config.json thresholds teeth" gating.
func TestNewDefaultEngineOmitsThresholdRuleWhenUnconfigured(t *testing.T) {
	e := NewDefaultEngine(config.Settings{})
	for _, r := range e.rules {
		if r.Name() == "threshold" {
			t.Fatalf("threshold rule registered with no configured threshold: %v", ruleNames(e))
		}
	}
}

// TestNewDefaultEngineAddsThresholdRuleWhenConfigured covers both the CPU-only
// and memory-only trigger paths for the optional rule.
func TestNewDefaultEngineAddsThresholdRuleWhenConfigured(t *testing.T) {
	for _, cfg := range []config.Settings{
		{CPUAlertThreshold: 80},
		{MemoryAlertThreshold: 90},
		{CPUAlertThreshold: 80, MemoryAlertThreshold: 90},
	} {
		e := NewDefaultEngine(cfg)
		names := ruleNames(e)
		if names[len(names)-1] != "threshold" {
			t.Fatalf("cfg=%+v: expected threshold rule appended last, got %v", cfg, names)
		}
		th, ok := e.rules[len(e.rules)-1].(*ThresholdRule)
		if !ok {
			t.Fatalf("cfg=%+v: last rule = %T, want *ThresholdRule", cfg, e.rules[len(e.rules)-1])
		}
		if th.CPUPercent != cfg.CPUAlertThreshold || th.MemPercent != cfg.MemoryAlertThreshold {
			t.Errorf("cfg=%+v: threshold rule = %+v", cfg, th)
		}
	}
}

// TestNewDefaultEngineIsWiredIntoConsumers is a structural guard for bug 17:
// having one rule-set source is worthless if watch, Studio, or the MCP/CLI
// analyze window quietly stop calling it and build their own *Engine again.
// It greps each consumer's source for the literal call rather than exercising
// the CLI (which would need a live collector and multi-second wall-clock
// waits), so it is cheap and fails loudly the moment any of the three drifts.
func TestNewDefaultEngineIsWiredIntoConsumers(t *testing.T) {
	for _, path := range []string{
		"../cli/watch.go",
		"../ui/studio/model.go",
		"../cli/mcp.go",
	} {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(src), "analyzer.NewDefaultEngine(") {
			t.Errorf("%s no longer calls analyzer.NewDefaultEngine — rule-set parity with `monitor watch` (bug 17) would silently drift again", path)
		}
	}
}
