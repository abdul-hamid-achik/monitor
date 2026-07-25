---
aside: false
---

# Installation

Homebrew is the recommended way to install Monitor on macOS or Linux when
Homebrew is available. It selects the correct release for your platform, puts
`monitor` on your `PATH`, and gives you one-command upgrades.

<InstallPanel heading-level="h2" />

## Install with Homebrew

Install Monitor from the project tap:

```bash
brew install --cask abdul-hamid-achik/tap/monitor
```

The tap is added automatically. You do not need to run `brew tap` first.

### Verify and launch

Confirm that the binary is available, then open the interactive Studio:

```bash
monitor --version
monitor studio
```

Running `monitor` without a subcommand prints command help. To check optional
ecosystem integrations such as `codemap`, `fcheap`, and `tmux`, run:

```bash
monitor doctor
```

### Upgrade

Refresh Homebrew metadata and install the latest Monitor release:

```bash
brew update
brew upgrade --cask monitor
```

### Uninstall

```bash
brew uninstall --cask monitor
```

This removes the binary but keeps your configuration and local Monitor data.
If you no longer use anything else from the project tap, remove it with
`brew untap abdul-hamid-achik/tap`.

## Install a release archive

Prebuilt archives are available for macOS and Linux on both Apple/ARM64 and
x86-64 systems. Open the [latest release][releases], then download the archive
that matches your machine:

| System | Release archive suffix |
|---|---|
| Apple Silicon Mac | `Darwin_arm64.tar.gz` |
| Intel Mac | `Darwin_x86_64.tar.gz` |
| ARM64 Linux | `Linux_arm64.tar.gz` |
| x86-64 Linux | `Linux_x86_64.tar.gz` |

Archive names follow
`monitor_VERSION_SYSTEM_ARCH.tar.gz`. For example, a `v1.2.3` Apple Silicon
release is named `monitor_1.2.3_Darwin_arm64.tar.gz`.

Extract the archive and install the binary somewhere on your `PATH`:

```bash
archive="monitor_VERSION_SYSTEM_ARCH.tar.gz"
grep "  $archive$" checksums.txt | sha256sum --check --strict -
tar -xzf "$archive"
sudo install -m 0755 monitor /usr/local/bin/monitor
monitor --version
```

Replace `VERSION`, `SYSTEM`, and `ARCH` with the values from the downloaded
file, and download the matching `checksums.txt` beside the archive. The
checksum command must succeed before extraction; it verifies the exact archive
name rather than trusting a mutable download URL. On macOS, replace
`sha256sum` with `shasum -a 256 -c`.

## Build from source

Building requires [Go 1.25 or newer](https://go.dev/doc/install) and Git.

```bash
git clone https://github.com/abdul-hamid-achik/monitor.git
cd monitor
mkdir -p bin
go build -trimpath -ldflags="-s -w" -o bin/monitor ./cmd/monitor
sudo install -m 0755 bin/monitor /usr/local/bin/monitor
monitor --version
```

If you use [Task](https://taskfile.dev/), create `bin/` first, then
`task release` replaces the `go build` command and produces the same
`bin/monitor` path. You can also run a source build without installing it by
using `./bin/monitor`.

## Platform capabilities

Monitor's core CPU, memory, disk, network, and process metrics work on macOS
and Linux. A few host-specific capabilities differ:

- **macOS:** supports `sample` process profiles. On Apple Silicon, real SMC
  temperature readings use `powermetrics` with cached `sudo` credentials; if
  they are unavailable, Monitor transparently reports a CPU-load estimate.
  Load averages are not exposed by the macOS collector.
- **Linux:** supports load averages and cgroup v2 detection. Temperature uses
  the CPU-load estimate, and macOS `sample` profiles are unavailable. Go
  processes that expose `pprof` remain profileable.

Every temperature payload includes its source, and Studio badges readings as
`real` or `est` so the fallback is visible.

## Next steps

- Follow the [two-minute tour](/guide/getting-started).
- Learn the [Studio keyboard controls](/guide/tui).
- Explore the [JSON CLI](/guide/cli) or [MCP server](/guide/mcp).

[releases]: https://github.com/abdul-hamid-achik/monitor/releases/latest
