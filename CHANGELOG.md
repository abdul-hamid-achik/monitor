# Changelog

All notable changes to Monitor are documented here. The format is loosely
based on [Keep a Changelog](https://keepachangelog.com/), and the project
follows [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Changed

- The documentation site now uses a purpose-built editorial landing page,
  local Geist variable fonts, an interactive signal-to-evidence walkthrough,
  clearer ecosystem and MCP safety explanations, and a more polished reading
  system across navigation, sidebars, code blocks, tables, and mobile layouts.

## [1.15.1] - 2026-07-27

### Added

- MCP tools now publish standard safety annotations for read-only,
  destructive, idempotent, and closed-world behavior, with a behavioral spec
  that verifies the advertised contract.
- The MCP guide now includes a bounded, observation-first agent workflow from
  compact snapshot through issue triage and confirmed investigation.

## [1.15.0] - 2026-07-27

### Added

- **Local issue intelligence.** Monitor now persists a local-first
  `Run → Event/Occurrence → Issue → Evidence` model in veclite. Stable v1
  fingerprints group recurring failures without mixing PID, run, release, or
  artifact identity; resolved issues reopen on a later regression while
  ignored issues keep recording occurrences without reopening.
- **Issue CLI and MCP surfaces.** `monitor issues list/show/resolve/reopen/ignore`
  (alias `issue`) provide bounded JSON and human workflows. Read-only MCP
  tools `monitor_issues` and `monitor_issue` expose the same groups,
  occurrences, run context, and evidence to agents; the MCP server now has ten
  tools (six read-only and four confirm-gated mutations).
- **Node/process→codebase binding.** `monitor process <pid>` and
  `monitor investigate` resolve cmdline/cwd/exe, classify runtime
  (`node`/`bun`/`deno`/`go`/`python`), detect Node `--inspect`, find the
  nearest `package.json`/`go.mod` root (or `--codebase`), and attach the
  binding to the investigation report.
- **Node CDP inspector profiler.** `monitor investigate` now captures a
  CPU profile via the Chrome DevTools Protocol when a Node/Bun/Deno process
  has `--inspect` enabled. Frames carry real `file:line` (e.g.
  `node:internal/timers:508`, `[eval]:1`), so codemap correlation works
  for JS runtimes — not just Go. Falls back to macOS `sample` when the
  inspector is absent or unreachable.
- **Investigate pipeline extensions:** steps `identify` → `snapshot` →
  `profile` → `correlate` → `semantic` → `stash` → `issue`. Codemap calls take
  `-C <codebase>`; vecgrep runs similar/search against the bound project.
  Chalupa correlation IDs (environment, deployment, run, step, suite,
  attempt, release, service, git SHA) become typed run context, fcheap tags,
  and manifest context.
- **Richer incident bundles** (`monitor.incident` v1): `process.json`,
  `correlations.json`, `semantic.json`, the verified raw profile bytes, plus best-effort
  `fcheap artifact-ref` → `ArtifactRefV1` on successful stash.
- `monitor watch --stash` groups each emitted alert into the durable issue
  index and attaches either `fcheap://stash/<id>` or a recoverable
  `monitor://incidents/<id>` evidence reference.
- `monitor run` now always gives glyphrun children `MONITOR=1` and a live
  `MONITOR_RUN_DIR`, creating and cleaning a temporary run directory when the
  caller did not supply one.
- Cross-repo handoff spec: `docs/contracts/monitor-incident-v1.md` for
  Chalupa and file.cheap agents.

### Changed

- Per-process CPU is derived from cumulative user+system deltas over wall
  time, detects PID reuse/counter resets, and reports first samples as
  unavailable. One-shot process surfaces warm up for 100 ms and fall back to
  a targeted live-PID inspection when macOS omits a process from enumeration.
- MCP `monitor_investigate` accepts `codebase`, the complete Chalupa CI
  correlation context, `ttl`, and `no_save`.
- Log search is bounded (default 50, maximum 1000); the writer applies seven-day
  time retention and a 100,000-entry FIFO cap, including cleanup of legacy
  records. The issue store retains at most 10,000 issue groups and 100,000
  occurrence bodies while preserving cumulative counts. Default log and issue
  directories are private (`0700`) and stores are `0600`.
- file.cheap integration now follows its real JSON contract, disables
  post-save auto-compression for atomic success semantics, treats stash IDs as
  opaque, and emits strict credential-free `ArtifactRefV1` values. The stable
  bundle tree hash is an integrity receipt, not a deduplication promise.
- codemap correlation distinguishes unresolved call graphs from real blast
  radius, and vecgrep consumes its versioned JSON envelope with explicit
  indexed/fresh readiness and surfaced warnings. Correlation work is bounded
  by frame and time budgets.
- Ecosystem docs: codemap/vecgrep are active investigate integrations, not
  doctor-only probes.

### Security

- Incident registries validate IDs and trusted roots instead of trusting
  persisted paths; reject symlinks, non-regular files, undeclared manifest
  entries, oversized manifests, more than 32 files, files over 128 MiB, and
  bundles over 256 MiB.
- Bundle hashing uses length-framed filenames and contents, re-checks the hash
  after fcheap returns, stages resume operations privately, and preserves
  external source bundles. Process argv and machine-specific temporary profile
  paths are no longer persisted.
- Artifact-ref decoding rejects unknown/trailing fields and validates schema,
  provider, URI/ID agreement, producer metadata, kind, and entrypoint before a
  reference is exposed to Chalupa or another consumer.

### Fixed

- Failed or unavailable fcheap saves retain complete evidence in the private
  local registry and return a resumable reference; a save that returns an
  invalid ID or races with bundle mutation is never reported as successful.
- Incident tree hashes no longer admit ambiguous filename/content
  concatenations.
- Tests that invoke installed ecosystem tools isolate their file.cheap/XDG
  state instead of touching the operator's real vault.


## [1.14.0] - 2026-07-24

### Added

- `monitor telemetry` streams bounded `monitor.telemetry_window` V1 NDJSON
  rollups for external control planes. The fixed CPU, memory, swap, network,
  disk, and load schema includes explicit availability and sanitized system
  alert counts while excluding host/process identity, paths, mounts, raw
  errors, and alert details.
- The collector can skip process enumeration for privacy-constrained,
  host-metric-only consumers without changing the default TUI, CLI, or MCP
  behavior.
- Telemetry uses a closed scalar-only collection profile and monotonic
  deadlines. It skips identity, filesystem, topology, temperature, cgroup, and
  history work; delayed or missed sampling produces a partial window instead
  of stretching an elapsed one.

## [1.13.0] - 2026-07-16

### Fixed

- **The MCP handshake reports the real version.** The MCP initialize
  handshake advertised a hardcoded `0.3.0` while the repo shipped v1.12.1 —
  the server now takes the goreleaser-injected build version. `task release`
  stamps `git describe` so local release builds stop reporting `monitor dev`.

### Changed

- CI and release workflow actions are pinned to full commit SHAs
  (checkout v7.0.0, setup-go v6.5.0, goreleaser-action v7.2.3), and the specs
  job installs `glyph@v0.14.0` instead of `@latest`.

### Documentation

- README documents the recommended Homebrew cask install.

## [1.12.1] - 2026-07-13

CI-only maintenance: workflow actions moved to Node 24 runtimes and the
glyphrun specs job runs on Go 1.26.

## [1.12.0] - 2026-07-13

### Changed

- **Studio TUI overhaul.** New Overview tab and theme module, richer
  CPU/Memory/Disk/Network views, an expanded process detail pane, gauge
  widget improvements, availability-aware rendering, and substantially more
  render/safety test coverage.

### Documentation

- Website and installation guide overhauled; the landing page gains brand
  styling, a terminal preview, and SEO metadata.

## [1.11.0] - 2026-07-12

### Added

- `monitor analyze` — bounded cross-signal diagnosis from the CLI.
- `monitor processes` (alias `ps`) — bounded, sortable, filterable process
  inventory.
- `monitor config show` / `monitor config set` — validated, atomic settings
  workflow.

### Changed

- Stronger alert delivery and process telemetry; more durable log capture;
  more responsive Studio diagnostics; improved cleanup error handling;
  expanded behavioral coverage and documentation.

## [1.10.0] - 2026-07-10

### Added

- **Explicit metric availability.** A new capability layer reports which
  metrics are actually collectable instead of silently zeroing them,
  surfaced across the collector, snapshot/investigate/profile, and the MCP
  tools. A compact snapshot payload (`monitor snapshot --compact`,
  `monitor_snapshot {"compact":true}`) bounds process/filesystem lists for
  agent contexts.

### Fixed

- `monitor tree` JSON output preserves the nested parent/child hierarchy.

## [1.9.0] - 2026-07-09

### Added

- **Diagnosis engine.** Deterministic cross-signal diagnoses
  (`memory_leak`/`cpu_spin`/`load`/`gc_pressure`) preserving slope/R² as
  evidence, with confidence mapping and bounded next actions, attached to
  alerts, incident bundles, webhooks, and desktop notifications.
- New read-only `monitor_analyze` MCP tool over windowed sampling;
  `monitor_snapshot` gains a summary + next actions above the raw data;
  `monitor_processes` is bounded with limit/sort/filter and
  total/truncated/reason honesty.
- **Integrity receipts.** Investigate emits typed per-step receipts with a
  `complete|partial` verdict and pprof endpoint ownership verification (macOS
  `sample` fallback); kill reports a verified
  `terminated|still_running|unknown` outcome (force-kill is a suggested next
  action, never automatic); profile/record emit artifact-exists receipts.
- Incidents keep a durable local registry when fcheap archival fails, plus
  `monitor incident resume-stash`; diff/baseline print verdict lines on
  significant deltas. 5 new glyphrun specs.

### Changed

- veclite bumped v0.20.0 → v0.22.1: lock-free read-only opens (CLI search
  never blocks the TUI writer) and an HNSW insert-after-soft-delete panic fix.

### Documentation

- monitorcli.dev launched: VitePress landing page deployed via Vercel with
  logo/favicon/robots/sitemap, a custom 404 page, and an accuracy pass; the
  legacy GitHub Pages deployment is removed.

## [1.8.1] - 2026-06-27

Test-posture release: every user-facing command now has a glyphrun spec
(23 total, 21 in CI — incidents/run/vault added) plus unit tests for the
remaining pure CLI helpers.

## [1.8.0] - 2026-06-27

### Fixed

- **Cross-surface (CLI/TUI/MCP) consistency.** CLI `kill --json` top-level
  `killed` now reflects actual success (it was unconditionally `true`); the
  CLI kill refusal payload is unified with MCP `monitor_kill`
  (`refused:true` + `reason` + `safety`); `kill.Confirmation` carries
  snake_case JSON tags; a single shared protected/system classifier is used
  by the collector and the kill safety gate so serialized `is_protected`
  matches behavior; the TUI shows a "Spared N" notice when a confirmed kill
  skips protected/system PIDs; the `monitor_investigate` MCP description
  matches the real pipeline.

## [1.7.1] - 2026-06-27

### Fixed

- **The studio kill dialog refuses system PIDs**, matching the CLI and MCP.
  A system-owned, non-protected process could previously be terminated from
  the TUI but not the other surfaces, violating the "one classification,
  three surfaces" guarantee.

### Changed

- Docs accuracy audit across the VitePress site; staticcheck-clean; new
  glyphrun specs for tree/baseline/history/diff and unit tests for studio
  rendering, process specs, and the real-HTTP profiler capture path.

## [1.7.0] - 2026-06-27

### Changed

- **Bare `monitor` now prints help** instead of auto-launching the
  full-screen TUI, and **the TUI is now `monitor studio`** (alias `tui`,
  formerly `v2`). The reload-server flags moved from global to studio scope
  (`monitor studio --reload-server` / `--reload-addr`).

## [1.6.0] - 2026-06-27

### Added

- History predicates are pushed into veclite queries and the history
  collection self-prunes (MaxRecords, FIFO eviction).
- cgroup limits are resolved from `/proc/self/cgroup`, walking up to the
  nearest enclosing limit (finds systemd / cgroupns=host limits).
- The TUI wires the UpdateInterval, TemperatureUnit, and CPU/Mem alert
  threshold settings.

### Fixed

- Post-feature-audit remediation (24 confirmed findings, each with a
  regression test): history syncs each batch so a crash no longer loses the
  recording; baseline names reject path traversal; `watch --once` drains
  in-flight stash/webhook/desktop deliveries; cgroup rescales App/Cache
  memory; the Trends tab caches its store query off the render path;
  codemap subprocesses get a 5s timeout; global `--pprof`/`--reload-*` flags
  are no longer dropped after a subcommand.

## [1.5.0] - 2026-06-27

### Added

- `monitor tree [pid]` — the process parent/child hierarchy, with `--json`
  nested output.
- `--pprof <addr>` serves monitor's own `net/http/pprof` for self-profiling.
- Disk-fill and swap-pressure anomaly rules, registered in `watch`.

### Fixed

- `watch` built a lightweight event without disk/process info, so the
  per-process rules (CPU spike, RSS growth) never fired there.

## [1.4.0] - 2026-06-27

### Added

- **Baselines and diff.** `baseline save/list/delete` captures labeled
  snapshots (system metrics, per-PID memory, TCP listeners);
  `diff <baseline>` shows new/gone processes and listeners, memory deltas,
  and cpu/mem/load shifts against the live system or a second baseline.
- **Outbound alerting.** `watch --webhook URL` and `--notify` (desktop
  notifications), plus a threshold rule that gives the config CPU/memory
  alert thresholds teeth.
- **Container/cgroup awareness.** Memory is reported against the cgroup v2
  limit inside containers, with a `[cgroup limit]` badge in the TUI.
- The v2 TUI is finished: mouse support, editable Settings tab, and a Trends
  tab reading the persistent history store.

### Fixed

- History queries panicked on a store with no samples recorded yet; the
  status bar tab hints now read 1–9.

## [1.3.1] - 2026-06-27

### Fixed

- **Released binaries reported version `dev`.** A hardcoded variable in
  `main` clobbered the goreleaser-injected version; the ldflag now sets the
  version directly.

## [1.3.0] - 2026-06-27

### Added

- **Persistent metric history.** `monitor history record/query/list` — a
  veclite-backed time series of cpu/mem/net/disk/load samples with windowed
  summary stats (min/avg/p95/max, trend).
- **Blast-radius ranking.** `investigate` scores each profile frame by
  runtime cost (pprof flat%) × codemap blast radius (transitive callers +
  test coverage), so frames that are both hot and central surface first.
- The `monitor_investigate` and `monitor_record` MCP tools are wired to real
  implementations (previously stubs): investigate shares the CLI pipeline;
  record captures a real screen recording via the platform recorder.
- Push/PR CI: vet, race tests, a build matrix, and glyphrun specs.

## [1.2.0] - 2026-06-27

### Added

- **VitePress docs site** (8 pages, generated from and verified against the
  real source), with `task docs` / `task docs-build`.
- **codemap correlation.** `investigate` resolves each captured profile
  frame's file:line to its enclosing codemap symbol (best-effort, skipped
  when codemap is absent).
- The pprof scrape address is configurable (`profile --pprof-addr`), and CPU
  profiles are symbolicated via `go tool pprof -top -lines` when the Go
  toolchain is present.

### Changed

- Collector sampling moved out from under the mutex — a slow per-PID
  enumeration no longer blocks the TUI render loop.

## [1.1.0] - 2026-06-26

### Fixed

- Full 16-package adversarial audit resolved (67 confirmed findings, each
  high-severity fix with a regression test): `monitor process <pid>` always
  reported "pid not found"; network per-second rates were always 0; CPU
  profiling 404'd; the log-capture cap could deadlock on a full OS pipe;
  process search swallowed `q`/digit/`l`/`h` keystrokes; plus 19 medium and
  40 low findings across MCP kill-safety parity, the collector, temperature,
  logging, ecosystem calls, the profiler parsers, and config saving.

### Changed

- `internal/widgets` migrated to lipgloss v2 so the binary links a single
  lipgloss; README/CLAUDE.md/AGENTS.md rewritten for the v2 surface.

## [1.0.0] - 2026-06-26

### Changed

- **Complete rewrite** from a v1 TUI-only tool into a local-first,
  agent-harnessable observability hub: Bubble Tea v2 TUI with 8 tabs and
  full process-table interactivity; 19 CLI subcommands (snapshot, watch,
  kill, profile, investigate, stash, incidents, logs, doctor, run, reload,
  mcp, vault, …); an MCP stdio server with 7 tools (3 read-only + 4 mutating
  behind a confirm gate); real temperature via `sudo powermetrics` with a
  graceful fallback; fcheap incident stashes; a log-capture pipeline; a
  `/reload` endpoint; CPU-spike/RSS-growth analyzer rules; and tinyvault
  integration. Module renamed to `github.com/abdul-hamid-achik/monitor`;
  Homebrew cask via goreleaser (darwin + linux, amd64 + arm64).
- En route to the rewrite, the v1 TUI gained process search (`/`), a Disk
  tab, JSON export, and configurable CPU/memory alert thresholds.

### Removed

- The legacy v1 TUI and system collector.

## [0.3.0] - 2026-03-10

### Fixed

- Tightened process interactions and mouse hit testing.

## [0.2.0] - 2026-03-06

### Added

- Per-process network monitoring.

### Fixed

- Tab clicking.

## [0.1.0] - 2026-03-06

Initial release: a terminal system monitor for macOS with a Network tab,
Settings documentation, and a GoReleaser + GitHub Actions release workflow.

[1.15.1]: https://github.com/abdul-hamid-achik/monitor/compare/v1.15.0...v1.15.1
[1.15.0]: https://github.com/abdul-hamid-achik/monitor/compare/v1.14.0...v1.15.0
[1.14.0]: https://github.com/abdul-hamid-achik/monitor/compare/v1.13.0...v1.14.0
[1.13.0]: https://github.com/abdul-hamid-achik/monitor/compare/v1.12.1...v1.13.0
[1.12.1]: https://github.com/abdul-hamid-achik/monitor/compare/v1.12.0...v1.12.1
[1.12.0]: https://github.com/abdul-hamid-achik/monitor/compare/v1.11.0...v1.12.0
[1.11.0]: https://github.com/abdul-hamid-achik/monitor/compare/v1.10.0...v1.11.0
[1.10.0]: https://github.com/abdul-hamid-achik/monitor/compare/v1.9.0...v1.10.0
[1.9.0]: https://github.com/abdul-hamid-achik/monitor/compare/v1.8.1...v1.9.0
[1.8.1]: https://github.com/abdul-hamid-achik/monitor/compare/v1.8.0...v1.8.1
[1.8.0]: https://github.com/abdul-hamid-achik/monitor/compare/v1.7.1...v1.8.0
[1.7.1]: https://github.com/abdul-hamid-achik/monitor/compare/v1.7.0...v1.7.1
[1.7.0]: https://github.com/abdul-hamid-achik/monitor/compare/v1.6.0...v1.7.0
[1.6.0]: https://github.com/abdul-hamid-achik/monitor/compare/v1.5.0...v1.6.0
[1.5.0]: https://github.com/abdul-hamid-achik/monitor/compare/v1.4.0...v1.5.0
[1.4.0]: https://github.com/abdul-hamid-achik/monitor/compare/v1.3.1...v1.4.0
[1.3.1]: https://github.com/abdul-hamid-achik/monitor/compare/v1.3.0...v1.3.1
[1.3.0]: https://github.com/abdul-hamid-achik/monitor/compare/v1.2.0...v1.3.0
[1.2.0]: https://github.com/abdul-hamid-achik/monitor/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/abdul-hamid-achik/monitor/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/abdul-hamid-achik/monitor/compare/v0.3.0...v1.0.0
[0.3.0]: https://github.com/abdul-hamid-achik/monitor/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/abdul-hamid-achik/monitor/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/abdul-hamid-achik/monitor/releases/tag/v0.1.0
