# `monitor.line_heatmap.v1`

> **Status: Implemented for `monitor hot --file <path>`** (E3.1,
> `profiler.BuildHeatmap`, the `heat.Build` this document originally
> drafted). Resolving a live `<pid|service>` target — the `hot <pid>` /
> `hot <service>` forms the naming ADR also documents — is E3.2, a later
> wave; today's `--file`-only CLI gives that combination a clear
> "not implemented yet" error rather than doing nothing silently. See the
> [naming ADR](./local-sentry-naming) for `monitor hot`'s full command
> shape.

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
data itself when codemap is unavailable (or, for a function with zero
attributed `lines` — a pure ancestor whose only signal is its `callees` —
falls back to its own generated declaration line as both start and end),
so a heatmap can still render with an honest caveat instead of failing
outright.

`FromCDP` also excludes a JS runtime's own bootstrap/module-loader frames
(Node's `node:internal/...` built-ins, and the analogous `ext:`/`deno:`
schemes Deno's isolate uses) from `functions` entirely, rather than listing
them as zero-self wrapper rows: every sample's stack passes through a
runtime's own module-loading machinery, so including it would bury real
user functions under a wall of `(anonymous)` bootstrap wrappers. Their real
CPU time still counts toward `active_samples` — it's excluded from the
function list, not from the honesty accounting.

A location-less native builtin (no `url`, `lineNumber: -1` — Bun's own
`JSON.stringify`/`String.prototype.repeat` and similar) is excluded from
`functions` the same way, for the same reason there's no real source line
to report: it would otherwise render as a fabricated "line 0 of an empty
file". Unlike the runtime-bootstrap case, though, its real cost is
significant enough to be worth surfacing — it still appears in its
caller's own `callees`, by name, with its real cumulative weight.

## Shape

```jsonc
{
  "schema": "monitor.line_heatmap.v1",
  "profile_type": "cpu", // "cpu" | "heap_inuse" | "heap_alloc" | "goroutine"
  "unit": "samples", // "samples" (CDP) | "nanoseconds" (pprof cpu — see the note below) | "bytes" (pprof heap) | "count" (pprof goroutine, or an unrecognized column)
  "method": "v8_position_ticks", // "v8_position_ticks" | "pprof_proto" | "cpuprofile_file" | "darwin_sample"
  "runtime": "unknown", // "node" | "deno" | "bun" | "go" | "python" | "ruby" | "unknown"

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
          "stale": false, // true when the .map file is older than the generated file it maps by more than a small tolerance (internal/sourcemap's mtime check — not a content hash)
          "issues": [
            // additive, v1.17 E3.4: errors × heat cross-reference,
            // read from the issues store, not computed here
            { "short_id": "A07E", "count": 3, "status": "open" }
          ]
        }
      ],
      "callees": [
        { "func": "JSON.stringify", "cum": 7600 }
      ] // sorted by cum descending, capped at 5
    }
  ],

  "warnings": [
    "mostly idle: slowness is off-CPU", // idle_pct > 50
    "diffuse: the hottest function (f) has only 3.2% of active samples; this profile may be too spread out to point at one line", // the top function's own self (or cum, for an all-wrapper profile) share is under 5%
    "one-line/minified bundle (dist/app.js): V8 positionTicks carry no column; per-line attribution not possible, only ambiguous", // a generated line's mapped segments span more than one original line
    "source map older than dist/app.js: resolved lines may be wrong" // the .map is older than the generated file it maps (a stale build)
  ],
  "limitations": [
    "V8 positionTicks are self-time; a callee's own time is listed under callees, not folded into the caller's line"
  ]
}
```

## Notes on specific fields

- **`runtime`** defaults to `"unknown"` for a file-loaded `.cpuprofile`
  (`monitor hot --file`): Node, Deno, and Bun all write the identical CDP
  wire shape, so it is genuinely not determinable from the file's content
  alone — degrading honestly to `"unknown"` rather than guessing `"node"`.
  A live capture (E3.2, `monitor hot <pid|service>`) can set it from
  `procbind`'s own runtime detection once that wiring lands. `"go"` is
  hard-coded for a pprof-sourced heatmap: every pprof proto `monitor hot
  --file` is actually handed, in this codebase, comes from `monitor
  profile`/`net/http/pprof` capturing a Go process — but `LoadFile` itself
  accepts any spec-shaped pprof proto, Go-produced or not, so this is an
  assumption about how the CLI is used, not a fact `heat.Build` can verify
  from the proto's own bytes.
- **`samples`/`unit`** are measured in whatever `unit` says, never
  literally "a count of samples" regardless of the field's name: a CDP
  source is a genuine sample count (`unit: "samples"`), but a pprof CPU
  source's `samples` is a **nanosecond total** across the selected value
  column (`unit: "nanoseconds"`) — the same figure `active_samples` and
  `idle_pct` are derived from when the proto carries a capture duration —
  a pprof heap source's is bytes, and goroutine's is a plain count.
- **`functions`** is sorted by `cum_pct` descending (then `self_pct`
  descending, then `name`) and capped at 25 entries by default
  (`monitor hot --top N` overrides the cap; `--func NAME` filters to one
  function by exact name instead). Sorting by `cum_pct` — not `self_pct` —
  matches a thin wrapper's cumulative total inheriting almost entirely from
  a callee it does no real work of its own on; `monitor hot`'s own default
  CodeFrame target is still chosen by highest `self_pct`, not this sort
  order, since a wrapper's own line is never the interesting one to expand.
  That default-target pick, like `warnings`' own "diffuse" check, is always
  computed from the FULL function list, before `--func`/`--top` filter or
  cap `functions` — so a small `--top` can never silently swap in some
  other, cooler function's CodeFrame just because it truncated the real hot
  one out of the visible table.
- **`lines[].pct_of_function`** is always relative to the *function's own*
  weight (`self`/`cum` roll-up within that function), not the profile total —
  this is what lets `monitor hot --file v8-hot.cpuprofile` name "the hot line
  within the hot function" instead of only "the hot function".
- **`lines`** is always aggregated on ORIGINAL (source-mapped) coordinates,
  not generated ones: when a source map sends more than one GENERATED line
  to the same original line — common in a bundler's transpiled output —
  `heat.Build` sums their `self`/`cum` into one `lines` entry rather than
  emitting duplicate rows for the same original line. That entry's
  `mapping` is the LEAST certain of the group's own mappings (claiming the
  group's best-case certainty would overstate how precisely it was
  actually located), and its `stale` is the OR of the group's.
- **`mapping`** is the *same* enum as a stack `Frame`'s `Mapping` field —
  defined once, in the [naming ADR](./local-sentry-naming#_1-naming-map)'s
  `Frame.Mapping` row, and reused here rather than redeclared: `exact`
  (source map resolved this line precisely), `ambiguous` (multiple candidate
  mappings), `transpiled` (mapped through a build step without a source
  map), `inferred` (no source map at all; the location was guessed), or
  `""` when no source map applies (plain Go, or a `.go` file profiled
  directly). `heat.Build` itself only ever produces `exact`, `ambiguous`, or
  `""` — the three outcomes `internal/sourcemap.Resolver.Resolve` itself can
  return, given only a line (no column: V8 `positionTicks`/pprof lines carry
  no column, so `Resolve` is queried at the column of the generated line's
  first non-blank character rather than column 0, which several bundlers'
  output maps to the tail of the *previous* statement instead of the one
  actually on that line — verified live against a real `bun build
  --sourcemap=external` output, see `internal/profiler/testdata/tssrc`).
  `transpiled`/`inferred` describe a *guess* made in the absence of a map
  entirely, which is `stacktrace.Frame`'s business, not `heat.Build`'s.
  Staleness is a **separate** boolean, `lines[].stale`, not a
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
  whatever line happened to be running when the sampler fired, and a
  profile whose hottest function barely dominates (under roughly 5% of
  active samples, computed from the FULL function list — before `--func`/
  `--top` filter or cap it — so neither flag can hide a real concentration
  or manufacture a false one) gets a `"diffuse"` warning instead of a
  confident-looking pick among many similarly-cold functions. A source-map
  warning is added per affected generated file (not per line) when a
  generated line's mapped segments span more than one original line — V8
  `positionTicks` carry no column, so this can't honestly be called an
  *exact* attribution — or when the `.map` is older than the generated
  file it maps.

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
