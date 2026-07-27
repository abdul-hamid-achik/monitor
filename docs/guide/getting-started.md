# Getting Started

Monitor gives you the same local system data through an interactive terminal
UI, a JSON CLI, and an MCP server for AI agents. This tour gets all three
surfaces running in about two minutes.

## Install Monitor

On macOS or Linux with Homebrew, install the project cask:

```bash
brew install --cask abdul-hamid-achik/tap/monitor
```

For release archives, upgrade instructions, or a source build, see
[Installation](/guide/installation). The rest of this guide assumes `monitor`
is on your `PATH`.

## First run

Check the installed version and capture a compact system snapshot:

```bash
monitor --version
monitor snapshot --compact
```

Then launch Studio, Monitor's interactive TUI:

```bash
monitor studio
```

Running bare `monitor` prints command help. `monitor tui` is an alias for
`monitor studio`.

Studio opens on the **Overview** tab. Use these keys to move around:

| Key | Action |
|---|---|
| `1`–`9` | Jump directly to a tab |
| `→` / `Tab` / `l` | Next tab |
| `←` / `Shift+Tab` / `h` | Previous tab |
| `/` | Search processes |
| `?` | Open context-aware help |
| `q` / `Ctrl+C` | Quit |

The nine tabs cover Overview, CPU, Memory, Temperature, Disk, Network,
Processes, Settings, and Trends. See [The TUI](/guide/tui) for process actions,
diagnostics, and the complete keyboard reference.

## A two-minute tour

### 1. Watch the TUI

```bash
monitor studio
```

Open the CPU and Memory tabs, then visit Processes and press `/` to filter the
table. Press `Enter` on a process to inspect its pinned diagnostics.

### 2. Query the CLI

After leaving Studio, request the same metrics as machine-readable output:

```bash
monitor snapshot --json | jq '.cpu'
monitor process 1234 --json
monitor doctor --json
```

Replace `1234` with a live PID. `jq` is optional; omit the pipe to see the full
snapshot. To stream newline-delimited metric events until you press `Ctrl+C`,
run:

```bash
monitor watch --json
```

See the [CLI Reference](/guide/cli) for snapshots, history, baselines, anomaly
analysis, profiling, logs, grouped issues, and safety-checked process
termination.

To create a local issue with diagnostic evidence, investigate a live PID and
then inspect the grouped result:

```bash
monitor investigate 1234 --codebase "$PWD" --json
monitor issues list --status open
```

The seven investigation steps identify the runtime, snapshot the system,
capture a profile, correlate code with codemap and vecgrep, save evidence with
file.cheap when available, and persist the occurrence. See
[Local Issues](/guide/issues) for the Run/Event/Issue/Evidence model.

### 3. Start the MCP server

```bash
monitor mcp serve
```

The stdio server exposes six read-only tools and four mutating tools. Every
mutating call requires `confirm: true`, while protected and system processes
remain non-terminable. See [MCP Server](/guide/mcp) for client configuration and
the full tool schemas.

## Platform notes

The core TUI and CLI metrics work on macOS and Linux, with transparent
differences where the operating systems expose different data:

- **macOS:** `sample` profiling is available. Apple Silicon temperature can
  come from `powermetrics` when cached `sudo` credentials are available;
  otherwise Monitor uses an estimate. The macOS collector does not expose load
  averages.
- **Linux:** load averages and cgroup v2 detection are available. Temperature
  is estimated, and the macOS `sample` profiler is unavailable. `pprof`
  profiling still works for Go processes that expose an endpoint.

Studio marks temperature readings `real` or `est`, and JSON output includes the
temperature source.

## Where to go next

- [Installation](/guide/installation) — upgrade, uninstall, archives, and
  source builds.
- [The TUI](/guide/tui) — tabs, keyboard shortcuts, and mouse navigation.
- [CLI Reference](/guide/cli) — the full JSON command surface.
- [MCP Server](/guide/mcp) — the agent-facing tool surface.
- [Local Issues](/guide/issues) — recurring observations, lifecycle, and evidence.
- [Anomaly Detection](/guide/anomaly-detection) — alert rules and thresholds.
- [Ecosystem Integration](/guide/ecosystem) — optional local tool integrations.
- [Process Safety](/guide/safety) — termination policy across every surface.
