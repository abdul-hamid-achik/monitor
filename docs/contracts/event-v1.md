# `monitor.event.v1`: the SDK event (Draft)

Monitor's own SDKs (`@monitorcli/sdk` for Node and Bun, `monitorcli` for
Python, `github.com/abdul-hamid-achik/monitor/sdk/go`) report errors as
`monitor.event.v1` documents. Anything else may write them too: the format is
small, and the CLI validates every field it reads.

An event describes one error, or one message. It is **not** an issue:
monitor parses its stack with the same parsers it uses on process output,
scrubs it, fingerprints it (V2) and groups it, so an event and the same crash
printed on stderr become one issue.

## Transport: one file per event

There is no server and no socket. A producer writes each event as one JSON
file, atomically:

1. write the document to `.<name>.tmp` in the target directory (mode `0600`);
2. rename it to `<name>.json`.

`<name>` is `<unix-nanoseconds, 19 digits zero-padded>-<pid>-<event_id>`, so a
name sort is oldest first. Readers ignore dot-files, and a drain removes a
`.tmp` file older than an hour (a writer killed mid-write).

The target directory, in order:

| Directory | When |
|---|---|
| an explicit directory the app configured | the app chose one |
| `$MONITOR_EVENTS_DIR` | the process runs under `monitor run --`, which sets it per launch (mode `0700`) |
| `$XDG_STATE_HOME/monitor/events/inbox` (default `~/.local/state/monitor/events/inbox`) | everywhere else, created `0700` |

A producer falls back down this list when a write fails. Directories it
creates are `0700`. A launch directory disappears when its launch ends, so a
process that outlives `monitor run` lands in the inbox.

Who reads them:

- `monitor run --` reads its launch directory live and merges each event with
  the same crash parsed from stderr (see [Merging](#merging-with-process-output)).
- `monitor issues`, `monitor issue` and `monitor serve` (not with `--read-only`)
  drain the inbox into the **default** issue store. A command pointed at
  another store (`--store`, `MONITOR_ISSUES_STORE`) never touches the inbox.
- `monitor events drain [--store PATH] [--dir PATH]` drains explicitly.

A drained file is deleted once the store has the event. A file that can never
be recorded (bad JSON, wrong `$schema`, nothing to record) moves to
`<dir>/.rejected/`, which keeps the newest 20.

## Document

```jsonc
{
  "$schema": "monitor.event.v1",          // required, exact
  "event_id": "9f1c…",                    // [A-Za-z0-9_-]{1,64}; dedupe key
  "timestamp": "2026-10-02T09:00:00.123Z",// RFC 3339; when the error happened
  "kind": "uncaught",                     // required, see below
  "handled": false,                       // optional; derived from kind when absent
  "level": "fatal",                       // fatal | error | warning | info
  "runtime": "node",                      // node | bun | deno | python | ruby | go
  "error": {                              // an error, or…
    "type": "Error",
    "value": "queue worker crashed",
    "stack": "Error: queue worker crashed\n    at drainQueue (/app/worker.js:35:9)\n…",
    "frames": [],                         // structured alternative to stack
    "cause": { "type": "RangeError", "value": "queue overflow", "stack": "…" }
  },
  "message": "cache rebuilt",             // …or a message (kind "message")
  "tags": { "region": "mx" },
  "breadcrumbs": [
    { "timestamp": "…", "category": "console", "level": "info", "message": "booting" }
  ],
  "service": "api",
  "release": "1.4.0",
  "environment": "dev",
  "cwd": "/home/dev/app",                 // resolves the project for inbox events
  "pid": 4242,
  "launch_id": "…",                       // $MONITOR_LAUNCH_ID, when set
  "sdk": { "name": "monitor.node", "version": "0.1.0", "mode": "auto" }
}
```

### `kind`

| Kind | Meaning | Default `handled` | Default `level` |
|---|---|---|---|
| `uncaught` | an exception that ended the process | `false` | `fatal` |
| `rejection` | an unhandled promise rejection | `false` | `error` |
| `thread` | an exception that ended a thread | `false` | `error` |
| `logged` | an error the app caught and logged | `true` | `error` |
| `captured` | an error the app passed to the SDK | `true` | `error` |
| `message` | a message the app passed to the SDK | `true` | `info` |

### `error`

- `stack` is the runtime's **own** text for this one error, exactly as the
  runtime formats it: V8's `err.stack`, or Python's
  `traceback.format_exception(type, value, tb, chain=False)`. Monitor parses
  it with the same parsers it uses on stderr, which keeps grouping identical
  and keeps source-mapped positions intact. The parsed type and message win
  over `type` and `value` when the stack parses.
- `frames` is for runtimes with no stack text (Go): `{function, module,
  filename, lineno, colno}`, oldest first, with the frame closest to the
  fault **last**. `frames` wins over `stack` when both are present.
- `cause` nests the chain, outer to inner. A chain is cut at 8 levels.
  Without a `cause`, a chain the stack text itself carries (a full Python
  traceback) is used instead.

### Bounds

| Field | Bound | Over the bound |
|---|---|---|
| the whole file | 512 KiB | rejected |
| `stack` | 64 KiB | cut |
| `value`, `message` | 8 KiB | cut |
| short fields (`type`, `service`, …) | 256 bytes | cut |
| `frames` | 256 per error | the oldest dropped |
| `cause` depth | 8 | cut |
| `breadcrumbs` | 100 read, 30 stored (newest) | the oldest dropped |
| a breadcrumb `message` | 300 characters stored | cut |
| `tags` | 64 read, 24 stored | more than 64 discards all; key 64, value 200 characters |

### What an event never contains

The SDKs never put local variables, function arguments, `argv`, environment
variables, request headers or bodies, or cookies in an event. Monitor scrubs
every free-text field it stores anyway (the error's type, message and function
names, breadcrumb messages, tag keys and values): secret shapes, emails,
card numbers, and the values of secret-named environment variables in
monitor's own environment.

## Merging with process output

Under `monitor run --`, the same crash usually arrives twice: once as an
event, once as text on stderr. Both are fingerprinted with V2 and meet in
the same 2-second coalescing window, and the window records **one**
occurrence:

- its count is the larger of the two sources' counts (each saw every
  repeat);
- the event's data wins: its exact `handled` flag, unabridged frames,
  breadcrumbs, tags, `pid` and `release`;
- the stderr block still supplies the DedupeKey nested launches fold on.

An event no stderr text matches (an error logged to a file, a rejection the
app's own listener handled) is recorded on its own.

Outside `monitor run`, the event id is the DedupeKey, so delivering the same
file twice folds into one occurrence.

## Where it shows up

`monitor issue <id> --json` (`monitor.issue_context.v1`) carries an additive
`event` section built from the newest occurrence an SDK delivered: `kind`,
`sdk`, `mode`, `release`, `tags` and `breadcrumbs`. The occurrence itself
keeps `breadcrumbs`, `tags` and `metadata.source = "sdk"`.

## Compatibility

Additive only within v1: new optional fields and new `kind` values may
appear. A reader must ignore fields it does not know. An unknown `kind` is
rejected, so a producer must not invent one.
