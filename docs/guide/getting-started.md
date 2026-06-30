# Getting Started

Monitor is a terminal-based, **agent-harnessable** system monitor for macOS and
Linux, built in Go with the Charm ecosystem (Bubble Tea v2) and a Nord theme.

It offers an interactive TUI (`monitor studio`) and exposes the same data to
scripts, agents, and other tools via JSON CLI commands and an MCP stdio server.
Running bare `monitor` prints help.

## Prerequisites

- **Go 1.25 or higher** — required to build from source.
- **A platform:**
  - **macOS (Apple Silicon)** — full feature set, including real SMC
    temperature readings via `powermetrics`.
  - **Linux** — core metrics (CPU, memory, network, disk, processes).
    Temperature always falls back to a CPU-load estimate.
- **[Task](https://taskfile.dev/)** — optional task runner. Every `task`
  command below has a plain `go` equivalent, so Task is never strictly
  required.

## Build from source

Clone the repository, then build the binary:

```bash
go mod tidy

# Build to bin/monitor
go build -o bin/monitor ./cmd/monitor
```

If you have Task installed, the one-word equivalent is:

```bash
task build
```

For an optimized (stripped) release binary:

```bash
go build -ldflags="-s -w" -o bin/monitor ./cmd/monitor
# or: task release
```

Optionally install it onto your `PATH`:

```bash
sudo cp bin/monitor /usr/local/bin/
# or: task install
```

## First run

Launch the interactive TUI with the `studio` subcommand (running bare
`monitor` prints help instead):

```bash
./bin/monitor studio   # `monitor tui` is an alias
```

You will land on the **Overview** tab. The TUI ships nine tabs — Overview,
CPU, Memory, Temperature, Disk, Network, Processes, Settings, and Trends —
with full keyboard and mouse navigation.

A few keys to get moving:

| Key | Action |
|-----|--------|
| `1`–`9` | Jump to a tab |
| `→` / `Tab` / `l` | Next tab |
| `←` / `Shift+Tab` / `h` | Previous tab |
| `/` | Search processes |
| `q` / `Ctrl+C` | Quit |

If you prefer the task runner, `task run` builds and launches the TUI in one
step.

## A 2-minute tour

Monitor has three surfaces over the same underlying metrics. Pick the one that
matches how you want to use it:

### 1. The TUI

A live, themed dashboard with sortable processes, multi-select, and
safety-checked termination. Launch it with the `studio` subcommand:

```bash
./bin/monitor studio
```

→ See [The TUI](/guide/tui) for the full tab and keyboard reference.

### 2. The CLI (JSON for scripts and agents)

Every subcommand supports `--json` for machine-readable output:

```bash
./bin/monitor snapshot --json | jq '.cpu'   # one-shot system snapshot
./bin/monitor watch --json                   # stream NDJSON metric events
./bin/monitor process 1234 --json            # detailed process info
./bin/monitor doctor --json                  # ecosystem tool availability
```

→ See the [CLI Reference](/guide/cli) for every subcommand and flag.

### 3. The MCP server (for AI agents)

Speak MCP over stdio to expose Monitor's data and tools to an agent:

```bash
./bin/monitor mcp serve
```

This exposes seven tools — three read-only and four mutating — with the
mutating tools gated on `confirm: true`.

→ See [MCP Server](/guide/mcp) for the tool list and confirmation model.

## Where to go next

- [The TUI](/guide/tui) — tabs, keyboard shortcuts, and mouse navigation.
- [CLI Reference](/guide/cli) — the full JSON command surface.
- [MCP Server](/guide/mcp) — the agent-facing tool surface.
- [Anomaly Detection](/guide/anomaly-detection) — the rules behind `watch`
  alerts, and how to configure thresholds.
- [Ecosystem Integration](/guide/ecosystem) — fcheap, tinyvault, glyphrun,
  codemap, and friends.
