# Runtimes Matrix

Monitor's error tracking is SDK-free: it parses what a runtime already
prints. What each runtime gets, at a glance:

| Runtime | Crash parsing (`monitor run --` / `stacktrace parse`) | Live hot lines (`monitor hot`) | Exit-time profile (`monitor run --profile`) | Source maps |
|---|---|---|---|---|
| **Node** | Yes — uncaught exceptions and caught-and-printed errors on stderr/stdout | Yes — started with `--inspect`, or launched under `monitor run --inspect` | Yes — V8 `.cpuprofile` written at exit | Crash stacks: runtime-level `--enable-source-maps` (appended to `NODE_OPTIONS` at launch). Hot lines: monitor's own Source Map v3 resolver (`mapping: exact`/`ambiguous`) |
| **Deno** | Yes | Yes — Deno honors `NODE_OPTIONS=--inspect` the same way Node does | Not yet — Deno has no env-injectable `--cpu-prof`; use `--inspect` + `monitor hot <service>` for live profiling instead | Same as Node |
| **Bun** | Yes | No live lines — Bun's inspector speaks JSC, not V8's CDP; hot lines only at exit | Yes — via `BUN_OPTIONS`' `--cpu-prof` | Same as Node |
| **Python** | Yes — tracebacks, including `raise ... from` cause chains | Errors only — Python hot lines need launch-time probes (a later epic) | No | n/a |
| **Ruby** | Yes — exceptions with backtraces, including `rescue`-printed backtraces without a message | Errors only — same as Python | No | n/a |
| **Go** | Yes — panics, plus `zap`, `pkg/errors`-style and `fmt.Errorf("%w")` wrapped-error chains | Exact lines need `net/http/pprof` (`--pprof-addr`); without it, macOS `sample` is function-level only | No — use `net/http/pprof` with `monitor hot`/`monitor profile` | n/a |

All six runtimes' crashes group into the same issue store with the same
culprit `file:line` rule — see [Your First Issue](/guide/first-issue) and
[Local Issues](/guide/issues).

## Crash parsing details

`monitor run -- <cmd>` scans `stderr` by default (`--scan stdout` or
`--scan both` for loggers that print to stdout — some `zap` setups, `pino`,
many Rails loggers). The detector understands, per runtime:

- **V8 family (Node/Deno/Bun):** uncaught exceptions, `Error.cause` chains
  printed as `[cause]`, `console.error(err.stack)`, and
  `typescript-logging` output.
- **Python:** tracebacks with `__cause__`/`__context__` chaining
  ("The above exception was the direct cause of..."), `logging` output.
- **Ruby:** raised exceptions with backtraces, `warn e.backtrace`.
- **Go:** panics, plus `zap` and `pkg/errors`/`fmt.Errorf("%w")` chains
  printed in `%+v` form.

Parsed text is scrubbed before it is persisted (provider-shaped secrets,
emails, Luhn card numbers, URL credentials, and the exact values of
secret-named environment variables — `--redact-env` adds more names). Lines
are never dropped for the child's sake: the copy to the terminal comes
first, and a full detector buffer drops and counts rather than applying
back-pressure.

One buffering caveat, printed by `run --` itself when it applies: a scanned
stream becomes a pipe, which changes some runtimes' stdio buffering. Python
is handled (`PYTHONUNBUFFERED=1` is set when `--scan` includes stdout);
Ruby has no equivalent, so a scanned Ruby **stdout** may arrive in the
terminal in blocks until the process exits. Scanning stderr (the default)
avoids this.

## Hot lines and profiles details

- **Live hot lines** need a sampler that reports positions, which for
  Node/Deno means the V8 inspector (CDP `positionTicks`). `monitor run
  --inspect` opens one at launch and registers it in the launch registry
  for `monitor hot <service>`; the inspector's `ws://` URL is never
  printed — only the port. For details see [Hot Lines](/guide/hot-lines).
- **Exit-time profiles** (`monitor run --profile`) work for Node and Bun
  via the runtime's own `--cpu-prof` flags, flushed by an exit shim that
  even a bare Ctrl-C reaches — unless the application installs its own
  SIGINT/SIGTERM handler, in which case monitor leaves it completely
  alone.
- **Go** exactness depends on the profile source: a pprof proto
  (`net/http/pprof`) is line-exact and inlining-aware; macOS `sample` (the
  fallback when no pprof endpoint exists) is function-level only.
- **`monitor profile`** is runtime-aware independently of `hot`: CDP for
  Node/Deno processes with `--inspect`, ownership-verified
  `net/http/pprof` for Go `heap`/`cpu`/`goroutine`, macOS `sample` as the
  final fallback.

## Source maps

Two different mechanisms, both on by default:

- **Crash stacks** are symbolicated by the runtime itself: `monitor run --`
  appends `--enable-source-maps` to `NODE_OPTIONS` (creating it if unset,
  always appending, never overwriting) so Node/Deno/Bun print original
  `.ts`/source positions in their own stack traces. `--no-source-maps`
  leaves `NODE_OPTIONS` completely alone.
- **Hot lines** from a `.cpuprofile` or CDP capture are resolved by
  monitor's own dependency-free Source Map v3 decoder: generated positions
  map back to original lines with a per-line confidence of `exact` or
  `ambiguous` (a minified bundle that collapses many statements onto one
  generated line is `ambiguous`, never silently `exact`), and a map older
  than the file it maps is flagged stale with a warning instead of being
  trusted.

The `examples/polyglot/` directory ships a workload per runtime
(`js/` for Node/Bun/Deno, `python/`, `ruby/`, and several Go programs);
`WORKLOAD_SECONDS=3` shortens any of them.
