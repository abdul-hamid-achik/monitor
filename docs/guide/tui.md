# The TUI

`monitor studio` launches the interactive TUI — a Bubble Tea v2 application
with a Nord theme, nine tabs, and full keyboard and mouse navigation. (Running
bare `monitor` prints help instead.)

```bash
./bin/monitor studio   # launches the TUI (9 tabs); `monitor tui` also works
```

The same data is available without a terminal via the [CLI](./cli.md) and the
[MCP server](./mcp.md); the TUI is just the human-facing surface.

## The nine tabs

The tab bar runs across the top of the screen. Tabs are numbered `1`–`9` and
always appear in this order:

| # | Tab | What it shows |
|---|-----|---------------|
| 1 | **Overview** | CPU and memory gauges side by side, plus a network panel (per-second and total throughput) |
| 2 | **CPU** | CPU history, a responsive per-core grid, frequency, load averages, and core/thread counts |
| 3 | **Memory** | Memory usage and swap |
| 4 | **Temperature** | CPU package, CPU cores, GPU, ANE, and battery sensor readings, plus fan telemetry when available |
| 5 | **Disk** | Disk usage and I/O |
| 6 | **Network** | Network throughput |
| 7 | **Processes** | A sortable, searchable, selectable process table |
| 8 | **Settings** | The current configuration (editable in the TUI) |
| 9 | **Trends** | Sparklines and summary stats over the persistent history captured by `monitor history record` |

The header adapts from full tab names to abbreviations and finally the active
tab on narrow terminals. The active tab uses both color and a `▸` marker. The
status bar remains pinned to the bottom and reports `LIVE`, `PAUSED`, `WAITING`,
or `STALE` alongside CPU, memory, sample time, and context-aware shortcuts.

### CPU core grid and metric availability

The CPU tab lays out per-core gauges in one to four columns based on terminal
width. It uses the available height as a row budget, so common Apple Silicon
core counts appear without the old eight-core cutoff. If the terminal cannot
display every core, the panel shows `+N cores hidden` and prompts you to
enlarge the terminal instead of silently omitting them.

CPU history, per-core counters, CPU information, and load averages each expose
their collection state. When a metric is unsupported or temporarily
unavailable, Studio shows the collector's reason instead of an ambiguous
numeric zero. When collection is healthy but has not produced a sample yet,
the panel tells you to wait for or request the next refresh with `r`.

### Temperature badges

Each temperature reading is badged `● real` when it comes from real SMC
sensors (via `powermetrics` on macOS) or `● est` when it falls back to a
CPU-load estimate. Fan telemetry is unavailable on Apple Silicon (the SMC
keys are restricted), in which case the tab says so.

### Settings tab

The Settings tab is **editable** in the TUI. It lists the update interval,
temperature unit, whether system processes are shown, the maximum process
count, mouse enablement, and the CPU/memory alert thresholds. A `▸` marks the
selected row:

| Key | Action |
|-----|--------|
| `↑` / `↓` (or `k` / `j`) | Select a setting row |
| `Enter` / `Space` | Cycle the selected value forward |
| `-` (or `_`) | Cycle the selected value back |
| `s` | Save the current settings to the config file |

Because `h`/`l` and the arrow keys `←`/`→` remain bound to tab navigation,
row selection uses `↑`/`↓` (or `k`/`j`) and value editing uses `Enter`/`Space`/`-`. Changes
are held in memory until you press `s`, which writes them to the
[config file](/reference/configuration) at `~/.config/monitor/config.json`.
Studio shows saved, unsaved, and error feedback inline. Changing Update
Interval immediately resets both the repaint timer and the running collector.

## Keyboard shortcuts

### Global navigation

| Key | Action |
|-----|--------|
| `q` / `Ctrl+C` / `Esc` | Quit |
| `Tab` / `→` / `l` | Next tab |
| `Shift+Tab` / `←` / `h` | Previous tab |
| `1`–`9` | Jump directly to a tab |
| `?` | Open context-aware keyboard help (`?`, `Esc`, or `q` closes it) |
| `p` | Pause/resume applying live snapshots |
| `r` | Refresh from the latest snapshot now |

Tab navigation wraps around in both directions.

### Processes tab

These keys are active only on the Processes tab (tab `7`):

| Key | Action |
|-----|--------|
| `Enter` | Open diagnostics for the highlighted process |
| `/` | Edit the process filter |
| `Space` | Toggle selection of the highlighted process |
| `Ctrl+A` | Select all (filtered) processes |
| `Ctrl+D` | Clear the selection |
| `c` | Sort by CPU (press again to reverse) |
| `m` | Sort by memory (press again to reverse) |
| `k` | Terminate (SIGTERM) the selection, with confirmation |
| `x` | Force-kill (SIGKILL) the selection, with confirmation |

The table itself responds to the usual arrow keys for moving the highlight up
and down. Its columns adapt to terminal width, preserving PID, name, and CPU on
narrow screens and progressively adding memory, threads, I/O, and user details.

## Inspecting process diagnostics

Highlight a process and press `Enter` to open its read-only diagnostics panel.
The panel remains pinned to that PID while live samples and table sorting
continue. It shows:

- PID, name, owner, status, and parent PID
- CPU, resident memory (RSS), memory share, read/write I/O, and thread count
- Whether the process is a user, system, or protected process, including its
  termination policy

Metric values retain their availability state. For example, macOS reports that
per-process I/O counters are unsupported instead of displaying an ambiguous
`0 B`; temporary permission or collection failures include their reason. If
the pinned PID exits between samples, the panel says that the process is no
longer present and offers a refresh instead of switching to whichever process
now occupies the table row.

The diagnostics panel is modal: process selection, sorting, navigation, and
termination shortcuts do not reach the table underneath it.

| Key | Action in process diagnostics |
|-----|-------------------------------|
| `Enter` / `Esc` / `q` | Close the diagnostics panel without quitting Studio |
| `r` | Refresh the latest snapshot for the pinned PID |
| `?` | Open context-aware help; close help to return to diagnostics |
| `Ctrl+C` | Quit Studio |

## Searching processes

Press `/` on the Processes tab to edit the filter. While the prompt is active,
**every keystroke is search input** — typing `q`, `l`, a digit, etc. edits the
query instead of quitting or switching tabs. Matching is case-insensitive
across process name, PID, and user, and the visible match count is shown.

- `Backspace` deletes the last character.
- `Ctrl+U` clears the query.
- `Enter` applies the filter and closes the prompt.
- `Esc` cancels editing and restores the prior filter.

If nothing matches, the table shows `No processes match the current filter`.

## Selecting and killing processes

You can act on either a single highlighted row or a multi-row selection:

1. **Select** rows with `Space` (toggling each one), `Ctrl+A` (select all
   filtered rows), or `Ctrl+D` (clear). Selected rows are marked with a `▸`
   and the Processes panel heading shows the count, e.g.
   `Processes - 3 selected │ k:kill x:force-kill`.
2. **Kill** with `k` (SIGTERM) or `x` (SIGKILL). If you haven't explicitly
   selected anything, the currently highlighted row is used.
3. A **confirmation dialog** appears, listing each target PID and a safety
   label: `✓ OK`, `⚠️ CAUTION` for system processes, or `🛓 CRITICAL` for
   protected ones. Force-kill adds a warning that the process won't be allowed
   to clean up.
4. Press `y` to confirm or `n` / `Esc` to cancel. Verification runs
   asynchronously so Studio stays responsive, then reports terminated,
   still-running, failed, and spared counts in the status bar.

Termination goes through the same shared safety check used by the CLI and MCP
server: **protected processes are never killed**, even if you confirm. See
[Safety](./safety) for the full protected/system process rules.

## Mouse

Mouse support is controlled by the **Mouse Enabled** setting (`mouse_enabled`
in the [config](/reference/configuration)), which is on by default. You can
toggle it from the [Settings tab](#settings-tab) or the config file. When it is
on:

- **Clicking a tab** in the header row switches directly to that tab.
- **Scrolling the wheel** on the Processes tab scrolls the process table.

When `mouse_enabled` is off, the TUI is keyboard-only.
