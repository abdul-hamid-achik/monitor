# Monitor Desktop

An Electron app over `monitor serve --stdio`
([monitor.app.v1](../docs/contracts/app-protocol-v1.md)). Everything it shows
comes from monitor itself; the app owns windows, connections, launches and
notifications, nothing else.

- **This Mac:** the app spawns its bundled `monitor serve --stdio`.
- **Remote hosts:** it spawns `ssh -T <host> <monitor> serve --stdio` with
  your own ssh config, agent and keys (`BatchMode=yes`, so it never prompts).
  Issue data streams straight from the host and is never uploaded anywhere.
  A read-only connection starts the server with `--read-only`.

## Views

| View | What it does | Protocol |
|---|---|---|
| Issues | Inbox: filters, search, 24 h sparklines, live updates, bulk resolve/ignore/reopen | `issues.list`, `issues.histogram`, `issue.event` |
| Issue | Culprit snippet, cause chain, stack, codemap impact, last commit, degradations, occurrences, copy for agent, open in editor (VS Code/Cursor Remote-SSH for remote issues) | `issues.get`, `issues.occurrences` |
| Hot lines | Live capture (confirmed), saved `.cpuprofile`/pprof files, errors × heat per line | `profile.capture`, `heatmap.file`, `launches.list` |
| Host | Live CPU/memory/network/disk, cores, filesystems, alerts, processes with confirmed kill | `host.tick`, `processes.list`, `process.kill` |
| Launch | Runs a command under `monitor run --name` (no shell), streams its output | local only |
| Logs / Incidents / Doctor | The log store, fcheap incident bundles, ecosystem health for a project's checkout | `logs.search`, `incidents.list`, `doctor` |

## Develop

```sh
bun install
bun run dev          # builds ../bin/monitor-equivalent, the renderer, then starts Electron
bun test             # main-process + formatting units
bun run typecheck    # renderer (TS) and main/preload (checkJs)
bun run smoke        # boots hidden, calls the local monitor, prints one JSON line
bun run capture      # dev only: one PNG per view into captures/
```

In development the app uses the repo's `bin/monitor` (`go build -o
bin/monitor ./cmd/monitor`); a packaged app uses its bundled
`Contents/Resources/bin/monitor`; Settings can point at any other binary.

## Package and release

```sh
CSC_IDENTITY_AUTO_DISCOVERY=false bun run dist   # unsigned .app in release/
bun run dist                                     # signed with the Developer ID in your keychain
bun run setup:signing                            # once: notarization credentials -> keychain profile
bun run dist:notarized                           # universal .dmg/.zip, signed + notarized
```

`setup:signing` reads your Apple ID and an app-specific password (create one at
https://account.apple.com → Sign-In and Security) and stores them with
`xcrun notarytool store-credentials monitor-desktop`, so the password lives in
the keychain, not in env vars or files.

For CI, export the "Developer ID Application" certificate from Keychain Access
as a .p12 and run `bun run setup:signing --github path/to/cert.p12`: it sets
`MACOS_CERTIFICATE_P12_BASE64`, `MACOS_CERTIFICATE_PASSWORD`, `APPLE_ID`,
`APPLE_APP_SPECIFIC_PASSWORD` and `APPLE_TEAM_ID` with `gh secret set` (values
on stdin, never in argv). A `desktop-v*` tag then runs
`.github/workflows/desktop.yml`, which builds, signs, notarizes and publishes a
draft GitHub release.

## Security

- The renderer is sandboxed with context isolation and a strict CSP; preload
  exposes named functions only, and main allowlists the protocol methods.
- Error and log text is process output: it is scrubbed by the server and
  rendered as text, never as markup.
- Destructive actions (kill, live capture) go through a confirmation dialog
  and `confirm: true`; protected processes are refused by the server.
- SSH hosts and remote binary paths are validated so they can never be read
  as ssh options or shell syntax; launches split the command into argv
  without a shell.
