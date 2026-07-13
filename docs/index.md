---
layout: home

hero:
  name: Monitor
  text: See everything. In your terminal.
  tagline: The agent-harnessable system monitor for macOS and Linux. One model, three surfaces — an interactive TUI, JSON CLI commands, and an MCP server. For humans, scripts, and AI agents alike.
  image:
    src: /logo.svg
    alt: Monitor
  actions:
    - theme: brand
      text: Get Started
      link: /guide/getting-started
    - theme: alt
      text: CLI Reference
      link: /guide/cli
    - theme: alt
      text: MCP Server
      link: /guide/mcp
    - theme: alt
      text: View on GitHub
      link: https://github.com/abdul-hamid-achik/monitor

features:
  - icon: 🖥️
    title: Three surfaces, one model
    details: Interactive Bubble Tea TUI for humans, JSON CLI commands for scripts, and an MCP server with eight tools — all backed by the same pub/sub collector.
    link: /guide/getting-started
  - icon: ⚡
    title: Anomaly detection
    details: CPU-spike detection against a rolling per-process baseline, wall-clock RSS-growth regression with R², zombie-process, disk-fill, and swap-pressure rules.
    link: /guide/anomaly-detection
  - icon: 🌡️
    title: Real temperature
    details: SMC sensor readings via powermetrics on macOS with a transparent CPU-load estimate fallback. Every reading is badged real or est — you always know what you're looking at.
    link: /guide/cli
  - icon: 🔬
    title: Profiling built-in
    details: pprof HTTP scraping for Go processes and macOS sample for any process. One capture tells you where the CPU and the memory actually went.
    link: /guide/cli#profile
  - icon: 📝
    title: veclite-backed log store
    details: Capture and search log streams from any process. Shared-read with the CLI for compound queries — the writer never blocks a search.
    link: /guide/cli#logs
  - icon: 🧩
    title: Ecosystem-integrated
    details: Wraps codemap, fcheap, vecgrep, vidtrace, glyphrun, cairntrace, tinyvault, veclite, and tmux as one cohesive tool.
    link: /guide/ecosystem
  - icon: 🛡️
    title: Safe process killing
    details: Protected and system processes refuse termination — consistently across the TUI, CLI, and MCP server. --yes never bypasses protection.
    link: /guide/safety
  - icon: 🔒
    title: Local-first, no telemetry
    details: No cloud. No analytics. No phone-home. Everything runs on your laptop or in CI. Every CLI command accepts --json for agents.
    link: /guide/mcp
  - icon: 🤖
    title: Built for AI agents
    details: Eight MCP tools — four read-only, four mutating with a confirm gate. Compact payloads for small model contexts. Structured refusals for hand-built requests.
    link: /guide/mcp
---

<!-- Stats strip -->
<div class="stats-strip">
  <div class="stat-item">
    <div class="stat-value">8</div>
    <div class="stat-label">MCP Tools</div>
  </div>
  <div class="stat-item">
    <div class="stat-value">9</div>
    <div class="stat-label">TUI Tabs</div>
  </div>
  <div class="stat-item">
    <div class="stat-value">0</div>
    <div class="stat-label">Telemetry</div>
  </div>
  <div class="stat-item">
    <div class="stat-value">100%</div>
    <div class="stat-label">Local-first</div>
  </div>
</div>

<!-- Terminal preview -->
<div class="terminal-section">
  <div class="terminal-wrap">
    <div class="landing-section" style="padding: 0;">
      <div class="section-eyebrow">Live Preview</div>
      <h2>What it looks like in your terminal</h2>
      <p class="section-sub">
        A real-time Nord-themed TUI with CPU gauges, memory stats, temperature
        readings, a process table, and anomaly alerts — all keyboard-driven,
        all in your terminal.
      </p>
    </div>
    <TerminalMockup />
  </div>
</div>

<!-- Three surfaces comparison -->
<div class="landing-section">
  <div class="section-eyebrow">Architecture</div>
  <h2>One model, three surfaces</h2>
  <p class="section-sub">
    The same pub/sub collector powers every surface. Whether you're a human
    watching the TUI, a script piping JSON, or an AI agent calling MCP tools —
    you get the same data, the same safety guarantees.
  </p>
  <div class="surfaces-grid">
    <div class="surface-card">
      <div class="surface-icon">🖥️</div>
      <h3>Interactive TUI</h3>
      <p>
        Nine-tab Bubble Tea v2 interface with CPU gauges, sparklines, a sortable
        process table, PID-pinned diagnostics, and keyboard + mouse navigation.
        Nord-themed, fully responsive to terminal size.
      </p>
      <code class="surface-code">monitor studio</code>
    </div>
    <div class="surface-card">
      <div class="surface-icon">⚙️</div>
      <h3>JSON CLI</h3>
      <p>
        Every command accepts <code>--json</code> for machine-readable output.
        Snapshot, watch (NDJSON stream), analyze, process, kill, profile, logs,
        history, baseline, diff — all scriptable, all pipeable.
      </p>
      <code class="surface-code">monitor snapshot --json | jq '.cpu'</code>
    </div>
    <div class="surface-card">
      <div class="surface-icon">🤖</div>
      <h3>MCP Server</h3>
      <p>
        Eight tools over stdio for AI agents — four read-only (snapshot,
        processes, doctor, analyze) and four mutating with a confirm gate
        (kill, profile, investigate, record).
      </p>
      <code class="surface-code">monitor mcp serve</code>
    </div>
  </div>
</div>

<!-- Agent / MCP showcase -->
<div class="agent-section">
  <div class="agent-wrap">
    <div class="landing-section" style="padding: 0;">
      <div class="section-eyebrow">For AI Agents</div>
      <h2>Eight MCP tools, two-layer safety</h2>
      <p class="section-sub">
        The MCP SDK validates the typed input schema (rejecting calls that omit
        <code>confirm</code>), and the handlers re-check it so hand-built
        requests still get a structured refusal with <code>refused: true</code>
        and a <code>reason</code>.
      </p>
    </div>
    <div class="agent-grid">
      <div>
        <div class="agent-tool" style="margin-bottom: 12px;">
          <div style="display: flex; align-items: center; gap: 8px;">
            <span class="tool-name">monitor_snapshot</span>
            <span class="tool-badge read">Read-only</span>
          </div>
          <span class="tool-desc">Full SystemInfo — CPU, memory, disk, network, temperature, top processes. Pass <code>{"compact":true}</code> for bounded payloads.</span>
        </div>
        <div class="agent-tool" style="margin-bottom: 12px;">
          <div style="display: flex; align-items: center; gap: 8px;">
            <span class="tool-name">monitor_processes</span>
            <span class="tool-badge read">Read-only</span>
          </div>
          <span class="tool-desc">Top processes with CPU, RSS, threads, and protection status. Sortable and filterable.</span>
        </div>
        <div class="agent-tool" style="margin-bottom: 12px;">
          <div style="display: flex; align-items: center; gap: 8px;">
            <span class="tool-name">monitor_doctor</span>
            <span class="tool-badge read">Read-only</span>
          </div>
          <span class="tool-desc">Ecosystem tool availability — codemap, fcheap, vecgrep, glyphrun, tinyvault, and more.</span>
        </div>
        <div class="agent-tool">
          <div style="display: flex; align-items: center; gap: 8px;">
            <span class="tool-name">monitor_analyze</span>
            <span class="tool-badge read">Read-only</span>
          </div>
          <span class="tool-desc">Bounded cross-signal diagnosis — snapshot + anomaly rules + top processes in one call.</span>
        </div>
      </div>
      <div>
        <div class="agent-tool" style="margin-bottom: 12px;">
          <div style="display: flex; align-items: center; gap: 8px;">
            <span class="tool-name">monitor_kill</span>
            <span class="tool-badge mutate">Mutating · confirm</span>
          </div>
          <span class="tool-desc">Terminate a process. Safety-checked — refuses protected and system PIDs. Requires <code>confirm: true</code>.</span>
        </div>
        <div class="agent-tool" style="margin-bottom: 12px;">
          <div style="display: flex; align-items: center; gap: 8px;">
            <span class="tool-name">monitor_profile_capture</span>
            <span class="tool-badge mutate">Mutating · confirm</span>
          </div>
          <span class="tool-desc">Heap, CPU, goroutine, or macOS sample profile for a PID. Requires <code>confirm: true</code>.</span>
        </div>
        <div class="agent-tool" style="margin-bottom: 12px;">
          <div style="display: flex; align-items: center; gap: 8px;">
            <span class="tool-name">monitor_investigate</span>
            <span class="tool-badge mutate">Mutating · confirm</span>
          </div>
          <span class="tool-desc">Diagnostic pipeline — snapshot + profile + codemap-ranked stash. Requires <code>confirm: true</code>.</span>
        </div>
        <div class="agent-tool">
          <div style="display: flex; align-items: center; gap: 8px;">
            <span class="tool-name">monitor_record</span>
            <span class="tool-badge mutate">Mutating · confirm</span>
          </div>
          <span class="tool-desc">Platform screen recording with artifact verification. Requires <code>confirm: true</code>.</span>
        </div>
      </div>
    </div>
  </div>
</div>

<!-- Quick start -->
<div class="quick-start landing-section">
  <div class="section-eyebrow">Quick Start</div>
  <h2>Get running in 30 seconds</h2>
  <p class="section-sub">
    Build from source — no binary download needed. Go 1.25+ on macOS (Apple
    Silicon) or Linux.
  </p>

```bash
# Clone and build
git clone https://github.com/abdul-hamid-achik/monitor.git
cd monitor
go build -o bin/monitor ./cmd/monitor

# Launch the TUI
./bin/monitor studio

# Or get JSON for a script
./bin/monitor snapshot --json | jq '.cpu'
```

</div>

<!-- Final CTA -->
<div class="cta-section">
  <div class="cta-wrap">
    <h2>Start monitoring in your terminal</h2>
    <p>
      Free, open-source, MIT-licensed. No cloud, no telemetry, no lock-in.
      Built for developers who want real system visibility — for themselves,
      their scripts, and their AI agents.
    </p>
    <div class="cta-actions">
      <a class="cta-btn primary" href="/guide/getting-started">Get Started →</a>
      <a class="cta-btn secondary" href="https://github.com/abdul-hamid-achik/monitor">View on GitHub</a>
    </div>
  </div>
</div>