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
| **TUI** | A confirmation dialog appears (`k` = SIGTERM, `x` = SIGKILL); protected/system PIDs stay refused even at the dialog. |
| **CLI** | `monitor kill <pid>` refuses protected/system PIDs; `--yes` skips the protection checks for non-protected targets. |
| **MCP** | `monitor_kill` requires `confirm: true` in its typed input, *and* still refuses protected/system PIDs with a structured `{ "refused": true }` payload. |

## SIGTERM vs SIGKILL

By default Monitor sends `SIGTERM`, giving the process a chance to shut down
cleanly. Force-kill (`SIGKILL`) is always an explicit, separate action:

- TUI: `x` instead of `k`
- CLI: `monitor kill <pid> --force`
- MCP: `force: true` in the `monitor_kill` input

## Example: a refused kill

```bash
$ monitor kill 1 --json
{
  "killed": false,
  "refused": true,
  "reason": "launchd (pid 1) is a protected system process",
  "pid": 1
}
```

The same target is refused identically through the TUI and the MCP tool — the
classification lives in one place, so the three surfaces can't drift apart.
