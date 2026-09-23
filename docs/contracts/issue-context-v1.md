# `monitor.issue_context.v1` (Draft)

> **Status: Draft.** This schema is not implemented yet — it is the target
> shape for `explain.Build`, the planned single producer shared by the CLI
> (human, `--json`, `--md`), MCP's `monitor_issue`, and later Studio and
> cortex. Field names and budgets may still change before the first PR that
> implements it (E2.5). See the [naming ADR](./local-sentry-naming) for the
> rules this schema encodes (fingerprint/culprit, `ObservedAt`, `--scan`).

## Why this exists

`monitor issue <id>` currently returns whatever the `issues` store holds
verbatim (see [Local Issues](/guide/issues)). Once exceptions carry frames,
causes, and a culprit (§6 of the naming ADR), the issue page needs one
producer that reads the store once, enriches it with codemap/git/vecgrep, and
degrades honestly when any of those are unavailable — instead of every
renderer (CLI, `--md`, MCP) re-implementing that enrichment. `explain.Build`
is that producer. It always opens the store read-only
(`issues.OpenReadOnly`), so it never blocks on — or is blocked by — a writer
such as `watch --stash`.

## Budgets

`explain.Build(id, budget)` takes a budget so a caller can ask for less than
the full page:

| Budget | Target size | Includes |
|---|---|---|
| `brief` | ≤ 4 KB | `issue`, `culprit` (no snippet body), `timeline` summary, `degraded[]`. No author emails, no `related_notes` body, no `frames`. |
| `standard` | ≤ 16 KB | Everything in `brief`, plus `snippet`, `causes`, `frames`, `impact`, `last_touched`, `next[]`. |
| `full` | unbounded | Everything in `standard`, plus anything a future `--include-raw` opts into (e.g. uncapped frame list). |

MCP's `monitor_issue` defaults to `brief` so a "what's the latest crash"
question costs one small, cheap call; the CLI's `monitor issue <id>` defaults
to `standard`.

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
    "project": "graphite",
    "service": "web-api",
    "kind": "exception"
  },

  "issue": {
    "id": "ISS-0123456789ABCDEF",
    "short_id": "GRA-42",
    "status": "open", // open | resolved | ignored
    "kind": "exception", // exception | alert (existing FingerprintV1 issues)
    "title": "TypeError: Cannot read properties of undefined (reading 'id')",
    "exception_type": "TypeError",
    "handled": false,
    "project": "graphite",
    "service": "web-api"
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
    "fqn": "web-api/src/users.ts:loadUser",
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
    "tests": ["src/users.test.ts"],
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
    { "cli": "monitor issue GRA-42 --md", "mcp": null, "why": "share this issue as markdown" }
  ],

  "truncated": {},
  "privacy": { "scrubbed": true, "text_is_untrusted": true }
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
