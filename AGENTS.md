# AGENTS.md — Monitor development guide

This is the single guide for coding agents (Claude Code, Codex, and others)
and for people working on this repository. Read it before changing anything.

**Monitor** is a local-first, single-binary observability tool for macOS and
Linux, built for people and for agents. The binary does two jobs:

1. **Local error tracking that points at the line, with no SDK.**
   - `monitor run -- <cmd>` and `monitor stacktrace parse --record` turn
     crashes and printed errors from Node, Deno, Bun, Python, Ruby and Go into
     grouped issues. Each issue gets a culprit `file:line`, a source snippet,
     chained causes, codemap blast radius and the last local commit that
     touched the line.
   - `monitor hot` shows which line inside a function burns CPU, heap or
     goroutines.
   - Everything above is exposed through bounded contracts that the CLI and
     the MCP server share.
2. **Host and process monitoring.**
   - Snapshots, `watch` with anomaly rules, history, baselines and diffs,
     alerts.
   - `monitor studio`, the Bubble Tea v2 TUI.
   - Privacy-safe telemetry for Chalupa.

- **Module:** `github.com/abdul-hamid-achik/monitor`
- **Go:** 1.25+ (see go.mod)
- **License:** MIT
- **Design notes:** `~/notes/projects/monitor/` (Obsidian vault). The active goal is
  `2026-09-22-local-sentry-goal.md`, with its roadmap in
  `2026-09-22-local-sentry-roadmap.md`.

---

## TL;DR for agents

- **Build:**
  - `go build -o bin/monitor ./cmd/monitor`, or `task build` when the `task`
    binary works on your machine.
  - Run the CLI with `./bin/monitor --help`.
- **Tests:** `go test -race -count=1 ./...`
- **Lint and format:** `go vet ./...` and `gofmt -l .`. The gofmt command must
  print nothing.
- **Specs:** `GLYPH=glyph scripts/specs.sh`. It is the same script local runs,
  CI and release use, and it exits non-zero on any failure.
- Read every file before you edit it.
- Add tests with every change. Add a glyphrun spec for every behavior a user
  can observe.
- Never add a dependency without checking the existing patterns first.
- A PR is done when build, vet, gofmt, `go test -race` and `scripts/specs.sh`
  are all green. The CI matrix is ubuntu plus macOS for tests, and ubuntu with
  node, deno, bun, ruby and python provisioned for specs.

---

## Commands

### Taskfile (single words)

| Command | Effect |
|---|---|
| `task build` | Build `bin/monitor` |
| `task release` | Optimized release build with the version injected |
| `task run` | Build, then launch `monitor studio` |
| `task dev` | Rebuild and relaunch on file changes |
| `task test` | `go test -v ./...` |
| `task cover` | Write an HTML coverage report |
| `task bench` | Run benchmarks |
| `task lint` | `go vet ./...` |
| `task fmt` | `gofmt -w .` |
| `task tidy` | `go mod tidy` |
| `task specs` | Run `scripts/specs.sh` |
| `task docs` | Serve the VitePress site in `docs/` |
| `task docs-build` | Build the docs site; fails on dead links |
| `task snapshot` / `task doctor` | Print JSON snapshot / ecosystem health |
| `task check` | Full pipeline: tidy, lint, test, release build |
| `task clean` | Remove build artifacts |

### Quick debugging

```bash
./bin/monitor --help
./bin/monitor snapshot --json | jq '.cpu'
./bin/monitor doctor --json                    # ecosystem + codemap/vecgrep health + PATH shadowing
./bin/monitor run -- node examples/polyglot/js/workload.js   # crash -> issue
./bin/monitor issues && ./bin/monitor issue latest
./bin/monitor hot --file internal/profiler/testdata/v8-hot.cpuprofile
glyph spec verify specs/<name>.yml && glyph run specs/<name>.yml --format md
```

---

## Package map

```
cmd/monitor/main.go        entry point: cobra CLI; `monitor studio` launches the TUI

internal/
  cli/            cobra commands, one file each (run, issues, hot, investigate,
                  stacktrace, profile_logs, watch, doctor, mcp, ...); root.go
                  registers them
  devrun/         `monitor run -- <cmd>`: launch, copy output to the
                  terminal, detect exceptions, record issues; env, signals,
                  banners, launch registry
  stacktrace/     SDK-free stack-trace detection: Joiner and parsers for
                  V8/Deno/Bun, Python, Ruby, Go panics, zap +
                  pkg/errors, typescript-logging; InApp;
                  timestamps; parse checkpoints
  scrub/          default secret/PII redaction (patterns, emails, Luhn cards,
                  exact values of secret env vars)
  project/        one project/service resolver (flag > MONITOR_PROJECT > git
                  root > marker > service > process)
  issues/         veclite store: Issue/Occurrence, FingerprintV1 (alerts) and
                  V2 (exceptions), Culprit, ExceptionInfo, DedupeKey, window
                  filters, RecordException, short-lived writers
                  (OpenStoreWait/WithWriter)
  explain/        monitor.issue_context.v1 builder (snippet, causes, frames,
                  codemap impact, git "last touched", message culprit,
                  degraded[], next[])
  profiler/       CDP inspector (V8 positionTicks), in-process pprof proto
                  (google/pprof), macOS `sample` tree parser, LoadFile,
                  BuildHeatmap (monitor.line_heatmap.v1)
  sourcemap/      dependency-free Source Map v3 decoder/resolver
  procbind/       process -> runtime/codebase binding (node, bun, deno,
                  python, ruby, go); process tree + ResolveLeaf (wrapper ->
                  real child)
  ecosystem/      one-hop CLI wrappers (codemap, vecgrep, fcheap, glyph,
                  cairn, tvault, ...), health probes (health.go),
                  ArtifactRefV1
  incidents/      integrity-hashed monitor.incident bundles -> fcheap, with a
                  local resume registry
  mcp/            MCP stdio server (tools, typed inputs, confirm gate); the
                  logic lives in cli/mcp.go's Service
  collector/      host/process metric collection (canonical metric types)
  analyzer/       anomaly rules + cross-signal diagnosis; NewDefaultEngine is
                  shared by watch/Studio/MCP
  capture/        `monitor logs capture` (spawn or tail -> logger store)
  logger/ history/ baseline/   veclite log store, metric history, labeled
                  snapshots + diff
  telemetry/      identity-free host telemetry windows (frozen V1 contract)
  contextids/     MONITOR_* / CHALUPA_CI_* run correlation
  notify/ reload/ config/ kill/ cgroup/ temperature/ capability/
  ui/studio/      the TUI (Bubble Tea v2, charm.land/*)
  widgets/        sparklines, gauges, CodeFrame (line-heatmap renderer)

examples/polyglot/  js (node/bun/deno), python, ruby, go-pprof, go-plain,
                    go-crash, go-zap-stdout workloads; WORKLOAD_SECONDS
                    shortens them
specs/              glyphrun behavioral specs (run through scripts/specs.sh)
scripts/specs.sh    the one spec runner (PASS/SKIP/FAIL, skip-list, exit code)
docs/               VitePress site; docs/contracts/ holds the versioned
                    JSON contracts
```

---

## Architecture

### From error to issue

```
monitor run -- <cmd>          devrun: the copy goroutine writes to the terminal FIRST, then does a
monitor stacktrace parse      non-blocking send to a bounded channel (drops are counted)
  --record                    -> stacktrace.Joiner -> Parse -> scrub -> project.Resolve
                              -> issues.RecordException (FingerprintV2, Culprit, DedupeKey)
                              -> short-lived writer (issues.WithWriter)
monitor issues / issue <id>   explain.Build -> monitor.issue_context.v1 (CLI human | --json | --md, MCP brief)
```

- **FingerprintV2** hashes the outer exception type, the top 5 in_app frames of
  the outer exception (`func@relfile`, no line numbers) and the innermost cause
  type. Service, PID, release, codemap FQNs and sampled symbols never go into
  it. The **culprit** is the innermost cause's last in-app frame, walking
  back from its crash frame (the crash frame itself when in-app, otherwise
  the nearest in-app caller); if the innermost cause has none, the outer
  exception's last in-app frame; nil only when no frame in the chain is
  in-app.
- **Time is the event's time.** A replayed log takes its timestamp from the
  line, or from the file mtime when the line has none, never from now. Replays
  are idempotent through per-file checkpoints and a DedupeKey.
- **Monitoring never slows the monitored process.** Scanned streams use pipes
  owned by devrun. `cmd.Wait` never closes them, and they drain within a
  bounded grace period.

### From profile to line

```
CDP positionTicks (node/deno --inspect) | pprof proto (Go) | .cpuprofile (node/bun/deno --cpu-prof) | darwin sample
  -> profiler -> sourcemap (TS/bundled JS) -> BuildHeatmap -> monitor.line_heatmap.v1 -> widgets.CodeFrame
```

`monitor hot` works in three modes:

- `--file`: a `.cpuprofile` or `.pb.gz` on disk;
- `<pid>`: resolves the leaf process behind a `yarn`, `npm` or `go run`
  wrapper;
- `<service>`: looks up the launch registry that `monitor run --name` writes.

The heatmap also marks lines where an issue's culprit sits (errors × heat).
When a function's time looks JIT-inlined, the output says so; it never
invents a hot line for an idle or diffuse profile.

### Contracts (`docs/contracts/`)

- The naming ADR (launch verbs, environment-variable ownership, dedupe and
  checkpoint rules, the full fingerprint/culprit rule) lives in the
  Obsidian vault: `~/notes/projects/monitor/2026-09-22-local-sentry-naming-adr.md`.
  Code and specs cite it as "naming ADR §N"; it is NOT part of the docs
  site.
- `issue-context-v1.md`: `brief` is at most 4 KB and `standard` at most 16 KB.
  Every section carries a `status`.
- `line-heatmap-v1.md`
- `doctor-v1.md`: the stable presence contract.
- `monitor-incident-v1.md`: fcheap bundles and ArtifactRefV1.

Keep JSON changes additive. Chalupa and cairntrace parse `investigate --json`,
and glyphrun procmon parses `monitor profile --json` (`text` and `symbols`
must stay).

### MCP server (`internal/mcp`)

The server speaks stdio and has 10 tools.

- **Read-only:** `monitor_snapshot`, `monitor_processes`, `monitor_doctor`,
  `monitor_analyze`, `monitor_issues`, `monitor_issue`.
  - `monitor_issue` accepts `id:"latest"` with `project`, `service` and `kind`
    filters and returns the issue_context brief.
- **Mutating, gated by `confirm: true`:** `monitor_kill`,
  `monitor_profile_capture`, `monitor_investigate`, `monitor_record`.
  - `monitor_profile_capture` is runtime-aware. It supports `lines:true` for a
    bounded heatmap, plus `pprof_addr` and `keep`.

Confirmation is enforced in two layers. The SDK rejects a call that omits
`confirm`, and the handlers check it again, so a hand-built request still
gets a structured `refused: true` payload. Handlers only copy fields; the
business logic lives in the Service in `internal/cli/mcp.go`. Tool handlers
take `*mcp.CallToolRequest` as their second argument.

### Environment variable ownership

| Variable | Owner |
|---|---|
| `MONITOR=1`, `MONITOR_RUN_DIR` | legacy `monitor run <spec.yml>` (glyphrun/cairntrace react to them) |
| `MONITOR_RUN_ID`, `MONITOR_SERVICE`, `MONITOR_PROJECT`, `CHALUPA_CI_*` | read by `contextids` / `project` |
| `MONITOR_LAUNCH_ID`, `MONITOR_LAUNCH_SERVICE`, `MONITOR_LAUNCH_ROOT` | exported ONLY by `monitor run -- <cmd>`; `LAUNCH_ROOT` is the outermost launch's ID (nested runs dedupe, siblings don't) |
| `NODE_OPTIONS`, `BUN_OPTIONS`, `PYTHONUNBUFFERED` | only ever appended to, or set when unset, at launch |

### Stores

The logs, history and issues stores are embedded veclite files, pinned at
v0.22.1.

- **Writers are short-lived** (`OpenStoreWait` and `WithWriter`, retrying on
  `ErrFileLocked`). Readers use `OpenReadOnly`, which is lock-free.
- **Never** set `WithSharedRead(true)` on a writer; only readers may use it.
- **Known upstream issue:** veclite v0.22.1 does not persist MemoryConfig.
  Monitor re-applies its limits on every open. v0.22.1 also has a non-atomic
  Save. With heavy cross-process write contention, a write can be lost, so a
  single store never loses data but concurrent writers can race. The fix
  belongs in veclite (an atomic Save), followed by a measured version bump.

---

## Golden rules (do not break)

- **Never inject into a running process.** No SIGUSR1, `sys.remote_exec`,
  ptrace or late `--inspect`. Injection happens only at launch, only through
  the environment, and only by appending.
- **Never print inspector `ws://` URLs** in the CLI, `--json` or MCP output.
  Show the port only. The full URL may live only in the registry file, which
  is mode 0600.
- **Scrub error text** before persisting it or returning it through MCP or
  `--md`. Never persist argv, environment variables, local variables, headers
  or request bodies.
- **Keep telemetry V1 frozen and identity-free.** Only ArtifactRefV1 refs and
  closed-schema aggregates go to Chalupa. Chalupa owns those schemas.
- **Degrade honestly.** A missing or stale codemap, vecgrep index, git or
  source map becomes `skipped` with a recovery, never invented data. Never
  recommend `codemap index --reindex` on schema skew; upgrade the binary
  instead.
- **Call codemap and vecgrep as one-hop CLIs only.** No MCP→MCP chains. Use
  vecgrep only in `--mode keyword`, so error text never goes to an embeddings
  provider.
- **Everything is propose-only.** Specs, merges, notes, pins and resolves are
  suggested, never applied automatically.
- **Do not reintroduce Bubble Tea v1.** Keep each TUI tab's render and keys in
  `tab_<name>.go`; the model file is only a router.

---

## Coding conventions

- Put `context.Context` first on IO functions. Return errors immediately,
  wrapped with `fmt.Errorf("...: %w", err)`.
- Call `os.Exit` only from `main.go` or a CLI entry point, never from library
  code.
- CLI commands switch between human and JSON output with `JSONOutput(cmd)`.
  Metric structs carry `json:"snake_case"` tags.
- Name files in lowercase snake_case. Use PascalCase for exported names and
  camelCase for private ones. Every package has a doc comment. Group imports:
  stdlib, then third party, then local.
- Keep pure parsers free of OS build tags, so they are tested on both ubuntu
  and macOS. Only the system call goes in a `_darwin.go` file.

## Testing

- **Unit tests:**
  - Every package has `_test.go` files; prefer table-driven tests,
    `t.TempDir()` and golden fixtures (`testdata/`, `examples/polyglot/`).
  - **No fixed sleeps for readiness.** Poll for a ready file, a port or a
    line. Wall-clock budget tests skip under `-race`
    (`race_on_test.go` / `race_off_test.go`).
  - **Build fake secrets by concatenation** (`"sk_" + "live_..."`). GitHub push
    protection blocks provider-shaped literals anywhere in the pushed
    history.
  - Tests that need a runtime that is not on PATH call `t.Skip`.
- **Glyphrun specs:**
  - Every spec runs through `scripts/specs.sh`. The script accepts
    `MONITOR_SPECS_SKIP` and file arguments, and the same skip-list applies
    locally, in CI and in release.
  - A spec that needs a runtime needs that runtime provisioned in CI.
    Otherwise add it to the script's skip-list with a reason.
  - **Set both timeouts on command outcomes.** glyph applies an outcome-level
    `timeoutMs` and a `verify.command.timeoutMs`, both defaulting to 5s.
    Set both to the same value.
  - Keep specs portable: use `python3`, `ruby`, `node`, `bun` and `deno` from
    PATH, and relative repo paths.
  - Validate with `glyph spec verify specs/<name>.yml`.
  - When CI fails, the specs job uploads `.glyphrun/runs/` as the
    `glyphrun-runs` artifact.
- **Do not** run the TUI in tests, call `cobra.Execute()` (use
  `Root().Commands()`), or pass `--version` in tests (it calls `os.Exit`).

## Common tasks

- **Add a CLI command.**
  1. Create `internal/cli/<name>.go` with `newXxxCmd()`.
  2. Register it in `root.go`.
  3. Add the `--json` branch.
  4. Write `<name>_test.go` and a spec.
  5. Check that `./bin/monitor <name> --help` works.
- **Add an MCP tool.** Add a typed input struct, a handler that only copies
  fields, a Service method in `cli/mcp.go`, and an in-memory `CallTool` test.
  A mutating tool also needs `confirm` in its schema and a refusal test.
- **Add a stack-trace format.**
  1. Write a parser in `internal/stacktrace` with golden fixtures captured
     from the real runtime (paths rewritten to `/repo/...`).
  2. Add clean look-alike fixtures that must produce 0 events.
  3. Check that frame order is oldest to newest, with the crash frame last.
- **Add a spec:** copy `specs/version.yml` and adjust the intent, target,
  steps and outcomes.
- **Update the protected-process list:** edit `ProtectedProcessNames` in
  `internal/collector/types.go`.

## Gotchas

1. **The CPU-profile line is V8's.** `positionTicks` lines are 1-based.
   `callFrame.lineNumber` is 0-based and points at the declaration. When
   TurboFan inlines a function, its time lands on the caller's call line.
2. **Profile `Symbols` are sampled.** Never fingerprint on them.
   Investigations group on the dominant in-app function, and only when it
   holds at least 15% of active samples.
3. **Process CPU needs two samples.** The first collector observation is
   unavailable by design. Keep the PID creation-time checks, which catch PID
   reuse.
4. **Temperature comes from `sudo powermetrics` when possible.** Otherwise it
   is an estimate, flagged by `temperature.source`.
5. **Watch for codemap schema skew.** An older `codemap` binary on PATH
   cannot read a newer index. `monitor doctor` reports `schema_skew`; the fix
   is to upgrade the binary, never to reindex.
6. **Know the Status/Probe pair in `internal/ecosystem`.** Don't confuse the
   struct `Status` with the function `Probe`.
7. **fcheap contracts are strict.** ArtifactRefV1 requires `$schema`;
   validate before returning a ref.

## Docs site (Vercel)

- The repo-root `vercel.json` builds **only `main`**. On other branches the
  `ignoreCommand` exits 0, so Vercel skips the build instead of failing it.
- On `main` it skips commits that don't touch the docs.
- Never run `vercel promote`: pushing to `main` is the site release.
- CLI binaries ship from tags through the release workflow.
- Local commands: `task docs` and `task docs-build`.

## Known limitations

- **Hot lines by runtime:**
  - Live hot lines work for Node and Deno started with `--inspect` (or under
    `monitor run --inspect`).
  - Bun has hot lines only at exit, through `monitor run --profile` or
    `--cpu-prof`, because Bun's inspector speaks JSC, not V8 CDP.
  - Go needs `net/http/pprof` for exact lines; without it, `sample` is
    function-level only.
  - Python and Ruby get errors only; their hot lines need launch-time probes,
    which are the next epic.
- **Host metrics:** load averages are always 0 on macOS, because gopsutil
  does not expose them. Linux temperature is always estimated.

## References

- Goal, roadmap and verified findings: `~/notes/projects/monitor/2026-09-22-local-sentry-*.md`
- Codemap MCP pattern: `~/projects/codemap/internal/mcp/server.go`
- Glyphrun spec model: `~/projects/glyphrun/docs/verifiers.md`
