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
- **Chalupa droplets:** the app spawns `chalupa monitor serve (--name ENV |
  --config chalupa.yml)`, Chalupa's own stdio door. Chalupa resolves the
  address, identity and pinned host key and runs the droplet's
  `monitor serve --stdio --read-only`. The environment list comes from
  `chalupa ls --json`. Nothing is stored in Chalupa's cloud: the bytes go
  droplet → ssh → this app.

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
bun run dist:notarized:tvault                    # universal .dmg/.zip, signed + notarized (credentials from tvault)
```

### Before the first release (one-time)

1. **Developer ID certificate.** "Developer ID Application: abdul hamid achik
   (XWEDJB8MA4)" must be in the login keychain. Check it with
   `security find-identity -v -p codesigning`. electron-builder finds it there
   and signs the app and the bundled `monitor`, with hardened runtime and a
   timestamp.
2. **Apple Developer Program agreement.** Notarization answers **HTTP 403 "A
   required agreement is missing or has expired"** until the account holder
   accepts the current Program License Agreement. Apple updates it from time to
   time. Accept it at https://developer.apple.com/account (the banner at the
   top, or Account → Agreements). Also check App Store Connect → Business. This
   check is read-only and stops answering 403 once the agreement is in effect:

   ```sh
   tvault run -p monitor-desktop -- sh -c 'xcrun notarytool history --apple-id "$APPLE_ID" --password "$APPLE_APP_SPECIFIC_PASSWORD" --team-id "$APPLE_TEAM_ID"'
   ```
3. **Notarization credentials** live in the tinyvault project `monitor-desktop`.
   They are never committed or echoed, and only the build process receives
   them.
   - Preferred: an **App Store Connect API key**, scoped to App Store Connect.
     The keys are `APPLE_API_KEY_P8` (the .p8 contents, set with
     `tvault set APPLE_API_KEY_P8 --from-file AuthKey_XXXX.p8 -p monitor-desktop`),
     `APPLE_API_KEY_ID` and `APPLE_API_ISSUER`.
   - Fallback: an Apple ID **app-specific password**. The keys are `APPLE_ID`,
     `APPLE_TEAM_ID` and `APPLE_APP_SPECIFIC_PASSWORD`. Set the password with
     `read -rs P && printf %s "$P" | tvault set APPLE_APP_SPECIFIC_PASSWORD --stdin -p monitor-desktop; unset P`.

   `scripts/notarized-release.sh`, which `dist:notarized:tvault` runs, uses
   the API key when all three of its values are present, otherwise the
   password. It writes the .p8 to a private temp directory for the build only.
   `bun run setup:signing` is the keychain-profile alternative
   (`dist:notarized`).

### Cutting a release

```sh
git switch main && git pull                      # release from main
cd desktop && rm -rf release
bun run dist:notarized:tvault                    # builds the universal monitor, the renderer, then signs + notarizes
spctl --assess --type execute -vv "release/mac-universal/Monitor Desktop.app"   # must say: accepted, source=Notarized Developer ID
"release/mac-universal/Monitor Desktop.app/Contents/MacOS/Monitor Desktop" --smoke   # {"ok":true,...}
git tag -a desktop-v0.1.0 -m "Monitor Desktop 0.1.0 (preview)" && git push origin desktop-v0.1.0
gh release create desktop-v0.1.0 --prerelease --title "Monitor Desktop 0.1.0 (preview)" --notes-file <release-notes.md> \
  "release/Monitor Desktop-0.1.0-universal.dmg" "release/Monitor Desktop-0.1.0-universal-mac.zip"
```

- **Publish it as a prerelease.** A prerelease never becomes the repository's
  "latest" release, which the CLI install docs (`releases/latest`) link to.
- **Bump the version before the next release.** Change `version` in
  `package.json` first; the artifact names come from it.
- **CI can publish too.** Add the five GitHub secrets: export the "Developer
  ID Application" certificate from Keychain Access as a .p12, then run
  `bun run setup:signing --github path/to/cert.p12`. After that, a
  `desktop-v*` tag runs `.github/workflows/desktop.yml`, which builds, signs,
  notarizes and publishes a draft. Without those secrets the release job skips
  on purpose, so a tag released by hand is never overwritten by an unsigned
  build.

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
