# AGENTS.md - Monitor CLI Development Guide

This document provides essential information for AI agents working in the Monitor CLI codebase.

## Project Overview

**Monitor** is a terminal-based system monitor for macOS (optimized for Apple Silicon) built with Go and the Charm ecosystem. It features a Bubble Tea TUI with Nord theme, real-time system metrics (CPU, Memory, Temperature, Network, Processes), and safe process termination.

**Platform**: macOS Apple Silicon (M1/M2/M3/M5)  
**Language**: Go 1.25.5  
**Architecture**: Single binary CLI application

---

## Quick Reference

### Essential Commands

All commands use [Task](https://taskfile.dev/) runner (install recommended):

```bash
task build          # Build to bin/monitor
task run            # Build and run
task dev            # Development mode with watch
task test           # Run all tests
task lint           # Run go vet + staticcheck
task clean          # Remove build artifacts
task install        # Install to /usr/local/bin
task all            # Full CI pipeline (tidy, lint, test, build)
```

Manual commands:
```bash
go build -o bin/monitor ./cmd/monitor
go test -v ./...
go vet ./...
go mod tidy
```

### File Locations

- **Entry point**: `cmd/monitor/main.go`
- **UI layer**: `internal/ui/app.go` (Bubble Tea model)
- **System metrics**: `internal/system/collector.go`
- **Data types**: `internal/system/types.go`
- **Process killing**: `internal/system/kill.go`
- **Widgets**: `internal/widgets/gauge.go` (sparklines, gauges)

---

## Code Organization

```
monitor/
├── cmd/monitor/
│   └── main.go              # Entry point, initializes Bubble Tea
├── internal/
│   ├── system/
│   │   ├── collector.go     # System metrics collection (gopsutil wrapper)
│   │   ├── types.go         # Data structures (SystemInfo, ProcessInfo, etc.)
│   │   ├── kill.go          # Safe process termination with safety checks
│   │   └── *_test.go        # Unit tests
│   ├── ui/
│   │   ├── app.go           # Main Bubble Tea application model
│   │   ├── styles.go        # Nord theme styles and colors
│   │   └── app_test.go      # UI tests
│   └── widgets/
│       ├── gauge.go         # Custom gauge widgets (Sparkline, BarGauge, etc.)
│       └── gauge_test.go    # Widget tests
├── go.mod                   # Module definition
├── Taskfile.yml             # Task runner configuration
└── README.md                # User documentation
```

---

## Architecture Patterns

### 1. Bubble Tea (Elm Architecture)

The UI follows the Elm architecture pattern:

- **Model** (`internal/ui/app.go:Model`) - Holds all application state
- **Update** (`Model.Update()`) - Handles messages (key, mouse, tick, system info)
- **View** (`Model.View()`) - Renders UI to string

```go
// Message types
type systemInfoMsg struct { info system.SystemInfo }
type tickMsg time.Time

// Update pattern
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.KeyMsg: return m.handleKeyPress(msg)
    case tea.MouseMsg: return m.handleMouse(msg)
    case tickMsg: return m.handleTick(msg)
    case systemInfoMsg: return m.handleSystemInfo(msg)
    }
}
```

### 2. System Collection Layer

Metrics are collected by `system.Collector` using `gopsutil`:

```go
collector := system.NewCollector()
info := collector.Collect(ctx)  // Returns SystemInfo struct
```

**Important**: Temperature readings are **estimated** (not real) - accurate values require CGO bindings to access macOS SMC (System Management Controller).

### 3. Tab Navigation

Six tabs managed by `Tab` type enum:
- Tab 0: Overview
- Tab 1: CPU
- Tab 2: Memory
- Tab 3: Processes
- Tab 4: Temperature
- Tab 5: Settings

Tab state tracked in `Model.activeTab` with bounds calculated in `calculateTabBounds()`.

---

## Code Style & Conventions

### Naming

- **Files**: lowercase, underscores for multi-word (`collector.go`, `app.go`)
- **Types**: PascalCase with domain prefix (`CPUInfo`, `ProcessInfo`, `BarGauge`)
- **Functions**: PascalCase exported, camelCase private (`Collect` vs `collectCPU`)
- **Constants**: PascalCase (`TabOverview`, `ContextMenuNone`)
- **Variables**: camelCase, descriptive names (`selectedPids`, `lastUpdateTime`)

### Style Patterns

- **Error handling**: Return errors immediately, use early returns
- **Struct initialization**: Composite literals with field names
- **Comments**: Package-level docs, inline comments for "why" not "what"
- **Imports**: Standard lib first, third-party second, local packages last

### Example from Codebase

```go
// Collector collects system metrics
type Collector struct {
    mu           sync.RWMutex
    info         SystemInfo
    historySize  int
    lastNetStats net.IOCountersStat
}

// NewCollector creates a new system collector
func NewCollector() *Collector {
    return &Collector{
        historySize: 60, // Keep 60 data points (1 minute at 1s intervals)
        lastNetTime: time.Now(),
    }
}
```

---

## Testing Approach

### Test Files

Each `.go` file has a corresponding `*_test.go`:
- `collector_test.go` - System metrics collection tests
- `kill_test.go` - Process termination safety tests
- `app_test.go` - UI model tests
- `gauge_test.go` - Widget rendering tests

### Running Tests

```bash
task test              # Run all tests
task test:coverage     # Generate HTML coverage report
go test -v ./...       # Verbose output
```

### Test Patterns

Tests follow standard Go testing:

```go
func TestSomething(t *testing.T) {
    // Arrange
    collector := system.NewCollector()
    
    // Act
    info := collector.Collect(context.Background())
    
    // Assert
    if info.CPU.UsagePercent < 0 || info.CPU.UsagePercent > 100 {
        t.Errorf("CPU usage out of range: %f", info.CPU.UsagePercent)
    }
}
```

---

## Key Data Structures

### SystemInfo (internal/system/types.go)

```go
type SystemInfo struct {
    CPU         CPUInfo
    Memory      MemoryInfo
    Temperature TemperatureInfo
    Network     NetworkInfo
    Processes   []ProcessInfo
    Hostname    string
    OS          string
    // ...
}
```

### ProcessInfo

```go
type ProcessInfo struct {
    PID          int32
    Name         string
    CPUPercent   float64
    Memory       uint64
    MemoryPercent float64
    Threads      int32
    User         string
    IsSystem     bool
    IsProtected  bool  // Cannot be killed safely
}
```

---

## Safety Features

### Protected Processes

Critical system processes that **cannot** be killed (`internal/system/kill.go:12`):

```go
var ProtectedProcessNames = map[string]bool{
    "launchd": true, "kernel_task": true, "init": true,
    "Finder": true, "Dock": true, "WindowServer": true,
    // ... (see kill.go for full list)
}
```

### Kill Safety Checks

Before killing, `CheckKillSafety()` validates:
1. Process is not in protected list
2. Process is not root-owned (system process warning)
3. User has permissions (sudo check)
4. Confirmation dialog shown with warnings

---

## Important Gotchas

### 1. Temperature Readings Are Estimated

⚠️ **Critical Limitation**: Accurate temperature on macOS Apple Silicon requires CGO bindings to access SMC (System Management Controller). Current implementation **estimates** temperatures based on CPU usage patterns:

```go
// internal/system/collector.go:165
baseTemp := 35.0 // Base idle temperature for M-series chips
loadTemp := c.info.CPU.UsagePercent * 0.5
c.info.Temperature.CPUPackage = baseTemp + loadTemp
```

For production use, implement:
- CGO bindings to access SMC
- `powermetrics` with sudo (requires elevated privileges)
- Integration with existing macOS frameworks

### 2. Load Averages Not Available

gopsutil does not expose load averages on macOS. Values always show `0.0`:

```go
// internal/system/collector.go:91
// Load averages are not exposed via gopsutil on macOS
c.info.CPU.LoadAvg1 = 0
c.info.CPU.LoadAvg5 = 0
c.info.CPU.LoadAvg15 = 0
```

### 3. Terminal Compatibility

Application requires:
- **Dark background** (warns if light terminal detected)
- **True color** (24-bit) support
- **UTF-8** encoding
- **Mouse events** enabled

Best compatibility: Ghostty > iTerm2 > Terminal.app

### 4. Mouse Coordinate Calculations

UI layout calculations use hard-coded offsets. If modifying header/footer, update Y-coordinate math:

```go
// internal/ui/app.go:571
// Data rows start at Y=8, so row index = msg.Y - 8
cursorIndex := msg.Y - 8
```

### 5. Style Caching for Performance

Widgets cache lipgloss styles to avoid allocations in hot loops:

```go
// internal/widgets/gauge.go:25
var (
    sparklineStyleCache = make(map[string]lipgloss.Style)
    barFilledStyleCache = make(map[string]lipgloss.Style)
)
```

**Do not modify colors at runtime** - cached styles won't update.

### 6. Context Menu Centering

Context menu uses hard-coded dimensions for centering:

```go
// internal/ui/app.go:467
menuWidth, menuHeight := 34, 13
menuX := (m.width - menuWidth) / 2
menuY := (m.height - menuHeight) / 2
```

If modifying menu content, update these constants.

---

## Nord Theme Colors

Defined in `internal/ui/styles.go`:

```
Background:  #2E3440 (Nord0)
Foreground:  #D8DEE9 (Nord4)
Blue:        #88C0D0 (Nord8)
Green:       #A3BE8C (Nord14)
Red:         #BF616A (Nord11)
Yellow:      #EBCB8B (Nord12)
```

Use `TemperatureColor()` helper to colorize temps:
- < 60°C: Green (#A3BE8C)
- 60-85°C: Yellow (#EBCB8B)
- > 85°C: Red (#BF616A)

---

## Development Workflow

### 1. Make Changes

Edit files following existing patterns. Key files:
- UI changes: `internal/ui/app.go`
- Metrics: `internal/system/collector.go`
- New widgets: `internal/widgets/`

### 2. Run Tests

```bash
task test
```

Fix any failures immediately.

### 3. Lint

```bash
task lint
```

Addresses `go vet` warnings. `staticcheck` failures are ignored.

### 4. Build & Test Manually

```bash
task run
```

Verify UI renders correctly and features work.

### 5. Full CI Check

```bash
task all
```

Runs: tidy → lint → test → build:release

---

## Common Tasks for Agents

### Adding a New Metric

1. Add field to appropriate `*Info` struct in `internal/system/types.go`
2. Collect metric in `collector.go` (e.g., `collectCPU()`, `collectMemory()`)
3. Render metric in view (e.g., `renderCPUView()`)
4. Add test in `collector_test.go`

### Adding a New Tab

1. Add constant to `Tab` enum in `internal/ui/app.go`
2. Add tab name to `TabNames` slice
3. Add case to `renderActiveTab()` switch
4. Implement render function (e.g., `renderNewTabView()`)
5. Update keyboard shortcuts (`1-6` keys)

### Modifying Process Killing

1. Update `ProtectedProcessNames` in `internal/system/kill.go`
2. Modify `CheckKillSafety()` validation logic
3. Update confirmation dialog in `renderKillConfirmation()`
4. Test with safe/unsafe processes

### Changing Update Interval

Currently hard-coded to 1 second in multiple places:
- `internal/ui/app.go:207` (tick command)
- `internal/system/collector.go:29` (history size = 60 seconds)

To make configurable:
1. Add field to `Model` struct
2. Add to settings tab
3. Pass to collector via constructor or setter

---

## File-Specific Patterns

### internal/ui/app.go

- **Line 145-165**: Key bindings definition
- **Line 167-184**: Model initialization
- **Line 219-242**: Message dispatch (Update method)
- **Line 461-611**: Mouse handling
- **Line 699-762**: View composition
- **Line 825-1010**: View rendering functions

### internal/system/collector.go

- **Line 27-32**: Collector initialization
- **Line 34-60**: Main collection loop
- **Line 69-117**: CPU collection
- **Line 119-155**: Memory collection
- **Line 157-186**: Temperature (estimated)
- **Line 350-417**: Byte formatting utilities

### internal/widgets/gauge.go

- **Line 51-150**: Sparkline rendering
- **Line 273-335**: Bar gauge rendering
- **Style caches**: Lines 25-48 (performance optimization)

---

## Dependencies

```go
github.com/charmbracelet/bubbles    // TUI components (table, help, spinner)
github.com/charmbracelet/bubbletea  // TUI framework (Elm architecture)
github.com/charmbracelet/lipgloss   // Styling and layout
github.com/shirou/gopsutil/v4       // System metrics
github.com/atotto/clipboard         // Clipboard access
```

---

## Known Limitations

1. **Temperature**: Estimated values only (see Gotcha #1)
2. **Load Averages**: Always 0.0 (see Gotcha #2)
3. **macOS Only**: Uses macOS-specific process attributes
4. **Dark Terminal Required**: Light terminals show warning
5. **Minimum Terminal Size**: 60x20 enforced in code

---

## Troubleshooting

### UI Rendering Issues

Check terminal compatibility:
```bash
echo $TERM  # Should be xterm-256color or similar
```

Ensure dark background, UTF-8, true color support.

### Build Failures

```bash
go mod tidy      # Fix dependency issues
task clean       # Remove stale binaries
task build       # Rebuild
```

### Test Failures

Run with verbose output:
```bash
go test -v ./...
```

Common causes:
- Race conditions (add `go test -race`)
- Timing issues (tests use time-based metrics)
- Permission issues (process killing tests)

---

## Contributing Areas

Improvements welcome in:

1. **Accurate Temperature** - Implement SMC access via CGO
2. **Historical Data** - Add more graph views, export functionality
3. **Alerts** - Configurable thresholds with notifications
4. **Plugins** - Extensible architecture for custom metrics
5. **Cross-platform** - Support Linux/Windows with appropriate backends

---

## Additional Resources

- **Charm Documentation**: https://charm.sh/docs
- **Bubble Tea Tutorial**: https://github.com/charmbracelet/bubbletea#tutorial
- **gopsutil Docs**: https://pkg.go.dev/github.com/shirou/gopsutil/v4
- **Nord Theme**: https://www.nordtheme.com/docs/colors-and-palettes

---

*Last updated: Based on codebase analysis*  
*Generated for AI agents working in this repository*
