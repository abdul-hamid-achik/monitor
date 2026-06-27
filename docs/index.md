---
# https://vitepress.dev/reference/default-theme-home-page
layout: home

hero:
  name: 'Monitor'
  text: 'Agent-harnessable system monitor'
  tagline: A terminal-based system monitor for macOS and Linux — the same metrics in an interactive TUI, JSON CLI commands, and an MCP server.
  actions:
    - theme: brand
      text: Get Started
      link: /guide/getting-started
    - theme: alt
      text: CLI Reference
      link: /guide/cli
    - theme: alt
      text: View on GitHub
      link: https://github.com/abdul-hamid-achik/monitor

features:
  - icon: 📊
    title: Real-time TUI
    details: CPU, Memory, Temperature, Disk, Network, and Processes across 9 tabs (plus Overview, Settings, and Trends), built on Bubble Tea v2 with a Nord theme.
  - icon: 🤖
    title: Agent-harnessable
    details: Every view is also a --json CLI command and an MCP stdio tool, so scripts and AI agents see exactly what you see.
  - icon: 🌡️
    title: Real temperature
    details: SMC sensors via powermetrics on macOS, with a transparent CPU-load estimate fallback when sudo isn't available.
  - icon: ⚠️
    title: Safe by default
    details: Protected and system-owned processes refuse termination — consistently across the TUI, CLI, and MCP surfaces.
  - icon: 🧩
    title: Ecosystem integration
    details: fcheap incident stashes, tinyvault secret injection, glyphrun specs, and codemap correlation.
  - icon: 🔍
    title: Diagnose deeply
    details: Capture heap/CPU/goroutine profiles, tail logs into a searchable store, and bundle incidents for later investigation.
---
