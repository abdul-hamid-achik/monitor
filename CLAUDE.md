# CLAUDE.md - Guide for Claude Code (and other coding agents)

This file is the Claude Code companion to AGENTS.md. Read both before making
changes. When in doubt, follow AGENTS.md (it is more comprehensive).

## TL;DR

- Build with `task build` (one word). Tests: `task test`. Specs: `task specs`.
- Module: `github.com/abdul-hamid-achik/monitor`
- Run the CLI: `./bin/monitor --help` for subcommands
- Do not edit files you haven't read in this session
- Prefer adding tests alongside every change
- Every PR should pass `task check` (tidy + lint + test + release build)

## Before making changes

1. `ls internal/` to see what packages exist
2. `cat internal/<pkg>/<file>.go` for the file you intend to edit (mandatory)
3. For new packages, follow the conventions in `internal/collector/` (small,
   focused, with a `_test.go`)

## Single-word Taskfile commands

| Command | Effect |
|---------|--------|
| `task build` | Build to bin/monitor |
| `task release` | Build optimized release binary |
| `task run` | Build and launch the TUI |
| `task test` | Run all unit tests |
| `task cover` | Generate HTML coverage report |
| `task lint` | go vet ./... |
| `task fmt` | gofmt -w . |
| `task tidy` | go mod tidy |
| `task specs` | Run all glyphrun specs |
| `task docs` | Serve the VitePress docs site (`docs/`) locally |
| `task docs-build` | Build the docs site (fails on dead links) |
| `task check` | Full CI (tidy + lint + test + release) |
| `task snapshot` | Shortcut: print JSON system snapshot |
| `task doctor` | Shortcut: print ecosystem health |
| `task clean` | Remove build artifacts |

## Package layout (mental model)

- `internal/collector/` — pub/sub metric collector (canonical pattern)
- `internal/cli/` — cobra subcommands (snapshot, watch, kill, doctor, mcp, ...)
- `internal/analyzer/` — anomaly detection rules
- `internal/logger/` — veclite-backed log store
- `internal/profiler/` — pprof + sample capture
- `internal/capture/` — process stdout/stderr → veclite log capture
- `internal/history/` — veclite-backed persistent metric time-series (`monitor history`)
- `internal/incidents/` — fcheap content-addressed incident stash
- `internal/reload/` — localhost HTTP `/reload` endpoint
- `internal/temperature/` — real SMC temperature via powermetrics (graceful fallback)
- `internal/ecosystem/` — CLI wrappers for codemap, fcheap, tvault, etc.
- `internal/mcp/` — MCP stdio server
- `internal/kill/` — safe process termination
- `internal/ui/v2/` — the TUI (Bubble Tea v2 / charm.land; **default** bare
  `monitor`; all 8 tabs with full interactivity). v1 has been removed.
- `internal/widgets/` — sparklines, gauges (lipgloss v2)
- `internal/config/` — JSON settings at ~/.config/monitor/config.json
- `specs/` — glyphrun behavioral specs
- `cmd/monitor/main.go` — entry point; dispatches CLI vs TUI

## When adding a feature

1. Place new code in the appropriate package (see mental model above)
2. If it's a CLI feature, add to `internal/cli/<feature>.go` and register in `root.go`
3. Always add a test in the same package (`<feature>_test.go`)
4. If the feature has observable behavior, add a glyphrun spec in `specs/`
5. Run `task check` before declaring done

## Coding style

- Go 1.25+
- Run `task fmt` before committing
- Prefer `context.Context` first parameter for IO functions
- Use `JSONOutput(cmd) bool` to switch human vs JSON output in CLI commands
- Errors: return immediately, wrap with `fmt.Errorf("...: %w", err)`
- Never use `os.Exit` in library code; only in `main.go` and CLI entry points

## Things to avoid

- Don't reintroduce Bubble Tea v1 — the v1 TUI and `internal/system/` were
  removed; the only TUI is `internal/ui/v2/` on `charm.land/*`
- Don't add new dependencies without checking existing patterns first
- Don't run the TUI in tests (it requires a real terminal)
- Don't call `cobra.Execute()` from tests (use `Root().Commands()` to inspect)
- Don't merge a tab's render + key handler into `app.go` (keep them in
  `tab_<name>.go`; `app.go` is just the router)

## Testing checklist

Before finishing, ensure:

- [ ] `task test` passes
- [ ] `task lint` passes
- [ ] `task build` succeeds
- [ ] For CLI features: `./bin/monitor <feature> --help` works
- [ ] For MCP tools: registered with proper `*mcp.CallToolRequest` second arg
- [ ] For mutating MCP tools (`monitor_kill`, `monitor_profile_capture`,
  `monitor_investigate`, `monitor_record`): the typed input schema requires
  `confirm: true`. The MCP SDK rejects calls that omit `confirm` outright,
  and handlers re-check `confirm` so hand-built requests still get a
  structured `refused: true` payload. Test the refusal path as well as the
  happy path.
- [ ] For spec features: `task specs` runs and validates

## Quick debug commands

```bash
# Inspect the CLI surface
./bin/monitor --help

# Dump a JSON snapshot
./bin/monitor snapshot --json | jq '.cpu'

# Check ecosystem health
./bin/monitor doctor --json

# Validate glyphrun specs
for spec in specs/*.yml; do ~/projects/glyphrun/bin/glyph spec verify "$spec"; done

# Run one spec
~/projects/glyphrun/bin/glyph run specs/version.yml --format md
```

## Reference: key files

- `cmd/monitor/main.go` — entry point
- `internal/cli/root.go` — subcommand registration
- `internal/collector/collector.go` — pub/sub collector (canonical pattern)
- `internal/ecosystem/registry.go` — CLI wrapper pattern (canonical)
- `internal/mcp/server.go` — MCP server (canonical pattern)
- `specs/version.yml` — minimal spec template

## Common mistakes to avoid

- Confusing `Status` (struct) and `Probe` (function) in `internal/ecosystem`
- Using `WithSharedRead(true)` on the veclite writer (only readers can)
- Forgetting `*mcp.CallToolRequest` as second arg in MCP tool handlers
- Calling `cobra --version` in tests (calls `os.Exit`)
- Re-deriving metric types instead of using `internal/collector/` (the canonical source)

## When stuck

- Check `AGENTS.md` for the broader project context
- Check `~/notes/projects/monitor/` for the design notes
- Read existing code in the package you intend to modify
- Don't guess — search first