# Minified one-line source-map heat fixture

E3.1's "span ambiguity" case: V8 `positionTicks` carry only a LINE, never a
column, and a minified bundle can put an entire function (or the whole
program) on one generated line — so a column guessed only from "the first
non-blank character on the line" cannot honestly be called an *exact*
per-line attribution.

Produced 2026-09-23, the same way as `../tssrc`, except with `--minify`:

1. `hot.ts` is the identical source `../tssrc/hot.ts` uses.
2. `bun build hot.ts --minify --sourcemap=external --outdir dist` (Bun
   1.4.2) collapses the whole program — `heavyWork` (renamed `r` by the
   minifier) and the top-level `while` loop — onto a single generated line.
3. `dist/hot.js` was profiled with `node --cpu-prof --cpu-prof-dir=dist
   --cpu-prof-name=hot.cpuprofile dist/hot.js` (Node 26.7). Every
   `positionTicks` entry for `r` lands on generated line 1 — there is no
   other line to land on.

Resolving generated line 1 at its first non-blank column (`function r(n){`)
lands on `hot.ts:1`; resolving the SAME line at its last non-blank column
(`r(3000);`, near the end of the 161-character line) lands on `hot.ts:11` —
verified directly against `internal/sourcemap.Resolver` before this fixture
was committed. `heat.Build` must therefore report `mapping: "ambiguous"`
for `r`'s lines, never `"exact"`, and add a warning naming the one-line/
minified bundle.

`dist/hot.cpuprofile`'s one `callFrame.url` occurrence was rewritten from
the real build-machine scratch path to the placeholder
`file:///repo/internal/profiler/testdata/tssrc-min/dist/hot.js`, the same
convention `../tssrc` and `../v8-hot.cpuprofile` use — no `/Users/` or
`/tmp/` path is committed. `heat_test.go`'s
`TestBuildHeatmapMinifiedOneLineBundleMarksAmbiguousNotExact` copies this
whole tree into a `t.TempDir()` at the same relative layout and rewrites
that placeholder to the copy's real path before calling `LoadFile`,
mirroring `../tssrc`'s own test.
