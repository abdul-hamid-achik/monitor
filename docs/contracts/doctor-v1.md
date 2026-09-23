# `monitor doctor --json` v1 contract

`monitor doctor --json` is a **stable presence contract**, limited to the
tools monitor already probes. It answers three separate questions, each in
its own additive section: is a tool *on PATH at all* (`codemap`, `fcheap`,
...), is codemap/vecgrep *actually usable against the current directory*
right now (`code_intel`), and is the binary monitor is *actually running*
the same one that would resolve from a plain shell invocation
(`binaries`). minerva delegates ecosystem-presence checks to this contract;
cairntrace and Chalupa may also parse it. Every field below is additive —
existing consumers that only read the top-level tool keys are unaffected by
`code_intel` and `binaries`.

## Stability

- The nine top-level tool keys (`codemap`, `fcheap`, `vecgrep`, `tinyvault`,
  `vidtrace`, `glyphrun`, `cairntrace`, `veclite`, `tmux`) and their
  `{available, version, path, note}` shape are unchanged from prior
  releases. Nothing is removed or renamed.
- `code_intel` and `binaries` are new, additive top-level sections. A
  consumer that decodes into a struct with only the nine tool fields keeps
  working unmodified.
- Field additions to `code_intel`/`binaries` themselves will also be
  additive; a `state`/`available` value will never be silently repurposed.

## Top-level shape

```json
{
  "codemap": { "available": true, "version": "...", "path": "..." },
  "fcheap": { "available": true, "version": "...", "path": "..." },
  "vecgrep": { "available": true, "version": "...", "path": "..." },
  "tinyvault": { "available": true, "version": "...", "path": "..." },
  "vidtrace": { "available": true, "version": "...", "path": "..." },
  "glyphrun": { "available": true, "version": "...", "path": "..." },
  "cairntrace": { "available": true, "version": "...", "path": "..." },
  "veclite": { "available": true, "path": "..." },
  "tmux": { "available": true, "path": "..." },
  "code_intel": { "codemap": { "...": "Health, see below" }, "vecgrep": { "...": "Health" } },
  "binaries": {
    "self": { "version": "1.16.0", "path": "/usr/local/bin/monitor" },
    "other_binaries": [ { "...": "BinaryInfo, see below" } ]
  }
}
```

`available: false` on a top-level tool entry carries an optional `note`
(e.g. `"codemap not on PATH"`); it never carries `version`/`path`.

## `code_intel`: is the tool usable *here*, right now?

Presence on PATH (the top-level `codemap`/`vecgrep` keys) says nothing about
whether that binary can actually answer a query against the current
directory. `code_intel.codemap` and `code_intel.vecgrep` close that gap with
a `Health` object:

```json
{
  "tool": "codemap",
  "state": "schema_skew",
  "detail": "graph db schema v9 is newer than this codemap (supports v7); upgrade codemap",
  "recovery": "upgrade the codemap binary (cd ~/projects/codemap && go install ./cmd/codemap); do NOT run codemap index --reindex",
  "version": "codemap version v0.66.0-3-gc8b68c6 (c8b68c6) 2026-09-07T21:46:06Z",
  "path": "/Users/example/go/bin/codemap"
}
```

`detail` and `recovery` are omitted (not empty-stringed) when the state
carries none, so `state == "ok"` is the whole object plus `tool`/`version`/
`path`.

### `state` taxonomy

| State | Meaning | Recovery |
|---|---|---|
| `ok` | The binary is present and the project/branch is indexed and queryable. | — |
| `schema_skew` | The on-disk index was written by a **newer** binary than the one on PATH. The index itself is fine. | Upgrade the binary. **Never** run a reindex — that would rewrite the shared index with an older schema for every other consumer. |
| `index_corrupt` | The index exists but genuinely won't open (not a schema skew: disk/permission/truncation damage). | Back it up, then reindex. |
| `not_indexed` | The tool is healthy, but this project (codemap) or this git branch (vecgrep) has no index yet. | Build one (`codemap index` / `vecgrep index`, or `vecgrep branch switch` first to check for an existing snapshot). |
| `unavailable` | The binary is missing from PATH, the probe timed out (3s), or it failed in a way that doesn't fit the above. | Install the tool, or investigate why it's hanging/crashing. |

### Why `schema_skew` is not `index_corrupt`

codemap keeps one global graph index shared by every project. Today's
codemap CLI (as of this contract's writing) maps *both* "the index is
genuinely broken" and "the index was written by a newer codemap than the
one currently on PATH" to the same `index_corrupt` code, with a hint to
run `codemap index --reindex`. Reindexing in the second case would be
actively harmful: it silently downgrades the shared index's schema for
every other consumer on the machine. `ProbeCodemap`
(`internal/ecosystem/health.go`) recognizes the schema-skew wording in the
wrapped error text (and a forward-looking dedicated `schema_newer` code,
once codemap ships one) and reports `schema_skew` instead, with a recovery
that explicitly says not to reindex.

### vecgrep is per-branch

vecgrep keeps one index per git branch. `code_intel.vecgrep`'s `state`
always reflects the **current** checked-out branch in the probed
directory — `ok` on `main` says nothing about a feature branch that was
never indexed. `not_indexed`'s recovery names both `vecgrep branch switch`
(cheap: activates an existing snapshot for this branch, if any) and
`vecgrep index` (builds one from scratch).

### Degradation and cost

- Each probe has an internal 3-second timeout against the underlying
  `codemap status --json` / `vecgrep status --format json --lightweight`
  call. A hang degrades to `unavailable`, never blocks doctor.
- Results are cached in-process for 60 seconds, keyed by (tool, directory,
  binary path + modification time) — a `go install`/`brew upgrade` of the
  binary invalidates the cache immediately rather than waiting out the TTL.
- `vecgrep status --lightweight` never opens the vector store; `codemap
  status --json --skip-stale` never walks the working tree for drift. Both
  are the cheapest health signal each tool exposes.

## `binaries`: which binary is actually running, and is it shadowed?

```json
{
  "self": { "version": "1.16.0", "path": "/usr/local/bin/monitor" },
  "other_binaries": [
    {
      "name": "glyph",
      "available": true,
      "path": "/Users/example/go/bin/glyph",
      "version": "glyph version dev (unknown unknown)",
      "all_paths": [
        "/Users/example/go/bin/glyph",
        "/opt/homebrew/bin/glyph"
      ],
      "shadowed": true,
      "warning": "glyph resolves to /Users/example/go/bin/glyph; also found on PATH at /opt/homebrew/bin/glyph"
    }
  ]
}
```

- `binaries.self` is the process actually running this `monitor doctor`
  invocation: `version` is the linked build version (`"dev"` for a plain
  `go build`), `path` is `os.Executable()`'s answer (omitted if it can't be
  resolved). It is **not** looked up on PATH — a locally built or `go run`
  binary legitimately differs from whatever `monitor` resolves to on PATH.
- `binaries.other_binaries` is a fixed-order scan of `monitor`, `codemap`,
  `glyph`, `cairn`, and `vecgrep` — the exact binaries monitor shells out to,
  plus monitor itself (to catch a stale installed copy sitting next to a
  worktree build). Each entry is `exec.LookPath`'s answer (`path`,
  `version`) plus every other PATH match (`all_paths`, in PATH search
  order). `shadowed: true` and a human-readable `warning` appear whenever
  `all_paths` has more than one entry — the classic "a dev build in
  `~/go/bin` silently wins over the versioned release in
  `/opt/homebrew/bin`" hygiene bug (see the "one binary per consumer" rule
  in the local-sentry roadmap, E0.3).
- An unavailable binary is just `{"name": "...", "available": false}` — no
  `path`/`version`/`all_paths` noise.

## Compatibility

- `code_intel` and `binaries` are additive; nothing under the nine
  pre-existing top-level tool keys changed shape.
- `--require`/`--strict` (dependency gating, non-zero exit on a missing
  required tool) are unaffected; they still key off the top-level
  `available` booleans, not `code_intel`.
- The `Health` and `BinaryInfo` types (`internal/ecosystem/health.go`,
  `internal/ecosystem/binaries.go`) are exported so a later wave can call
  `ecosystem.ProbeCodemap`/`ProbeVecgrep`/`ProbeCodeIntel`/`ScanBinary`
  directly from `monitor issue`'s correlate step or `monitor hot`, without
  re-deriving this classification.
