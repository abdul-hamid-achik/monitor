# `monitor.app.v1`: the desktop app protocol (Draft)

`monitor serve --stdio` speaks `monitor.app.v1` to a desktop client such as
Monitor Desktop. It is JSON-RPC 2.0 with one message per line on
stdin/stdout.

The same command serves local and remote clients:

```sh
monitor serve --stdio                 # a local client spawns it directly
ssh dev-box monitor serve --stdio     # a remote client spawns it over SSH
```

Because stdio is the only transport, nothing listens on a port. Over SSH,
the SSH connection provides authentication and encryption. The server stays
alive only as long as its client: when stdin reaches EOF, the server answers
every request still in flight and exits 0.

## Guarantees

- **Stdout carries protocol messages only.** Diagnostics go to stderr.
- **The server is never the only write path.** Writes go through the same
  short-lived store writers the CLI uses, and reads open the stores
  read-only. Running `monitor` commands beside the server is always safe.
- **Process text is untrusted.** Every answer that carries error or log
  text includes `privacy: {scrubbed, text_is_untrusted: true}`, and the text
  has been scrubbed of secret patterns and the server's own secret
  environment values. A client must render this text as plain text, never
  as markup.
- **No inspector URLs.** `launches.list` reports inspector **ports** only.
  The full `ws://` URL never leaves the server.
- **Destructive methods require confirmation.** `process.kill` and
  `profile.capture` are refused unless `"confirm": true` is set. Protected
  and system processes are refused even when `confirm` is true.
- **`--read-only` rejects every mutating method.** Those methods are also
  left out of `hello`.
- **Changes are additive.** New methods, topics, and fields can be added.
  Existing ones are never renamed or repurposed within v1.

## Messages

Requests may run concurrently, so responses can arrive out of order. Match
each response to its request by `id`.

```json
{"jsonrpc":"2.0","id":7,"method":"issues.list","params":{"statuses":["open"]}}
{"jsonrpc":"2.0","id":7,"result":{"items":[...],"total":3,"truncated":false,"privacy":{...}}}
```

### `hello`

The server's first line is always a `hello` notification. A client can also
call `hello` as a method.

```json
{"jsonrpc":"2.0","method":"hello","params":{
  "protocol":"monitor.app.v1","monitor_version":"2.2.0","os":"darwin","arch":"arm64",
  "hostname":"dev-box","read_only":false,
  "capabilities":["doctor","heatmap","host","incidents","issues","issues.write","launches","logs","process.kill","profile","session"],
  "methods":["doctor","heatmap.file","hello","host.snapshot", "..."],
  "topics":["host","issues"]}}
```

Check `protocol` first. Then use `methods` and `topics` to decide which
views to enable.

## Methods

| Method | Params | Result |
|---|---|---|
| `ping` | none | `{pong: true}` |
| `issues.list` | `statuses[]`, `project`, `service`, `kind`, `release`, `run_id`, `since`, `until`, `query`, `limit` (default 200, max 1000) | `{items: Issue[], total, truncated, privacy}`. Newest activity first. `query` is a case-insensitive substring match on the id, title, message, exception type, project, service, or culprit. |
| `issues.get` | `id` (an id, a short id or prefix, or `"latest"`), `budget` (`brief`, `standard`, or `full`; default `full`), `markdown`, and for `"latest"`: `project`, `service`, `kind` | `{context: monitor.issue_context.v1, root, markdown?}` or `{not_found: true, recovery}`. `root` is the checkout the issue was recorded under, so `root + culprit.file` is the file to open. |
| `issues.occurrences` | `id`, `limit` (default 50, max 500) | `{items: Occurrence[], total, truncated, privacy}`, newest first |
| `issues.histogram` | `ids[]` (empty means every issue with activity), `since` (a duration, default `24h`, max 30 days), `buckets` (default 24, max 240) | `{start, bucket_seconds, buckets, series: {id: number[]}, note}`. Every series has exactly `buckets` entries, oldest first. Counts cover retained occurrences only. |
| `projects.list` | none | `{projects: [{project, services[], open, total, last_seen, root?}]}`. `root` is the checkout where the project's latest issue was recorded. |
| `issues.set_status` *(mutating)* | `ids[]`, `status` (`open`, `resolved`, or `ignored`) | `{updated: Issue[], failed: [{id, error}]}`. One bad id never aborts the rest. |
| `host.snapshot` | `process_limit`, `process_filter` | A host tick: `{snapshot: CompactSnapshot, per_core_usage[], load_avg[3], alerts?}` |
| `processes.list` | `sort` (`cpu`, `memory`, `pid`, or `name`), `limit`, `filter`, `include_system` | The `monitor processes --json` envelope |
| `process.kill` *(destructive)* | `pid`, `force`, `confirm` | `{killed, outcome, signal, waited_ms, pid, force, next_action?}` |
| `profile.capture` *(destructive)* | `pid`, `type` (`cpu`, `heap`, `heap-alloc`, or `goroutine`), `duration_seconds` (default 5, max 120), `pprof_addr`, `func`, `confirm` | `{heatmap: monitor.line_heatmap.v1, target: {pid, name, runtime, codebase_root, main_script}, method}` |
| `heatmap.file` | `path` (absolute), `type`, `func`, `top` | `monitor.line_heatmap.v1` |
| `launches.list` | none | `{launches: [{launch_id, name, project, pid, started_at, scan, alive, inspector_ports[]}]}` |
| `logs.search` | `query`, `levels[]`, `process`, `pid`, `since_seconds`, `limit` (default 200, max 1000) | `{entries: [{timestamp, pid, process, level, message, raw}], privacy}` |
| `incidents.list` | none | `{stashes[], pending[], stash_error?, pending_error?}` |
| `doctor` | `dir` (absolute, optional) | The `monitor doctor --json` report (see [Doctor v1](./doctor-v1)). If `dir` is set, `code_intel` is probed against that checkout instead of the server's working directory. |
| `subscribe` | `topics[]`, `host: {interval_ms, process_limit, process_filter}` | `{subscribed: [...]}` |
| `unsubscribe` | `topics[]` | `{subscribed: [...]}` |

`profile.capture` profiles the real runtime process under `pid`. For
example, a `yarn dev` pid resolves to its `node` child. Capture uses the
same mechanisms as `monitor hot <pid>`: the inspector for Node/Deno started
with `--inspect`, pprof for Go, and macOS `sample` at function level as a
fallback.

The server never attaches to a running process that was not started for
profiling. When there is no way to capture, it answers `CodeUnavailable`
with a `recovery`, for example "relaunch it under `monitor run --inspect`".

## Topics

Subscribing to a topic it already streams restarts that topic with the new
parameters.

### `host`: `host.tick`

The server sends one notification per interval. The default interval is
1 s, and it is clamped to between 500 ms and 60 s. Each notification has
the same shape as `host.snapshot`.

`alerts` carries rule alerts from the engine `monitor watch` runs. These
alerts pass through the same one-minute cooldown, so a sustained condition
is reported once a minute.

### `issues`: `issue.event`

The server watches the issue store. The first read is a silent baseline.
After that, it sends one event per changed issue, oldest first:

```json
{"jsonrpc":"2.0","method":"issue.event","params":{
  "type":"regressed","delta":1,
  "issue":{"id":"ISS-9FBE04FC5C81F656","short_id":"9FBE","title":"Error: ...","project":"shop",
           "service":"api","kind":"exception","status":"open","exception_type":"Error",
           "culprit":{"function":"detonate","file":"src/app.js","line":49,"source":"stack"},
           "occurrence_count":4,"reopened_count":1,"last_seen":"2026-10-01T18:04:05Z"},
  "privacy":{"scrubbed":0,"text_is_untrusted":true}}}
```

`type` is one of:

- `new`: an issue that did not exist before.
- `regressed`: a resolved issue that a new occurrence reopened.
- `occurrence`: more occurrences of an existing issue.
- `status`: only the status changed, for example a resolve from the CLI.

If a topic fails, the server sends one `topic.error` notification of the
form `{topic, error}` instead of stopping silently.

## Errors

| Code | Meaning |
|---|---|
| -32700 | Parse error: the line was not JSON |
| -32600 | Invalid request: not `{"jsonrpc":"2.0","method":...}` |
| -32601 | Method not found, or not wired in this build |
| -32602 | Invalid params |
| -32603 | Internal error |
| -32001 | Confirm required: retry with `"confirm": true` after the user confirms |
| -32002 | Read-only: this server was started with `--read-only` |
| -32003 | Not found: the issue, file, or other target does not exist |
| -32004 | Unavailable: this target cannot be captured. `data` carries `{status, limitation, recovery}`. |
| -32005 | Ambiguous: a short id or prefix matched several issues, or a pid has several runtime children. `data.candidates` lists them. |
| -32006 | Refused: a protected or system process. `confirm` never overrides this. |
