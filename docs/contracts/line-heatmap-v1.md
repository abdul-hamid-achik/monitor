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
      "file": "examples/polyglot/js/workload.js",
      "start_line": 11,
      "end_line": 22,
      "range_source": "codemap", // "codemap" | "observed"
      "self_pct": 71.2,
      "cum_pct": 88.0,
      "lines": [
        {
          "line": 17, // the dogfood hot line: 99.9% of heavyStringify's own ticks land here, not on the line-11 declaration
          "self": 8120,
          "cum": 8120,
          "pct_of_function": 91.4, // this line's share of its OWN function's weight
          "code": "      s += JSON.stringify({ i, item, doubled, pad: 'x'.repeat(64) });",
          "mapping": "exact", // "exact" | "ambiguous" | "transpiled" | "inferred" | ""
          "stale": false, // true when the mapped file's current sha256 no longer matches what was profiled
          "issues": [
            // additive, v1.17 E3.4: errors × heat cross-reference,
            // read from the issues store, not computed here
            { "short_id": "A07E", "count": 3, "status": "open" }
          ]
        }
      ],
      "callees": [
        { "func": "JSON.stringify", "cum": 7600 }
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
- **`mapping`** is the *same* enum as a stack `Frame`'s `Mapping` field —
  defined once, in the [naming ADR](./local-sentry-naming#_1-naming-map)'s
  `Frame.Mapping` row, and reused here rather than redeclared: `exact`
  (source map resolved this line precisely), `ambiguous` (multiple candidate
  mappings), `transpiled` (mapped through a build step without a source
  map), `inferred` (no source map at all; the location was guessed), or
  `""` when no source map applies (plain Go, or a `.go` file profiled
  directly). Staleness is a **separate** boolean, `lines[].stale`, not a
  fifth `mapping` value: a profiled line can go stale after profiling (the
  file changed since), which is a signal a per-crash stack `Frame` has no
  equivalent for, so it does not belong inside the shared enum.
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
