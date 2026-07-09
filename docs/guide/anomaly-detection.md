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
  "detail": "cpu spike",
  "diagnosis": {
    "summary": "node pinned a core for 45s while RSS stayed flat — consistent with a hot loop",
    "evidence": ["cpu 150% for 45s", "rss flat"],
    "confidence": "medium",
    "next_actions": ["monitor profile 1133 --type cpu", "monitor investigate 1133"]
  }
}
```

`pid` and `process` are present only on per-process rules; system-wide rules
omit them. `diagnosis` is attached whenever the analyzer's cross-signal rule
table can interpret the pattern — see [Diagnosis](#diagnosis) below.

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

## Diagnosis

Raw alerts say *what* crossed a line; the diagnosis says *why it matters and
what to do next*. Every diagnosis has four fields:

| Field | Meaning |
|-------|---------|
| `summary` | One plain-language sentence, e.g. "RSS grew 42%/10min while CPU stayed flat — consistent with a memory leak (slope 3.2MB/min, R²=0.94)". |
| `evidence` | The numbers behind the claim (regression slope, R², durations, sample counts) — never discarded, so an agent can double-check the reasoning. |
| `confidence` | `low`, `medium`, or `high`, graded from the strength of the evidence: regression fit (R², sample count) for a trend, the level for a flat-but-high signal, or the reversal count for a sawtooth. |
| `next_actions` | At most two concrete next steps — a `monitor` CLI command or MCP tool invocation. |

The analyzer (`internal/analyzer/diagnosis.go`) correlates RSS and CPU trend
classes over the per-PID history it already holds, using a small ordered rule
table (most specific pattern first):

| Pattern | Interpretation |
|---------|----------------|
| RSS sawtooth (reversals, weak linear fit) | GC pressure / allocation churn |
| RSS rising **and** CPU rising | load (demand, not a leak) |
| RSS rising, CPU flat/falling/high | memory leak |
| RSS flat/falling, CPU high or rising | spin / hot loop |

Everything else — nothing trending, too few samples — produces no diagnosis;
a quiet process stays quiet.

The same `Diagnosis` shape appears on `watch` alerts (this page), incident
bundles, `--notify` / `--webhook` sinks, `monitor diff` verdicts (see
[`diff`](./cli#diff)), and the `monitor_analyze` MCP tool (see
[MCP Server](./mcp)) — one interpretation layer, every surface.

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
| Desktop notification | `--notify` | `osascript` (macOS) / `notify-send` (Linux); the message leads with the diagnosis `summary` (falling back to the terse rule `detail` when there's no diagnosis) and appends the confidence level; failures log to stderr. |
| Webhook | `--webhook <url>` | POSTs the raw `collector.Alert` JSON (with `diagnosis` alongside it when the analyzer attached one); non-2xx logged to stderr. |
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