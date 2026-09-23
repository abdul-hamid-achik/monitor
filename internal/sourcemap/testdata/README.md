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

Only synthetic fixtures live here: no real application code, logs, or names.
