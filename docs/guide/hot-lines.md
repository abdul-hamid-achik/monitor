# Hot Lines

`monitor hot` answers "**which line** inside this function", not just "which
function" — a per-function table plus a source-rendered **CodeFrame** that
highlights the hot line with its share of the function's samples. It works
from a saved profile file, a live pid, or a named service, and it stays
honest: an idle or diffuse profile says so, an inlined callee is called out
instead of blamed on the caller's call line, and a missing source map is
reported, never guessed around.

## The three modes

| Mode | Command | What it does |
|---|---|---|
| File | `monitor hot --file <p>` | Loads a saved V8/Bun `.cpuprofile` or a pprof proto `.pb`/`.pb.gz` (gzip auto-detected) from disk. |
| Pid | `monitor hot <pid>` | Captures live. Wrapper processes (`sh`, `yarn`, `npm`, `go run`) are resolved to the real runtime leaf under them; an ambiguous tree exits 2 listing candidates. Node/Deno targets prefer a registered inspector; Go targets need `--pprof-addr`. |
| Service | `monitor hot <service>` | Looks the name up in the launch registry that `monitor run --name <service>` wrote (`$XDG_STATE_HOME/monitor/services/<project>/<name>.json`; `--project` overrides the project resolved from the current directory), then resolves and captures like the pid mode. An unregistered or stale name exits 2 listing every registered service for that project. |

A quick file-mode example, using a fixture profile from monitor's own
repository:

```bash
./bin/monitor hot --file internal/profiler/testdata/v8-hot.cpuprofile
```

```text
loaded internal/profiler/testdata/v8-hot.cpuprofile · cpu · 1.5s · 1,002 samples · active 97% (idle 0%, gc 1%, program 2% excluded)      method: v8 positionTicks
TOTAL  SELF   FUNCTION        LOCATION                                         ISSUES
96.2%  0.3%   (anonymous)     /repo/internal/profiler/testdata/src/hot.js:12   -
95.8%  95.8%  heavyStringify  /repo/internal/profiler/testdata/src/hot.js:3-6  -
0.1%  0.1%   cheap           /repo/internal/profiler/testdata/src/hot.js:10   -
-- heavyStringify · /repo/internal/profiler/testdata/src/hot.js:3-6 · 95.8% self -------------------
   3 |                                                              1.1% |
   4 |                                                             32.7% |########
>  5 |                                                             66.1% |#################
   6 |                                                              0.1% |
     % = share of this function's self samples
  V8 positionTicks are self-time; a callee's own time is listed under callees, not folded into the caller's line
next  monitor hot --file 'internal/profiler/testdata/v8-hot.cpuprofile' --func 'cheap' · --export hot.cpuprofile (Chrome DevTools / VS Code)
```

The header line reports the profile's health up front: how many samples,
how much of the capture was active (with idle, GC and program time
excluded). The **ISSUES** column is the errors × heat overlay: `-` when no
line in the function carries a recorded issue's culprit, otherwise the
culprit issues' short ids (capped at two, then `+N more`), so a function
that is both hot *and* crashing stands out immediately.

## Live capture: `--inspect`, `--name`, `--profile`

Live hot lines need a sampler that reports line positions, which for
Node/Deno means the V8 inspector. The launch flags wire it for you:

```bash
./bin/monitor run --name w --inspect -- sh -c 'node --jitless app.js'
./bin/monitor hot w --duration 2s
```

`--inspect` appends `--inspect=127.0.0.1:0` at launch (Node and Deno; a
one-line no-op note for Bun, whose inspector speaks JSC rather than V8's
CDP), records every "Debugger listening on ..." banner the child prints —
including ones from wrappers that re-exec into their own node child — into
the launch registry, and never prints the `ws://` URL itself: the URL's
session id is the inspector protocol's only bearer-token-shaped secret, so
only the port is ever shown, and the registry file is mode `0600`. A later
`monitor hot w` reuses the registered inspector instead of rediscovering
one. When the process is gone, its registry entry is cleaned up.

```text
monitor > w = node pid 1540 · inspector 127.0.0.1:62439 · sampling 2s
CPU · 2.0s · 1,366 samples · active 74% (idle 24%, gc 1%, program 1% excluded)      method: v8 positionTicks
TOTAL  SELF   FUNCTION        LOCATION                                ISSUES
99.2%  5.7%   processBatch    examples/polyglot/js/workload.js:25-26  -
99.2%  0.0%   (anonymous)     examples/polyglot/js/workload.js:80     -
93.5%  93.5%  heavyStringify  examples/polyglot/js/workload.js:13-17  -
-- heavyStringify · examples/polyglot/js/workload.js:13-17 · 93.5% self ----------------------------
  13 |   for (const item of items) {                                0.1% |
> 17 |       s += JSON.stringify({ i, item, doubled, pad: 'x'.re…  99.9% |##########################
     % = share of this function's self samples
  V8 positionTicks are self-time; a callee's own time is listed under callees, not folded into the caller's line
next  monitor hot w --duration 2s --func 'processBatch' · --export hot.cpuprofile (Chrome DevTools / VS Code)
```

`monitor run --profile` is the exit-time variant for Node and Bun: it
writes a V8 `.cpuprofile` when the child exits — flushed even by a bare
Ctrl-C through an exit shim, unless the application installs its own
SIGINT/SIGTERM handler, in which case monitor leaves it completely alone —
and prints the hottest function plus the `monitor hot --file` command to
drill in:

```text
monitor > cpu profile: ~/.local/state/monitor/profiles/05a9804d9dccc6ee33c6cc7b/CPU.20260924.064900.2100.0.001.cpuprofile · hottest: heavyStringify .../workload.js:13 (10%)
next  monitor hot --file ~/.local/state/monitor/profiles/05a9804d9dccc6ee33c6cc7b/CPU.20260924.064900.2100.0.001.cpuprofile
```

## Reading the CodeFrame

The CodeFrame under the table renders the target function's source (read
from your working tree) with one row per sampled line:

- `> 17 |` marks the hot line; the percentage column is that line's share
  of **this function's self samples**, and the bar chart is the same
  number drawn out.
- The footer states the method and its meaning: V8 `positionTicks` are
  self-time, so a callee's own time appears under that callee (the table's
  callees), never folded into the caller's line.
- `--func <name>` shows a different function's CodeFrame instead of the
  hottest one; `--top N` widens the table; `--export <path>` saves the
  loaded profile (`.cpuprofile`/`.pb.gz`/`.pb`, openable in Chrome
  DevTools or VS Code) or the heatmap document (`.json`); `--json` emits
  the whole `monitor.line_heatmap.v1` document
  (`schema`, `method`, `profile_type`, `runtime`, `unit`, `samples`,
  `active_samples`, `idle_pct`, `gc_pct`, `limitations[]`, `functions[]`
  with per-line `lines[]`, `callees[]`, `self_pct`/`cum_pct`).

## Honesty rules

Hot never invents a line:

- **Inlining.** When a caller's one dominant line is a bare call to a
  function that never appears separately in the profile — the shape
  TurboFan inlining produces — the output says so under that function's
  CodeFrame instead of blaming the call line:

  ```text
  -- heavyStringify (mapping: inferred (inlined into processBatch:26)) · examples/polyglot/js/workload
    11 | function heavyStringify(items) {
    ...
  ! likely JIT-inlined: heavyStringify() was inlined into processBatch; the time on line 26 is spent inside heavyStringify
  ```

  When the callee's own source can be located, the primary CodeFrame
  points at the CALLEE — where the time is actually spent — labelled
  `mapping: inferred (inlined into processBatch:26)`, and the warning
  prints under that frame (not in the generic top-of-output warnings
  block). The callee frame renders in a metrics-free mode: no `>`
  marker, no percentage column, because a fully-inlined callee has no
  per-line sample data to show, and pretending otherwise would claim
  things that are not true. When the profile *does* carry a genuine
  small trickle of the callee's own samples, that real data is shown
  instead.
- **Idle and diffuse profiles.** A capture that is mostly idle reports
  `active N% (idle M% excluded)` and says the heat is off-CPU — the hot
  line would be a lie. A profile whose time is spread across many lines
  and functions reports that instead of picking a spurious winner.
- **Source maps.** Hot lines from bundled or transpiled JavaScript resolve
  through monitor's own Source Map v3 decoder. Each resolved line carries
  a `mapping` confidence of `exact` or `ambiguous` — a minified bundle
  that collapses many statements onto one generated line is `ambiguous`,
  never silently `exact` — and a map older than the file it maps triggers
  a stale warning rather than trusted output. When no map applies, the
  generated position is shown as-is.

## Go targets: pprof and the `sample` fallback

For a live Go process, exact lines require a `net/http/pprof` endpoint:

```bash
./bin/monitor hot <pid> --pprof-addr 127.0.0.1:6069 --duration 3s
```

Monitor verifies that the listener actually belongs to the target pid
before scraping it; passing `--pprof-addr` explicitly asserts you already
know the endpoint is the right one and skips that check. The pprof proto
is line-exact and inlining-aware (`method: pprof proto (inlining-aware; no
go toolchain needed)` — Go's own inlined frames are resolved to the
innermost line, so the hot line is the statement that was executing, not
its inline wrapper). `--type heap`, `--type heap-alloc` and
`--type goroutine` are available for pprof sources.

Without a pprof endpoint, monitor falls back to macOS `sample`, which is
**function-level only** — the output says so (`method: macOS sample
(function-level only; no per-line detail)`) instead of fabricating lines.
The same caveat applies to `sample`-sourced captures everywhere else in
monitor.

## See also

- [Runtimes Matrix](/guide/runtimes) — which runtime supports live hot
  lines, exit-time profiles, and source maps.
- [Your First Issue](/guide/first-issue) — the crash-to-explained-issue
  journey; issue culprits are the `ISSUES` column's other half.
- [CLI Reference](/guide/cli) — `hot` flags in the command index.
