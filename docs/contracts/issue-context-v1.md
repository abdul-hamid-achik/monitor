# `monitor.issue_context.v1`

> **Status: Implemented (E2.5/E2.7/E2.8).** Produced by `internal/explain.
> Build` and consumed by `monitor issue <id|short-prefix|latest>` (human,
> `--json`, `--md`) and MCP's `monitor_issue` (additively, alongside its
> pre-E2.7 `{issue, occurrences, occurrences_truncated}` shape — see
> "Compatibility" below). Studio and cortex are still later work. See the
> [naming ADR](./local-sentry-naming) for the rules this schema encodes
> (fingerprint/culprit, `ObservedAt`, `--scan`). A few shape details were
> settled during implementation and are called out inline below, since the
> draft explicitly left them open.

## Why this exists

Before E2.5, `monitor issues show <id>` returned whatever the `issues` store
held verbatim (see [Local Issues](/guide/issues)), and `internal/cli/
issues.go` registered `issue` only as a cobra **alias** of `issues`, so
`monitor issue <anything>` just printed the `issues` command's help. E2.5
gave `monitor issue <id>` its own command, taking over that alias (see the
naming ADR's `issue`/`issues` collision row) — `monitor issues list/show/
resolve/reopen/ignore` keep working exactly as before. Once exceptions carry
frames, causes, and a culprit (§6 of the naming ADR), the issue page needs
one producer that reads the store once, enriches it with codemap/git/
vecgrep, and degrades honestly when any of those are unavailable — instead
of every renderer (CLI, `--md`, MCP) re-implementing that enrichment.
`explain.Build` is that producer. It always opens the store read-only
(`issues.OpenReadOnly`), so it never blocks on — or is blocked by — a writer
such as `watch --stash`.

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
| `brief` | ≤ 4 KB | `frames` capped to 5 in-app entries (the rest counted in `truncated.frames`); `causes` capped to the 2 closest to the innermost cause (the rest counted in `truncated.causes`); `last_touched.author_email` always omitted. A pathologically long culprit/snippet still over budget after those fixed caps is shrunk further (snippet lines first, then frames, then causes) until it fits — see `truncated` below. |
| `standard` | ≤ 16 KB | The same fixed caps do not apply — every in-app frame (already bounded to 12 at ingest) and every cause (bounded to 3) — but the same shrink-until-it-fits backstop still runs if 16 KB is somehow exceeded. |
| `full` | unbounded | Everything `standard` has; the backstop never runs. |

Both budgets currently read the same ±4-line snippet window around the
culprit — the draft's "a wider snippet window" distinction for `standard`
was not implemented; `truncated` is what actually shows whether anything
was cut. MCP's `monitor_issue` defaults to `brief` so a "what's the latest
crash" question costs one small, cheap call and still gets a real culprit,
causes, impact, and last-touched answer, rather than a stub that needs a
second call to be useful. The CLI's `monitor issue <id>` defaults to
`standard`.

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
    // "line" | "mtime" | "live" — see naming ADR §4. Neither issues.Issue
    // nor issues.Occurrence PERSISTS which of the three produced an
    // occurrence's ObservedAt yet (that provenance belongs to the E2.1/E2.4
    // producers, `stacktrace parse --record` and `monitor run --`, on a
    // parallel branch not merged here); until a producer-side field exists,
    // this is a documented placeholder that always reads "live".
    "time_source": "live"
  },

  "culprit": {
    "function": "loadUser",
    // fqn is set only when codemap resolved the symbol (ProbeCodemap
    // HealthOK and CodemapSymbolAt found a real range) — omitted, not
    // null, otherwise, like every other omitempty field below.
    "fqn": "workload/src/users.ts:loadUser",
    "file": "src/users.ts",
    "line": 42,
    "source": "stack", // "stack" | "message_search" — see naming ADR §7
    "via": "vecgrep", // "vecgrep" | "git_grep" — omitted when source is "stack"
    "confidence": "high", // "high" | "low"
    // range.source is "codemap" when CodemapSymbolAt resolved it, or
    // "frame" for a synthetic +-4-line window around the culprit line when
    // codemap is unavailable/unhealthy or found nothing there.
    "range": { "start": 38, "end": 51, "source": "codemap" },
    "snippet": {
      "start": 38,
      "lines": ["function loadUser(id) {", "  return db.users.find(id).name;", "}"],
      "highlight": 42,
      "sha256": "…", // sha256 of the snippet SLICE (the joined lines above), not the whole file
      // true when git blame's last-touching commit for this line differs
      // from Issue.FirstGitSHA (the file changed since this issue was
      // first recorded) -- always false when either SHA is unknown, which
      // is the common case for an ordinary local dev session with no
      // MONITOR_GIT_SHA/GIT_SHA/GITHUB_SHA set (see Issue.FirstGitSHA).
      "stale": false
    }
  },
  // culprit is entirely absent (omitted, not null) when the exception chain
  // has no in_app frame anywhere AND the message-search fallback also found
  // nothing to blame -- see "degraded" for why in that case.

  "causes": [
    { "type": "TypeError", "culprit": { "function": "loadUser", "file": "src/users.ts", "line": 42 } }
  ],

  "frames": [
    // in_app frames only, closest to the crash first; how many more were
    // cut (brief's 5-frame cap, or the ingest-time 12-frame cap) is in
    // truncated.frames below, never inlined per-entry here
    { "function": "loadUser", "file": "src/users.ts", "line": 42, "in_app": true }
  ],

  "impact": {
    "status": "ok", // "ok" | "skipped"
    "callers": 3,
    "blast_radius": 12,
    "tests": 1, // count of tests exercising the culprit line, from a single codemap `impact --at` call
    "test_files": ["src/users.test.ts"], // best-effort, additive; may be shorter than `tests` if codemap's own per-entry shape couldn't be parsed
    "untested": false,
    "call_graph": "confirmed" // codemap's confidence enum
  },

  "last_touched": {
    "status": "ok",
    "sha": "a1b2c3d",
    "subject": "fix: guard against missing user record",
    "author_time": "2026-09-18T14:02:00Z"
    // author_email omitted entirely at brief budget; present at standard/full
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
  // sorted by component name; deduplicated -- codemap being unhealthy is
  // reported once even though both culprit.fqn/range AND impact depend on it

  "next": [
    { "cli": "monitor issue a07e --md", "why": "paste-ready fix context for your agent" }
  ],

  "truncated": {}, // e.g. {"frames": 7} when brief's cap (or the size backstop) dropped 7 frames; {"causes": N} likewise; omitted keys mean nothing was cut
  // scrubbed: a count of values a defense-in-depth scrub pass redacted from
  // this READ (title/snippet/commit subject) -- separate from, and on top
  // of, whatever internal/scrub already redacted before this text was ever
  // persisted (E2.2)
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

`degraded[]` can carry a `vecgrep` entry even when `culprit.source` is
`message_search` and a culprit WAS found via `via: "git_grep"`: vecgrep not
being ready for the project's current branch is worth surfacing on its own
(with its own recovery) regardless of whether the git grep fallback happened
to succeed anyway — the roadmap's own wording is "en una rama sin índice,
degraded incluye la recuperación".

## Compatibility

- `full` budget frames and `related_notes` bodies are additive to the
  existing `issues.Issue` / `issues.Occurrence` JSON — v1.15 consumers that
  decode today's fields keep working unchanged.
- MCP's `monitor_issue` (E2.7) merges this schema's fields ADDITIVELY into
  its existing response, keyed exactly as above (`schema`, `budget`,
  `generated_at`, `resolved_from`, `timeline`, `culprit`, `causes`,
  `frames`, `impact`, `last_touched`, `related_notes`, `degraded`, `next`,
  `truncated`, `privacy`), alongside its pre-E2.7
  `{issue, occurrences, occurrences_truncated}` shape. The one exception is
  this schema's OWN `issue` field (the small `{id, short_id, status, kind,
  title, exception_type, handled, project, service}` summary above): its
  key is reserved by the pre-existing, richer `issue` field (the full
  `issues.Issue`, unchanged), so `short_id` is only ever visible via the
  CLI/`--json`'s dedicated `monitor issue` page, not spread into
  `monitor_issue`'s response.
- `id:"latest"` (E2.7) is a NEW accepted value for `monitor_issue`'s `id`
  field and the CLI's `monitor issue` positional argument — additive to
  the existing literal-id/prefix behavior, never a required change for an
  existing caller that always passes a real id.
- A "latest" (or filtered) query matching nothing returns
  `{not_found: true, recovery: "..."}` — never an `error` field or a
  protocol-level failure; an ordinary, expected outcome (a healthy project
  between crashes), not a broken tool.
- `suspects` and `similar` (later epics, L1/L3) will be added the same way:
  additive fields with `status: "skipped"` until they exist, never a
  breaking rename.
- This schema does not change `monitor profile --json`; that stays exactly as
  documented in [`line-heatmap-v1`](./line-heatmap-v1), and only
  `investigate --json` / MCP omit `profile.text` by default.
