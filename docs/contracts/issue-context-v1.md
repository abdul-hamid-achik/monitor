# `monitor.issue_context.v1` (Draft)

> **Status: Draft.** This schema is not implemented yet — it is the target
> shape for `explain.Build`, the planned single producer shared by the CLI
> (human, `--json`, `--md`), MCP's `monitor_issue`, and later Studio and
> cortex. Field names and budgets may still change before the first PR that
> implements it (E2.5). See the [naming ADR](./local-sentry-naming) for the
> rules this schema encodes (fingerprint/culprit, `ObservedAt`, `--scan`).

## Why this exists

`monitor issues show <id>` currently returns whatever the `issues` store
holds verbatim (see [Local Issues](/guide/issues)). The one-argument
`monitor issue <id>` this schema targets does not exist yet as a command —
`internal/cli/issues.go` currently registers `issue` only as a cobra
**alias** of `issues`, so today `monitor issue <anything>` just prints the
`issues` command's help. E2.5 has to make `monitor issue <id>` its own
command, taking over that alias rather than adding a second, conflicting
meaning for it (see the naming ADR's `issue`/`issues` collision row); that is
a user-visible CLI change and belongs in the CHANGELOG, not something this
schema doc can silently assume away. Once exceptions carry frames, causes,
and a culprit (§6 of the naming ADR), the issue page needs one producer that
reads the store once, enriches it with codemap/git/vecgrep, and degrades
honestly when any of those are unavailable — instead of every renderer (CLI,
`--md`, MCP) re-implementing that enrichment. `explain.Build` is that
producer. It always opens the store read-only (`issues.OpenReadOnly`), so it
never blocks on — or is blocked by — a writer such as `watch --stash`.

## Budgets

`explain.Build(id, budget)` takes a budget so a caller can ask for less than
the full page. The budget does **not** gate which top-level sections show up
— every section `explain.Build` knows how to fill is present at every
budget, because a "why did it fail" answer that silently drops `impact` or
`last_touched` to hit a size target is worse than a shorter version of the
same answer (see the roadmap's own `monitor_issue {id:"latest"}` brief
example, which carries `culprit.snippet`, `causes`, `impact`, `last_touched`,
`degraded[]`, and `next[]` in under 4 KB). The budget instead gates how much
**detail** each section carries:

| Budget | Target size | Every section is present; budget controls detail |
|---|---|---|
| `brief` | ≤ 4 KB | `culprit.snippet` capped to a few lines around the crash line; `frames` collapsed to in-app frames only (the rest folded into a count); `causes` capped to what fits. Left out entirely: author emails (`last_touched.author_email`) and any `related_notes` item body (a later-epic field — the items themselves, `{title, path}`, are small enough to keep). |
| `standard` | ≤ 16 KB | The same sections as `brief`, with fuller detail: a wider snippet window, the complete in-app frame list, and the full `causes` chain. |
| `full` | unbounded | Everything in `standard`, plus anything a future `--include-raw` opts into (e.g. an uncapped frame list, raw profiler payloads). |

MCP's `monitor_issue` defaults to `brief` so a "what's the latest crash"
question costs one small, cheap call and still gets a real culprit, causes,
impact, and last-touched answer, rather than a stub that needs a second call
to be useful. The CLI's `monitor issue <id>` defaults to `standard`.

## Shape

```jsonc
{
  "schema": "monitor.issue_context.v1",
  "budget": "brief", // "brief" | "standard" | "full"
  "generated_at": "2026-09-22T18:04:11Z",

  // present only when the caller asked for id:"latest" (or a filter) rather
  // than a specific short_id
  "resolved_from": {
    "id": "latest",
    "project": "polyglot",
    "service": "workload",
    "kind": "exception"
  },

  "issue": {
    "id": "ISS-A07E1234567890AB",
    // uppercase first 4 hex chars of the id's hex portion (store.go builds
    // `id` as "ISS-" + strings.ToUpper(fingerprint[:16])). This is a new,
    // purely-display field: today's `issues.Store.Get` only matches the
    // full `id` exactly, so E2.5 also has to teach issue lookup to accept
    // an unambiguous short_id/id prefix, not just the value itself.
    "short_id": "A07E",
    "status": "open", // open | resolved | ignored
    // Issue.Kind is an open string; producers write "exception" (this
    // schema, new), "investigation" (internal/cli/investigate.go), or
    // "monitor.alert.<rule>" (internal/cli/watch.go) — never "alert" bare.
    "kind": "exception",
    "title": "TypeError: Cannot read properties of undefined (reading 'id')",
    "exception_type": "TypeError",
    "handled": false,
    "project": "polyglot",
    "service": "workload"
  },

  "timeline": {
    "first_seen": "2026-09-20T09:11:00Z",
    "last_seen": "2026-09-22T18:03:50Z",
    "occurrences": 37,
    "reopened": 1,
    "first_git_sha": "a1b2c3d",
    "runs": ["CI-4821", "CI-4830"], // MONITOR_LAUNCH_ID / CHALUPA_CI_RUN_ID seen
    "time_source": "line" // "line" | "mtime" | "live" — see naming ADR §4
  },

  "culprit": {
    "function": "loadUser",
    "fqn": "workload/src/users.ts:loadUser",
    "file": "src/users.ts",
    "line": 42,
    "source": "stack", // "stack" | "message_search" — see naming ADR §7
    "via": null, // "vecgrep" | "git_grep", set only when source is message_search
    "confidence": "high", // "high" | "low"
    "range": { "start": 38, "end": 51, "source": "codemap" },
    "snippet": {
      "start": 38,
      "lines": ["function loadUser(id) {", "  return db.users.find(id).name;", "}"],
      "highlight": 42,
      "sha256": "…",
      "stale": false // true when the file's current sha256 no longer matches
    }
  },

  "causes": [
    { "type": "TypeError", "culprit": { "function": "loadUser", "file": "src/users.ts", "line": 42 } }
  ],

  "frames": [
    // in_app frames first; frames outside the git root collapse to a count
    { "function": "loadUser", "file": "src/users.ts", "line": 42, "in_app": true }
  ],

  "impact": {
    "status": "ok", // "ok" | "skipped"
    "callers": 3,
    "blast_radius": 12,
    "tests": 1, // count of tests exercising the culprit line, from codemap's `impact --batch`
    "test_files": ["src/users.test.ts"], // standard/full only: the additive, human-readable list `tests` counts
    "untested": false,
    "call_graph": "confirmed" // codemap's confidence enum
  },

  "last_touched": {
    "status": "ok",
    "sha": "a1b2c3d",
    "subject": "fix: guard against missing user record",
    "author_time": "2026-09-18T14:02:00Z"
    // no author email in `brief`
  },

  "related_notes": {
    "status": "skipped",
    "items": [] // <= 3 items, later epic (N12); never the note body in `brief`
  },

  "degraded": [
    {
      "component": "codemap",
      "state": "schema_skew",
      "detail": "graph db schema v9 is newer than this codemap (supports v7)",
      "recovery": "upgrade the codemap binary; do not run codemap index --reindex"
    }
  ],

  "next": [
    { "cli": "monitor issue a07e --md", "mcp": null, "why": "share this issue as markdown" }
  ],

  "truncated": {},
  // scrubbed: a count of redacted values (scrub.WithValues, E2.2), never
  // the values themselves
  "privacy": { "scrubbed": 0, "text_is_untrusted": true }
}
```

## Degradation

Every section that depends on an external tool — `impact` (codemap),
`last_touched` (git), `culprit.via` (vecgrep or git grep) — reports its own
`status`. A missing or unhealthy dependency produces `status: "skipped"` with
a `detail` and a `recovery` string in `degraded[]`; it never produces an
empty-but-present field that looks like "there was no impact" or "nobody
touched this line". This mirrors the existing `{status, limitation, recovery}`
habit already used by `monitor investigate`'s pipeline steps.

`codemap`'s "index is a newer schema than this binary supports" case reports
`schema_skew` (or, once codemap ships the fix from the roadmap's E1.3b, its
own `schema_newer` code) with the recovery "upgrade the binary, don't
reindex" — never `codemap index --reindex`, which would downgrade the shared
global index.

## Compatibility

- `full` budget frames and `related_notes` bodies are additive to the
  existing `issues.Issue` / `issues.Occurrence` JSON — v1.15 consumers that
  decode today's fields keep working unchanged.
- `suspects` and `similar` (later epics, L1/L3) will be added the same way:
  additive fields with `status: "skipped"` until they exist, never a
  breaking rename.
- This schema does not change `monitor profile --json`; that stays exactly as
  documented in [`line-heatmap-v1`](./line-heatmap-v1), and only
  `investigate --json` / MCP omit `profile.text` by default.
