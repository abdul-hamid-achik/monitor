---
layout: home

hero:
  name: Monitor
  text: Agent-harnessable system monitor
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
  - title: Three surfaces, one model
    details: Interactive Bubble Tea TUI for humans, JSON CLI commands for agents, and an MCP server with seven tools.
    link: /guide/getting-started
  - title: Anomaly detection
    details: CPU-spike detection (3× a fixed baseline), RSS-growth regression with slope and R² confidence, disk-fill and swap-pressure rules, plus configurable CPU/memory thresholds.
    link: /guide/anomaly-detection
  - title: Ecosystem-integrated
    details: Wraps codemap, fcheap, vecgrep, vidtrace, glyphrun, cairntrace, tinyvault, veclite, and tmux as one tool.
    link: /guide/ecosystem
  - title: Profiling built-in
    details: pprof + macOS sample. One capture tells you where the CPU and the memory actually went.
    link: /guide/cli#profile
  - title: veclite-backed log store
    details: Capture and search log streams from any process. Shared-read with the CLI for compound queries.
    link: /guide/cli#logs
  - title: Local-first, agent-friendly
    details: No cloud. No telemetry. Everything works on a laptop or in CI. Every CLI command accepts --json for agents.
    link: /guide/mcp
---
