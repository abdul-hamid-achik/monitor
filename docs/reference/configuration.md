# Configuration

Monitor reads its settings from a single JSON file:

```
~/.config/monitor/config.json
```

The file is **optional**. If it does not exist — or if it contains invalid
JSON — Monitor falls back to the built-in defaults rather than failing. The
directory (`~/.config/monitor`) is created automatically when settings are
saved.

Settings drive the TUI (update cadence, process limits, mouse, alert
thresholds). `monitor processes` also uses `max_processes` and
`show_system_processes` as its defaults; explicit CLI flags still win.

## Manage settings from the CLI

The `config` command avoids hand-editing duration values and validates every
change before atomically replacing the file:

```bash
monitor config show
monitor config get update-interval
monitor config set update-interval 500ms
monitor config set cpu-alert-threshold 85
monitor config path
monitor config reset --yes
```

Every subcommand also supports `--json`. Keys accept either underscores or
hyphens. `config set` accepts Go-style durations such as `250ms`, `2s`, and
`1m`, then stores them in the existing nanosecond representation.

## Example

A config file with every field set to its default:

```json
{
  "update_interval": 1000000000,
  "temperature_unit": "C",
  "show_system_processes": false,
  "max_processes": 50,
  "mouse_enabled": true,
  "cpu_alert_threshold": 0,
  "memory_alert_threshold": 0
}
```

You only need to include the fields you want to override — any field you omit
keeps its default.

## Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `update_interval` | duration (nanoseconds) | `1000000000` (1s) | How often the collector refreshes metrics. Encoded as an integer number of nanoseconds (Go `time.Duration`). Values `<= 0` are ignored; full process collection clamps positive values below 100ms to 100ms. |
| `temperature_unit` | string | `"C"` | Temperature display unit (`C` or `F`, case-insensitive). Unknown values are ignored and the default is kept. |
| `show_system_processes` | bool | `false` | Whether system/root-owned processes are shown in the Processes tab. |
| `max_processes` | int | `50` | Maximum number of processes listed. Only accepted in the range `1`–`1000`; out-of-range values are ignored and the default is kept. |
| `mouse_enabled` | bool | `true` | Whether mouse interaction (clicking tabs, selecting rows, scrolling) is enabled in the TUI. |
| `cpu_alert_threshold` | float | `0` | CPU-usage percentage that triggers an alert. `0` disables the alert. Values outside `0`–`100` are ignored. |
| `memory_alert_threshold` | float | `0` | Memory-usage percentage that triggers an alert. `0` disables the alert. Values outside `0`–`100` are ignored. |

::: tip update_interval is in nanoseconds
Because `update_interval` is a Go `time.Duration`, it is stored as an integer
number of **nanoseconds**, not a string. For example, `500000000` is 500ms and
`2000000000` is 2 seconds.
:::

## How the file is written

When Monitor saves settings it writes them **atomically**: the JSON is first
written to a temporary file in the same directory, then `rename`d into place.
`rename` is atomic, so a crash or a full disk mid-write cannot leave a
truncated `config.json` that would otherwise be silently discarded in favour of
the defaults. The resulting file is written with mode `0644`.

## Invalid or missing config

Monitor is deliberately forgiving here:

- **Missing file** → defaults are used.
- **Unreadable / malformed JSON** → defaults are used (the file is not
  rewritten or deleted).
- **Out-of-range field** (e.g. `max_processes` of `0` or `5000`, a threshold
  outside `0`–`100`, an unknown temperature unit, or a non-positive
  `update_interval`) → that single field falls back to its default; the rest
  of the file is still applied.

## Temperature source flag

Separate from the config file, a persistent CLI flag controls how temperature
is read. It is a persistent flag, so it is available on **every** subcommand,
including the TUI (`monitor studio`):

```bash
monitor studio --no-temperature-source
# or for a one-shot:
monitor snapshot --no-temperature-source
```

By default Monitor wires a `powermetrics`-backed temperature source so readings
come from real SMC sensors when sudo credentials are available, falling back to
a CPU-load estimate otherwise. Passing `--no-temperature-source` skips the
`powermetrics` subprocess entirely and always uses the estimation heuristic —
useful on systems where sudo can't be obtained, with no recompile required.

## Environment variables

`monitor run` sets two environment variables in the glyphrun child so the spec
and the processes it launches can detect the observed run:

| Variable | Set by | Meaning |
|----------|--------|---------|
| `MONITOR` | `monitor run` | Always set to `1` in the glyphrun child, mirroring glyphrun's `GLYPHRUN` pattern. |
| `MONITOR_RUN_DIR` | `monitor run` | Reuses a non-empty inherited value; otherwise points to a temporary directory that Monitor removes after glyphrun exits. |
| `MONITOR_LOG_STORE` | `monitor logs capture/search` | Shared override for the log database path. A command-level `--store` flag takes precedence; the default is `~/.local/share/monitor/logs.veclite`. |
| `MONITOR_ISSUES_STORE` | issue CLI, investigate, watch, MCP | Shared override for the issue database path. `issues --store` takes precedence for CLI reads/mutations; the default follows `$XDG_DATA_HOME/monitor/issues.veclite` or `~/.local/share/monitor/issues.veclite`. |

A glyphrun child can check `MONITOR=1` (or the presence of `MONITOR_RUN_DIR`)
to alter its behaviour — for example to emit extra diagnostics — while being
monitored.

### Diagnostic run context

`investigate` and `watch --stash` also read optional correlation fields. An
explicit investigate flag wins over `MONITOR_*`, which wins over Chalupa CI and
legacy aliases.

| Field | Preferred environment variables |
|-------|---------------------------------|
| environment | `MONITOR_ENVIRONMENT`, `CHALUPA_CI_ENVIRONMENT`, `CHALUPA_ENV`, `CHALUPA_ENVIRONMENT` |
| deployment | `MONITOR_DEPLOYMENT_ID`, `CHALUPA_DEPLOYMENT_ID` |
| run | `MONITOR_RUN_ID`, `CHALUPA_CI_RUN_ID`, `CHALUPA_RUN_ID`, basename of `MONITOR_RUN_DIR` |
| step | `MONITOR_STEP_ID`, `CHALUPA_CI_STEP_ID` |
| suite | `MONITOR_SUITE`, `CHALUPA_CI_SUITE` |
| attempt | `MONITOR_ATTEMPT`, `CHALUPA_CI_ATTEMPT` |
| release | `MONITOR_RELEASE`, `CHALUPA_RELEASE` |
| service | `MONITOR_SERVICE`, `CHALUPA_SERVICE` |
| Git SHA | `MONITOR_GIT_SHA`, `GIT_SHA`, `GITHUB_SHA` |

These values belong to the local diagnostic plane and occurrence context. They
are never added to `monitor.telemetry_window` V1.
