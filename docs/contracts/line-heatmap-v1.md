# `monitor.line_heatmap.v1` (Draft)

> **Status: Draft.** This schema is not implemented yet — it is the target
> output of `heat.Build` (E3.1), the planned single producer behind
> `monitor hot <file|pid|service>`. Field names may still change before that
> PR lands. See the [naming ADR](./local-sentry-naming) for `monitor hot`'s
> command shape.

## Why this exists

Monitor already captures per-line data in two profilers — V8's
`positionTicks` (Node/Deno CPU profiles) and the `github.com/google/pprof`
proto (Go CPU/heap/goroutine) — but today's CLI only ever prints
function-level, sampled-symbol summaries; the per-line detail is discarded
after being read. `heat.Build` combines both sources into one line-level
model instead of adding a second, format-specific renderer per profiler:

- `FromCDP`: builds the model from a V8/Bun `.cpuprofile`'s `positionTicks`.
  Call-path variants of the same (function, file, line) are merged; `(idle)`,
  `(program)`, and `(garbage collector)` pseudo-frames are excluded from the
  percentage denominator and reported separately as `idle_pct` / `gc_pct`
  instead of diluting real lines.
- `FromPprof`: builds the model from a parsed pprof proto (`cpu`, `heap`, or
  `goroutine`), rolling up `flat`/`cum` per (function, line) directly from the
  proto instead of shelling out to `go tool pprof`.

A function's line range comes from `codemap symbol-at`, when codemap is
healthy; `range_source: "observed"` marks a range inferred from the sample
data itself when codemap is unavailable, so a heatmap can still render with
an honest caveat instead of failing outright.

## Shape

```jsonc
{
  "schema": "monitor.line_heatmap.v1",
  "profile_type": "cpu", // "cpu" | "heap_inuse" | "heap_alloc" | "goroutine"
  "unit": "samples", // or "bytes" for heap profiles
  "method": "v8_position_ticks", // "v8_position_ticks" | "pprof_proto" | "cpuprofile_file" | "darwin_sample"
  "runtime": "node", // "node" | "deno" | "bun" | "go" | "python" | "ruby"

  "samples": 12000,
  "active_samples": 11840, // excludes (idle)/(program)/(GC) pseudo-frames
  "idle_pct": 1.3,
  "gc_pct": 4.1,

  "functions": [
    {
      "name": "heavyStringify",
      "file": "internal/report/format.go",
      "start_line": 40,
      "end_line": 58,
      "range_source": "codemap", // "codemap" | "observed"
      "self_pct": 71.2,
      "cum_pct": 88.0,
      "lines": [
        {
          "line": 48,
          "self": 8120,
          "cum": 8120,
          "pct_of_function": 91.4, // this line's share of its OWN function's weight
          "code": "  data, _ := json.Marshal(v)",
          "mapping": "exact", // "exact" | "ambiguous" | "transpiled" | "stale" | ""
          "issues": [
            // additive, v1.17 E3.4: errors × heat cross-reference,
            // read from the issues store, not computed here
            { "short_id": "GRA-42", "count": 3, "status": "open" }
          ]
        }
      ],
      "callees": [
        { "func": "json.Marshal", "cum": 7600 }
      ]
    }
  ],

  "warnings": [
    "mostly idle: slowness is off-CPU" // e.g. when idle_pct > 50
  ],
  "limitations": [
    "V8 positionTicks are self-time; a callee's own time is listed under callees, not folded into the caller's line"
  ]
}
```

## Notes on specific fields

- **`lines[].pct_of_function`** is always relative to the *function's own*
  weight (`self`/`cum` roll-up within that function), not the profile total —
  this is what lets `monitor hot --file v8-hot.cpuprofile` name "the hot line
  within the hot function" instead of only "the hot function".
- **`mapping`** carries the same enum a stack frame's `Mapping` field uses
  (see the naming ADR): `exact` (source map resolved this line precisely),
  `ambiguous` (multiple candidate mappings), `transpiled` (mapped through a
  build step without a source map), `stale` (the mapped file's sha256 no
  longer matches what was profiled), or `""` when no source map applies
  (plain Go, or a `.go` file profiled directly).
- **`lines[].issues`** is additive (v1.17, E3.4) and always read from the
  issues store at render time — `heat.Build` never computes or caches issue
  membership itself, so a heatmap and the issues store never disagree about
  an issue's current status.
- **`warnings`** exists for exactly the honesty problem in the "which part of
  a function" reality check: an idle target (`idle_pct` over roughly 50%)
  gets an explicit `"mostly idle"` warning instead of silently pointing at
  whatever line happened to be running when the sampler fired.

## Compatibility

- `monitor profile --json` is unaffected: it keeps its current `text` and
  `symbols` fields exactly as-is, because `glyphrun`'s `procmon` step saves
  `profile --json`'s `text` verbatim. Only `investigate --json` and MCP omit
  `profile.text` by default (opt back in with `--include-raw` / `keep: true`).
- `monitor hot` is a new command with no prior JSON shape to preserve, so
  `monitor.line_heatmap.v1` has no back-compat constraint of its own yet —
  but once it ships, later additive fields (such as `lines[].issues` above)
  must follow the same "new field with an explicit status, never a rename"
  rule as `issue-context-v1`.
