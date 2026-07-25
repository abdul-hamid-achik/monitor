# Telemetry contract

`monitor telemetry` produces privacy-safe, bounded metric rollups for an
external control-plane adapter. It is a local stdout protocol: Monitor does not
open an outbound connection, accept credentials, persist a spool, or attach
vendor-specific deployment identity.

## Command

```bash
monitor telemetry [--interval 5s] [--window 30s] [--once]
```

The command prewarms counter-based network and disk metrics before starting the
exported window. It emits one compact JSON object per line. `--once` emits one
partial window after a single interval. SIGINT or SIGTERM flushes a non-empty
partial window and exits successfully.

Sampling cadence and window deadlines use a monotonic clock. Civil-clock
corrections therefore cannot stretch, reverse, or collapse serialized windows.
If Monitor misses a sampling slot, resumes late, or a collection call crosses
a window deadline, that window is marked partial and the late sample is not
assigned to the elapsed window.

## V1 envelope

```json
{
  "schema_version": 1,
  "kind": "monitor.telemetry_window",
  "session_id": "0123456789abcdef0123456789abcdef",
  "sequence": 1,
  "emitted_at": "2026-07-24T12:00:30Z",
  "producer": {
    "name": "monitor",
    "version": "1.14.0"
  },
  "window": {
    "from": "2026-07-24T12:00:00Z",
    "to": "2026-07-24T12:00:30Z",
    "sample_interval_ms": 5000,
    "sample_count": 6,
    "partial": false
  },
  "metrics": {
    "system.cpu.usage": {
      "unit": "percent",
      "count": 6,
      "min": 8.1,
      "avg": 15.2,
      "p95": 22.4,
      "max": 27.9,
      "last": 17.1
    }
  },
  "availability": {
    "system.cpu.usage": {
      "state": "observed",
      "observed_samples": 6,
      "missing_samples": 0
    },
    "system.memory.used": {
      "state": "unavailable",
      "observed_samples": 0,
      "missing_samples": 6
    },
    "system.memory.available": {
      "state": "unavailable",
      "observed_samples": 0,
      "missing_samples": 6
    },
    "system.memory.usage": {
      "state": "unavailable",
      "observed_samples": 0,
      "missing_samples": 6
    },
    "system.memory.pressure": {
      "state": "unavailable",
      "observed_samples": 0,
      "missing_samples": 6
    },
    "system.swap.used": {
      "state": "unsupported",
      "observed_samples": 0,
      "missing_samples": 6
    },
    "system.network.receive_rate": {
      "state": "unavailable",
      "observed_samples": 0,
      "missing_samples": 6
    },
    "system.network.transmit_rate": {
      "state": "unavailable",
      "observed_samples": 0,
      "missing_samples": 6
    },
    "system.disk.read_rate": {
      "state": "unavailable",
      "observed_samples": 0,
      "missing_samples": 6
    },
    "system.disk.write_rate": {
      "state": "unavailable",
      "observed_samples": 0,
      "missing_samples": 6
    },
    "system.load.one_minute": {
      "state": "unavailable",
      "observed_samples": 0,
      "missing_samples": 6
    }
  },
  "alerts": [
    {
      "rule": "swap_pressure",
      "severity": "warning",
      "count": 1
    }
  ]
}
```

All timestamps are UTC RFC3339Nano values. A session ID is 16 random bytes
encoded as 32 lowercase hexadecimal characters. Sequence numbers start at one
and increase only after a line is written successfully. Consumers can use
`session_id + sequence` as an idempotency key.

The sample interval is expressed as a positive integer number of milliseconds,
but host-wide scans are intentionally rate-limited: the CLI rejects intervals
shorter than 1 second.

`availability` always contains all eleven V1 metric IDs. `metrics` contains a
summary only when at least one sample was observed. This keeps an observed zero
distinct from unavailable data.

## Fixed metrics

| Metric ID | Unit |
|-----------|------|
| `system.cpu.usage` | `percent` |
| `system.memory.used` | `bytes` |
| `system.memory.available` | `bytes` |
| `system.memory.usage` | `percent` |
| `system.memory.pressure` | `percent` |
| `system.swap.used` | `bytes` |
| `system.network.receive_rate` | `bytes_per_second` |
| `system.network.transmit_rate` | `bytes_per_second` |
| `system.disk.read_rate` | `bytes_per_second` |
| `system.disk.write_rate` | `bytes_per_second` |
| `system.load.one_minute` | `load` |

Metric values must be finite and non-negative; percentages cannot exceed 100.
Each summary reports `count`, `min`, `avg`, nearest-rank `p95`, `max`, and
`last`.

## Availability and alerts

Availability states are:

- `observed` — every sample was collected.
- `partial` — the window contains observed and missing samples.
- `unsupported` — the platform does not support the metric.
- `unavailable` — no sample was observed for any other reason.

Raw collector reasons are never forwarded because operating-system errors can
contain local paths or other identity.

Alert summaries are limited to the system-level rules `cpu_threshold`,
`mem_threshold`, `disk_fill`, and `swap_pressure`. Allowed severities are
`info`, `warning`, and `critical`. Count means the number of affected samples,
not the number of mounts or processes, so it cannot exceed the window's sample
count. Detail, PID, process, diagnosis, evidence, and next actions are never
included.

The current strict collector can produce `swap_pressure` without collecting
identity. It does not inspect filesystems to produce `disk_fill`; the rule
remains reserved in the V1 allowlist for a future anonymous source.

## Bounds

- Maximum 3,600 samples per window.
- Maximum 32 KiB per complete NDJSON line, including the newline.
- Fixed eleven-metric allowlist.
- Fixed alert rule and severity allowlists.
- No unbounded labels, arrays, histories, process lists, or filesystem lists.

The writer applies backpressure. Monitor does not create an internal queue or
disk spool; a broken consumer pipe is an explicit command failure. Cancellation
can interrupt a blocked write, and a final partial flush uses a bounded grace
period.

## Compatibility

V1 is a closed privacy contract: consumers must reject unknown object fields,
metric IDs, units, availability states, alert rules, and severities. Adding,
removing, or renaming a field or metric, or changing the meaning of an
existing value, requires a new integer `schema_version`.

`producer.version` identifies the Monitor implementation and is limited to
32 safe characters; it does not widen the schema. Consumers must reject an
unknown schema version instead of guessing.

## Privacy boundary

The contract never includes:

- hostname, IP address, operating-system user, PID, PPID, or process name;
- command arguments, environment variables, logs, or arbitrary labels;
- devices, mount points, filesystem names, source files, or artifact paths;
- raw collector errors, alert details, diagnoses, profile text, or symbols.

The command enforces this boundary at collection time, not only during JSON
projection. Its closed telemetry profile does not run hostname, process,
cgroup, temperature, per-core/topology, history, partition, filesystem, mount,
or device collectors.

The existing `watch`, full snapshot, and compact snapshot commands are local
diagnostic interfaces and do not provide this privacy guarantee.

## Scope limitation

V1 reports host-level metrics. Monitor's current cgroup reader resolves
`/proc/self/cgroup`, which describes Monitor's own service or container.
Consequently the telemetry contract does not present that data as workload
usage. A container-aware adapter should use a verified container metric source
instead of relabeling Monitor's self-cgroup.

On Linux, network rates come from system-wide IPv4/IPv6 octet counters and
disk rates come from the aggregate `pgpgin`/`pgpgout` counters. They are
reported as unavailable on platforms without equivalent identity-free sources.
Filesystem fill remains unavailable because discovering mounted filesystems
would violate the closed collection profile. Network rates represent IP-layer
octets and intentionally exclude link-layer header overhead.
