# Anomaly Detection

Monitor ships a small, built-in analyzer (`internal/analyzer`) that watches the
same metric stream the TUI and CLI render, and raises alerts when something
looks wrong. You don't configure it to exist — it runs on every
[`watch`](./cli#watch) tick — you only configure which thresholds matter to you.

## Where alerts come from

The collector publishes one `Event` per tick. The analyzer observes each event
and returns zero or more `collector.Alert` objects. `monitor watch` is the
surface that streams them: every alert becomes an `alert` line in the NDJSON
stream, and the same alert can fan out to a desktop notification, a webhook, and
an incident stash.

```
collector (tick) ──► analyzer ──► alert(s)
                                   │
        ┌──────────────────────────┼──────────────────────────┐
        ▼              ▼            ▼                          ▼
   NDJSON line     --notify     --webhook POST          --stash (fcheap)
```

Every alert carries the same shape regardless of which rule fired:

```json
{
  "rule": "cpu_spike",
  "severity": "warning",
  "pid": 1133,
  "process": "node",
  "detail": "cpu spike"
}
```

`pid` and `process` are present only on per-process rules; system-wide rules
omit them.

## The rules

Four rules always run, and two more light up when you configure them.

### Always-on rules

| Rule | What it detects | Default threshold |
|------|-----------------|-------------------|
| `cpu_spike` | A single process whose CPU exceeds 3× a fixed 50% baseline (≈150% CPU). | factor `3.0` |
| `rss_growth` | A process whose resident memory is climbing steadily — a linear regression over its RSS history with slope > ~50 KB/sample **and** R² > 0.7. | `50_000` bytes/sample |
| `disk_fill` | Any mounted partition whose usage is at or above the threshold. | `90%` |
| `swap_pressure` | Swap usage at or above a fraction of swap total — real memory pressure, not just high RAM use. | `50%` of swap total |

`cpu_spike` and `rss_growth` are per-process (the alert carries the PID);
`disk_fill` fires once per offending partition; `swap_pressure` and the
threshold rules are system-wide.

### Configurable threshold rules

The `cpu_alert_threshold` and `memory_alert_threshold` settings in
[`config.json`](/reference/configuration) are dormant until you set them above
`0`. When set, a `ThresholdRule` fires:

- `cpu_threshold` — overall CPU usage `>=` the configured percentage.
- `mem_threshold` — overall memory usage `>=` the configured percentage.

A threshold of `0` disables that check (the default). Set them in the
[Settings tab](./tui#settings-tab) or by editing the config file directly:

```json
{
  "cpu_alert_threshold": 90,
  "memory_alert_threshold": 85
}
```

These are percentage-point overall-usage alerts, distinct from the per-process
`cpu_spike` / `rss_growth` rules.

## Severity

Every alert the current rules emit is `severity: "warning"`. The field is part
of the alert contract so downstream sinks (webhooks, notifications, stashes) can
triage consistently as new severities are added.

## Alert sinks

Alerts never block the watch loop — each sink runs in its own goroutine. See
[`watch`](./cli#watch) for the full flag reference; in summary:

| Sink | Flag | Behavior |
|------|------|----------|
| NDJSON stream | `--json` | Always on with `--json`; one `alert` line per alert. |
| Desktop notification | `--notify` | `osascript` (macOS) / `notify-send` (Linux); failures log to stderr. |
| Webhook | `--webhook <url>` | POSTs the raw `collector.Alert` JSON; non-2xx logged to stderr. |
| Incident stash | `--stash` | Captures an incident bundle to fcheap in the background; emits a `stash` line with the outcome. |

Sinks are additive: `--notify`, `--webhook`, and `--stash` all fire alongside
the NDJSON stream, not instead of it.

## Capturing incidents on alert

Pair the analyzer with [`--stash`](./cli#watch) to keep a forensic bundle for
every alert. Each capture runs in a background goroutine and never stalls the
stream; a stash failure surfaces in a `stash_error` field rather than aborting
`watch`. See [Ecosystem Integration](./ecosystem) for how fcheap stashes work
and what happens when fcheap isn't installed.

```bash
./bin/monitor watch --json --stash --stash-ttl 24h
```

## See also

- [`watch`](./cli#watch) — the flags and NDJSON output that stream these alerts.
- [Configuration](/reference/configuration) — the `cpu_alert_threshold` and
  `memory_alert_threshold` settings.
- [Ecosystem Integration](./ecosystem) — fcheap incident stashes on alert.
- [Architecture](/reference/architecture) — where the analyzer sits in the data
  flow.