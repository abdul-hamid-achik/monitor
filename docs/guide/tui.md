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
| 2 | **CPU** | CPU usage, frequency, core and thread counts |
| 3 | **Memory** | Memory usage and swap |
| 4 | **Temperature** | CPU package, CPU cores, GPU, ANE, and battery sensor readings, plus fan telemetry when available |
| 5 | **Disk** | Disk usage and I/O |
| 6 | **Network** | Network throughput |
| 7 | **Processes** | A sortable, searchable, selectable process table |
| 8 | **Settings** | The current configuration (editable in the TUI) |
| 9 | **Trends** | Sparklines and summary stats over the persistent history captured by `monitor history record` |

The active tab is highlighted, and a status bar across the bottom shows live
CPU and memory percentages, the last update time, and a reminder of the tab
and `q` quit shortcuts.

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
[config file](/reference/configuration) at `~/.config/monitor/config.json`; a
`✓ saved` marker confirms the write.

## Keyboard shortcuts

### Global navigation

| Key | Action |
|-----|--------|
| `q` / `Ctrl+C` / `Esc` | Quit |
| `Tab` / `→` / `l` | Next tab |
| `Shift+Tab` / `←` / `h` | Previous tab |
| `1`–`9` | Jump directly to a tab |

Tab navigation wraps around in both directions.

### Processes tab

These keys are active only on the Processes tab (tab `7`):

| Key | Action |
|-----|--------|
| `/` | Toggle the process search prompt |
| `Space` | Toggle selection of the highlighted process |
| `Ctrl+A` | Select all (filtered) processes |
| `Ctrl+D` | Clear the selection |
| `c` | Sort by CPU (press again to reverse) |
| `m` | Sort by memory (press again to reverse) |
| `k` | Terminate (SIGTERM) the selection, with confirmation |
| `x` | Force-kill (SIGKILL) the selection, with confirmation |

The table itself responds to the usual arrow keys for moving the highlight up
and down the rows.

## Searching processes

Press `/` on the Processes tab to open the search prompt. While the prompt is
active, **every keystroke is search input** — typing `q`, `l`, a digit, etc.
edits the query instead of quitting or switching tabs. The table filters live
as you type (a case-insensitive substring match against the process name).

- `Backspace` deletes the last character.
- `Esc` clears the query and closes the prompt.
- `/` again also toggles the prompt off and clears the query.

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
4. Press `y` to confirm or `n` / `Esc` to cancel.

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
