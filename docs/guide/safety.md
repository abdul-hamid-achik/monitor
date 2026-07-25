# Process Safety

Terminating the wrong process can take down your session or destabilize the
system. Monitor gates every kill on a single safety check (`internal/kill`)
that is shared by the TUI, the `monitor kill` CLI command, and the
`monitor_kill` MCP tool — so the rules are identical no matter how you act.

## What is refused

The safety check classifies each target PID and refuses two categories:

- **Protected processes** — critical system processes that are never
  killable. The protected set includes `launchd` (PID 1), `kernel_task`,
  `WindowServer`, `Finder`, `Dock`, `loginwindow`, plus any PID `< 100`.
- **System-owned processes** — anything owned by `root` or `_mbsetupuser`.

Both categories surface a human-readable warning explaining *why* the target
was flagged.

## How confirmation differs per surface

| Surface | How you confirm |
|---------|-----------------|
| **TUI** | A confirmation dialog appears (`k` = SIGTERM, `x` = SIGKILL); confirm with `y`, cancel with `n`/`esc`. Protected **and** system PIDs stay refused even at the dialog, matching the CLI and MCP. |
| **CLI** | `monitor kill <pid>` refuses protected/system PIDs. `--yes` is accepted for compatibility but never overrides the safety classification. |
| **MCP** | `monitor_kill` requires `confirm: true` in its typed input, *and* still refuses protected/system PIDs with a structured `{ "refused": true }` payload. |

## SIGTERM vs SIGKILL

By default Monitor sends `SIGTERM`, giving the process a chance to shut down
cleanly. Force-kill (`SIGKILL`) is always an explicit, separate action:

- TUI: `x` instead of `k`
- CLI: `monitor kill <pid> --force`
- MCP: `force: true` in the `monitor_kill` input

### Verified outcomes

Sending a signal is not the same as the process dying. After `SIGTERM`,
`kill` polls the PID briefly (up to ~2s) and reports what actually
happened as an `outcome` on every surface (`monitor kill --json` per-PID
results and the `monitor_kill` MCP payload):

- `terminated` — the process is confirmed gone.
- `still_running` — it survived the polling window; a `next_action` field
  suggests the force-kill (`--force` on the CLI, `force: true` on MCP).
- `unknown` — the liveness check itself failed (state can't be verified).

`killed` is `true` only once `outcome` is `terminated` — never merely
because a signal was sent. Escalation to `SIGKILL` is never automatic: a
survivor produces a suggestion, and the follow-up force-kill is a separate,
explicit action. See [`kill`](/guide/cli#kill) for the full JSON shape.

## Example: a refused kill

```bash
$ monitor kill 1 --json
{
  "killed": false,
  "protected": true,
  "safety_warnings": [
    "launchd (pid 1) is a protected system process"
  ],
  "refused": true,
  "reason": "protected or system processes cannot be terminated by monitor",
  "safety": { "has_protected": true, "has_system": true }
}
```

(The MCP `monitor_kill` tool returns a different shape — `{ "killed": false,
"refused": true, "reason": ..., "pid": ... }` — for the same refusal.)

A protected or system target like `launchd` is refused identically across the
CLI, TUI, and MCP — the `kill.CheckSafety` classification lives in one place, so
the three surfaces can't drift apart.

## Telemetry export safety

`monitor telemetry` is the only Monitor stream designed to cross a host
boundary by default. Its V1 schema uses a fixed metric allowlist and excludes
hostname, IP addresses, PID/process/user identity, command arguments,
environment variables, devices, mounts, paths, raw availability reasons,
profile data, alert details, and diagnoses.

Do not forward `monitor watch --json`, `monitor snapshot --json`, or
`monitor snapshot --compact` as a substitute. Those local inspection surfaces
intentionally contain identity and diagnostic detail that the telemetry
contract removes.

Monitor writes telemetry only to stdout. A consuming adapter should:

- add deployment identity only after parsing and validating the V1 event;
- keep authentication credentials out of Monitor's environment;
- apply bounded retry storage in an ephemeral runtime directory;
- reject unknown major schema versions, object fields, metric IDs, units,
  availability states, alert rules, and severities;
- retain raw profiles only through an explicit, separate artifact workflow.
