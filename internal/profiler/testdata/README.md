# Profiler fixtures

Captured on macOS (Darwin 25.5, arm64) on 2026-09-22 during the local-Sentry
dogfood run. Local paths were rewritten to `/repo/internal/profiler/testdata/src/`
(the source files the profiles were taken from live in `src/`).

| File | Producer | What it proves |
|---|---|---|
| `v8-hot.cpuprofile` | `node --cpu-prof src/hot.js` (Node 26.7) | `positionTicks`: `heavyStringify` is declared on line 1 (`lineNumber` 0) but its self ticks land on **line 5** (`s += JSON.stringify(o)`, ~66% of the function) and line 4 (~33%). |
| `bun-cpu-prof.cpuprofile` | `bun --cpu-prof src/hot.js` (Bun 1.4.2) | Bun writes V8-format `.cpuprofile` files with `positionTicks`; hot line 5. |
| `bun-cpu-prof.md` | `bun --cpu-prof-md src/hot.js` | Bun's markdown profile output (reference only). |
| `jsc-trackingComplete.json` | Bun inspector `ScriptProfiler.trackingComplete` over WebKit protocol | Shape of JSC live-profiling data (`src/loop.js`). Bun does not serve `/json/list`. |
| `darwin-sample-go.txt` | `/usr/bin/sample <pid>` on a Go binary without pprof (`src/gowork.go`) | Real multi-thread `sample` output with `+ ! : \|` tree prefixes; `main.heavyStringify` must be found. |
| `py-probe-window.folded` | prototype Python sampling probe on `src/pywork.py` | Collapsed stacks with per-line frames. |
| `idle.cpuprofile` | hand-built (E3.1) | 98% `(idle)`, 2% `poll` (`src/idle.js`) — `monitor hot`'s AC-5 "mostly idle: slowness is off-CPU" warning, from a small, exact fixture rather than a real capture (idle time isn't reliably reproducible on demand). |

`tssrc/` (E3.1, E3.3a wiring) is a separate, self-contained fixture — a real
`bun build --sourcemap=external` output profiled with a real `node
--cpu-prof` — documented in its own `tssrc/README.md`.
