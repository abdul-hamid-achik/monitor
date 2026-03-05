# Monitor

A beautiful terminal-based system monitor for macOS inspired by Activity Monitor, built with Go using the Charm ecosystem and Nord theme.

![License](https://img.shields.io/badge/license-MIT-blue.svg)
![Go](https://img.shields.io/badge/go-1.21+-blue.svg)
![Platform](https://img.shields.io/badge/platform-macOS%20Apple%20Silicon-blue)

## Features

- 📊 **Real-time System Monitoring** - CPU, Memory, Temperature, Network, and Processes
- 🎨 **Nord Theme** - Beautiful dark theme optimized for long viewing sessions
- 🖱️ **Enhanced Mouse Support** - Click tabs, select processes, scroll with wheel
- ⌨️ **Keyboard Navigation** - Full keyboard control with shortcuts
- 📈 **Interactive Graphs** - Sparkline charts showing historical data
- 🔍 **Process Filtering** - Filter and sort processes by various criteria
- ⚠️ **Safe Process Killing** - Protected system processes with confirmation dialogs
- 📑 **Tab Navigation** - 5 different views (Overview, CPU, Memory, Processes, Temperature)
- 📺 **Full Screen Layout** - Responsive design that adapts to terminal size
- 🔄 **Auto-Resize** - Automatically adjusts when terminal window changes

## Screenshots

```
╭──────────────────────────────────────────────────────────────────────────────╮
│  MONITOR v1.0    Overview  CPU  Memory  Processes  Temperature  Settings     │
│  MacBook Pro · 14:30:45                                                     │
╰──────────────────────────────────────────────────────────────────────────────╯

╭─ CPU ─────────────────────────────╮ ╭─ Memory ─────────────────────────────╮
│  ▓▓▓▓▓▓▓▓▓░░░░░░░░░  42%          │ │  ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓░░░  78%             │
│  4.2 GHz │ 12 cores │ 8 threads   │ │  12.4GB / 16GB │ 3.2GB swap          │
╰────────────────────────────────────╯ ╰─────────────────────────────────────╯

╭─ Temperature ──────────────────────╮ ╭─ Network ────────────────────────────╮
│  CPU: 52°C │ GPU: 48°C │ ANE: 45°C │ │  ↓ 2.4 MB/s  ↑ 856 KB/s             │
│  Fan: 2400 RPM (Auto)               │ │  Total: ↓ 1.2 GB │ ↑ 456 MB         │
╰────────────────────────────────────╯ ╰─────────────────────────────────────╯

╭─ Top Processes ──────────────────────────────────────────────────────────────╮
│  PID    Name                      CPU%    Memory     Threads   User         │
│  1234   Chrome                   15.2    2.4 GB       48      abdulachik    │
│  5678   Code                     12.8    1.8 GB       32      abdulachik    │
╰──────────────────────────────────────────────────────────────────────────────╯

Processes: 342  │  CPU: 42.0%  │  Memory: 78.0%  │  Last Update: 14:30:45
```

## Installation

### Prerequisites

- Go 1.21 or higher
- macOS (optimized for Apple Silicon M1/M2/M3/M5)
- [Task](https://taskfile.dev/) - Task runner (optional but recommended)

### Build from Source

```bash
# Clone the repository
cd /path/to/monitor

# Install dependencies
go mod tidy

# Build using Task (recommended)
task build

# Or build manually
go build -o bin/monitor ./cmd/monitor

# Optionally install system-wide
task install
# Or manually: sudo cp bin/monitor /usr/local/bin/
```

### Quick Start

```bash
# Run using Task
task run

# Or run directly
./bin/monitor

# Or if installed system-wide
monitor
```

### Ghostty Terminal

This application works perfectly with [Ghostty](https://ghostty.org/) terminal!
Just run `./bin/monitor` in your Ghostty terminal.

## Usage

### Keyboard Shortcuts

| Key | Action |
|-----|--------|
| `q` / `Ctrl+C` | Quit application |
| `→` / `Tab` / `l` | Next tab |
| `←` / `Shift+Tab` / `h` | Previous tab |
| `1-6` | Switch to specific tab (1=Overview, 2=CPU, 3=Memory, 4=Processes, 5=Temperature, 6=Settings) |
| `r` | Refresh data |
| `?` | Toggle help |
| `/` | Filter processes |
| `k` | Kill selected process |
| `Space` | Select process (multi-select) |
| `↑` / `k` | Move up |
| `↓` / `j` | Move down |
| `Page Up` / `b` | Page up |
| `Page Down` / `f` | Page down |
| `Home` / `g` | Go to top |
| `End` / `G` | Go to bottom |

#### Settings Tab (Tab 6)

| Key | Action |
|-----|--------|
| `↑` / `↓` or `j/k` | Navigate settings |
| `←` / `→` or `Enter` | Change setting value |
| `1-4` | Quick select setting |
| `r` | Reset to defaults |

### Mouse Controls

- **Click tabs** to switch views
- **Click process rows** to select
- **Scroll wheel** to navigate lists
- **Click buttons** for actions

### Views

#### 1. Overview (Tab 1)
Dashboard showing CPU, Memory, Temperature, Network, and Top Processes at a glance.

#### 2. CPU (Tab 2)
Detailed CPU monitoring with:
- Real-time usage history graph
- Per-core breakdown
- CPU statistics (frequency, cores, threads, load average)

#### 3. Memory (Tab 3)
Memory monitoring with:
- Physical memory usage bar
- Swap usage
- Memory pressure indicator

#### 4. Processes (Tab 4)
Full process list with:
- Sortable columns
- Filtering support
- Multi-select for batch operations
- Safe process termination

#### 5. Temperature (Tab 5)
Temperature monitoring with:
- Sensor readings (CPU, GPU, ANE, Battery)
- Temperature history graph
- Fan speed and control

#### 6. Settings (Tab 6)
Customize the application behavior:
- **Update Interval**: How often to refresh data (500ms, 1s, 2s, 5s)
- **Temperature Unit**: Display temperatures in Celsius or Fahrenheit
- **Show System Procs**: Toggle visibility of system processes
- **Max Processes**: Limit the number of processes shown (20, 50, 100, 200)

Settings are automatically saved to `~/.config/monitor/config.json` after each change.

## Safety Features

### Process Killing Protection

Monitor includes multiple safety layers to prevent accidental system damage:

1. **Protected Processes** - Critical system processes (launchd, kernel_task, etc.) cannot be killed
2. **System Process Warning** - Root-owned processes show caution warnings
3. **Confirmation Dialog** - Requires explicit confirmation before killing
4. **Graceful Termination** - Uses SIGTERM first, escalates to SIGKILL only if needed
5. **Visual Warnings** - Color-coded safety levels (Green=Safe, Yellow=Caution, Red=Critical)

### Protected Process List

The following processes are protected from termination:
- `launchd` (PID 1)
- `kernel_task`
- `init`
- `System`
- `loginwindow`
- `WindowServer`
- `Finder`
- `Dock`

## Architecture

```
monitor/
├── cmd/
│   └── monitor/
│       └── main.go              # Entry point
├── internal/
│   ├── ui/
│   │   ├── app.go               # Bubble Tea application
│   │   ├── styles.go            # Nord theme styles
│   │   └── ...                  # View renderers
│   ├── system/
│   │   ├── collector.go         # System data collection
│   │   ├── types.go             # Data structures
│   │   └── kill.go              # Safe process termination
│   └── widgets/
│       ├── gauge.go             # Custom gauge widgets
│       └── ...                  # Sparklines, graphs
├── go.mod
├── go.sum
├── bin/
│   └── monitor                  # Compiled binary
├── Taskfile.yml                 # Task definitions
└── README.md
```

## Technologies

- **Bubble Tea** - Terminal UI framework (Elm architecture)
- **Bubbles** - TUI components (table, list, viewport, help)
- **Lip Gloss** - Styling and layout
- **gopsutil** - System information gathering
- **Nord Theme** - Color palette optimized for dark terminals

## Nord Theme Colors

```
Background:  #2E3440 (Polar Night)
Text:        #D8DEE9 (Snow Storm)
Accents:     #88C0D0 (Blue), #A3BE8C (Green), #BF616A (Red)
```

## Limitations

### Temperature Monitoring

Accurate temperature readings on macOS Apple Silicon require access to the System Management Controller (SMC) via CGO/Objective-C bindings. The current implementation provides **estimated temperatures** based on CPU usage patterns.

For production use with accurate temperatures, consider:
- Adding CGO bindings to access SMC
- Using `powermetrics` with sudo (requires elevated privileges)
- Integrating with existing macOS frameworks

### Load Averages

Load averages are not exposed via gopsutil on macOS. Currently shows 0.0.

## Performance

- **Update Interval**: 1 second (configurable)
- **Memory Usage**: ~20-30 MB
- **CPU Overhead**: < 1%

## Troubleshooting

### Terminal Compatibility

Works best with:
- **Ghostty** ⭐ (Primary supported terminal)
- iTerm2
- Terminal.app
- Kitty
- Alacritty

Ensure your terminal supports:
- True color (24-bit)
- Unicode characters
- Mouse events

### Display Issues

If the UI appears broken:
1. Ensure terminal has dark background
2. Check terminal supports UTF-8
3. Try resizing terminal window
4. Press `Ctrl+L` to refresh

## Contributing

Contributions are welcome! Areas for improvement:

1. **Accurate Temperature** - Implement SMC access via CGO
2. **More Graphs** - Add historical views for all metrics
3. **Custom Alerts** - Configurable thresholds with notifications
4. **Export Data** - CSV/JSON export of metrics
5. **Plugins** - Extensible architecture for custom metrics

## License

MIT License - See LICENSE file for details.

## Acknowledgments

- [Charm](https://charm.sh/) for the amazing TUI libraries
- [gopsutil](https://github.com/shirou/gopsutil) for system metrics
- [Nord Theme](https://www.nordtheme.com/) for the beautiful color palette
- [mactop](https://github.com/context-labs/mactop) for inspiration

---

**Built with ❤️ for macOS Apple Silicon**
