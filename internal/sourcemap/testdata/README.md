# Source map fixtures

`greeter.ts` is a small synthetic TypeScript file (no real project code, no
real names). It was compiled with `bun build greeter.ts --sourcemap=<mode>
--outdir <dir>` (Bun 1.4.2) on 2026-09-22 to produce two independent, real
Source Map v3 outputs:

| Dir | Bun flag | What it proves |
|---|---|---|
| `bun-external/` | `--sourcemap=external` | A sibling `<file>.map` with no `sourceMappingURL` comment in the generated file (Bun does not emit one in this mode) — exercises the sibling-file discovery step. |
| `bun-inline/` | `--sourcemap=inline` | A `//# sourceMappingURL=data:application/json;base64,...` comment carrying the whole map inline — exercises the comment + `data:` URL discovery step. |

Both maps' `sources` field is the real, unedited `"../greeter.ts"` Bun wrote,
which is why `greeter.ts` lives one directory up from both build outputs
instead of being duplicated into each — the layout mirrors how Bun was
actually invoked (source file one level above `--outdir`).

Generated line 9, col 10 (`function main() {`) is used by the tests as the
known-good position: it maps exactly to `greeter.ts:14:10`, the `main`
identifier in `function main(): void {}`.

## `tsc/`

The same `greeter.ts` (copied to `tsc/src/greeter.ts`), compiled with a real
`tsc` (TypeScript 5.9.3, run as `bun <path-to-typescript>/bin/tsc --target
ES2020 --module commonjs --sourceMap --outDir tsc/dist tsc/src/greeter.ts`
on 2026-09-22 — `bunx tsc` doesn't work in this environment because no
Node.js version is asdf-selected, so the compiler is invoked directly
through `bun` instead). Unlike bun's own `--sourcemap=external`, `tsc`
*does* emit a `//# sourceMappingURL=greeter.js.map` comment, so this
fixture is what exercises that relative-comment discovery path with a real,
independent compiler rather than only Bun's own. Generated line 11, col 10
(`function main() {`) maps to the same `greeter.ts:14:10`.

## `bun-inline-big/`

`big.ts` is a synthetic file with 80 tiny generated functions — still no
real project code — compiled with `bun build big.ts --sourcemap=inline
--outdir out` on 2026-09-22. The point of this fixture is purely its size:
the resulting inline `//# sourceMappingURL=data:...` comment is one line of
about 24 KB, several times past the fixed 8 KB tail window this package
used to search when discovering a source map, which silently failed to
find any inline comment past roughly the first 6 KB of map data. Generated
line 322, col 10 (`function main() {`) maps to `big.ts:405:10`.

Only synthetic fixtures live here: no real application code, logs, or names.
