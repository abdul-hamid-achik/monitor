# TypeScript source-map heat fixture

E3.1's "hot line in original .ts coordinates" case: a real TypeScript build,
profiled with a real Node CPU profiler, resolved back through its real
source map — not a hand-rolled mapping.

Produced 2026-09-23:

1. `hot.ts` — synthetic, no real project code — was compiled with
   `bun build hot.ts --sourcemap=external --outdir dist` (Bun 1.4.2),
   producing `dist/hot.js` and `dist/hot.js.map`. Like
   `internal/sourcemap/testdata/bun-external`, Bun's `--sourcemap=external`
   emits no `sourceMappingURL` comment, so resolving this pair exercises
   the sibling `<file>.map` discovery step. The map's `sources` entry is
   Bun's own unedited `"../hot.ts"`, which is why `hot.ts` lives one
   directory above `dist/` rather than beside it.
2. `dist/hot.js` was profiled with `node --cpu-prof --cpu-prof-dir=dist
   --cpu-prof-name=hot.cpuprofile dist/hot.js` (Node 26.7). `heavyWork`'s
   positionTicks land overwhelmingly on generated line 5
   (`const s = JSON.stringify(...)`, ~92% of the function's self ticks),
   which `dist/hot.js.map` resolves to `hot.ts:4` — the `// HOT LINE`
   comment marks it.

`dist/hot.cpuprofile`'s two `callFrame.url` occurrences were rewritten from
the real build-machine scratch path to the placeholder
`file:///repo/internal/profiler/testdata/tssrc/dist/hot.js`, the same
convention `../v8-hot.cpuprofile` uses (see `../README.md`) — no `/Users/`
or `/tmp/` path is committed. `heat_test.go`'s source-map wiring test copies
this whole tree into a `t.TempDir()` at the same relative layout and
rewrites that one placeholder to the copy's real path before calling
`LoadFile`, mirroring `internal/sourcemap/fixture_test.go`'s
`copyFixtureTree` pattern (source-map resolution needs a real, stat-able
file on disk; a fixture's own embedded path never is one).
