<script setup lang="ts">
import { withBase } from 'vitepress'
import CommandCopy from './CommandCopy.vue'
import InstallPanel from './InstallPanel.vue'
import TerminalMockup from './TerminalMockup.vue'

const installCommand = 'brew install --cask abdul-hamid-achik/tap/monitor'
</script>

<template>
  <div class="monitor-home">
    <section class="home-hero">
      <div class="hero-grid home-shell">
        <div class="hero-copy">
          <div class="hero-kicker">
            <span aria-hidden="true" />
            Local observability for macOS and Linux
          </div>
          <h1>See what your machine is doing, without leaving the terminal.</h1>
          <p class="hero-lede">
            Monitor turns live system signals into a focused Studio, scriptable
            JSON, and a safe MCP surface. One local binary for developers,
            automation, and agents.
          </p>

          <CommandCopy class="hero-command" :command="installCommand" />

          <div class="hero-actions">
            <a class="home-button primary" :href="withBase('/guide/getting-started')">
              Start the two-minute tour
            </a>
            <a
              class="home-button secondary"
              href="https://github.com/abdul-hamid-achik/monitor"
            >
              View source <span aria-hidden="true">→</span>
            </a>
          </div>

          <div class="hero-support">
            <span>Apple Silicon</span>
            <span>Intel macOS</span>
            <span>Linux arm64 + x86_64</span>
          </div>
        </div>

        <div class="hero-visual">
          <div class="preview-meta">
            <span>Studio preview</span>
            <span>local metrics · 1s refresh</span>
          </div>
          <TerminalMockup />
          <div class="preview-note">
            <span>CPU spike detected</span>
            <strong>node · PID 8421</strong>
          </div>
        </div>
      </div>
    </section>

    <dl class="signal-rail home-shell">
      <div>
        <dt>Local-first</dt>
        <dd>No account or telemetry</dd>
      </div>
      <div>
        <dt>9 views</dt>
        <dd>One keyboard-driven Studio</dd>
      </div>
      <div>
        <dt>JSON + NDJSON</dt>
        <dd>Stable output for automation</dd>
      </div>
      <div>
        <dt>8 MCP tools</dt>
        <dd>Typed inputs and safety gates</dd>
      </div>
    </dl>

    <section class="home-section surfaces-section">
      <div class="home-shell">
        <header class="section-heading">
          <div>
            <span class="section-kicker">One collector</span>
            <h2>Choose the surface that fits the moment.</h2>
          </div>
          <p>
            Every interface reads the same process and system model, so a human
            in Studio and an agent over MCP see the same facts and the same
            safeguards.
          </p>
        </header>

        <div class="surface-layout">
          <article class="surface-main">
            <div class="surface-index">01 / Studio</div>
            <h3>Watch the whole machine at a glance.</h3>
            <p>
              Navigate CPU, memory, temperature, disks, network, processes,
              settings, and trends without giving up your terminal workflow.
            </p>
            <div class="surface-command">
              <span aria-hidden="true">$</span>
              <code>monitor studio</code>
            </div>
            <ul>
              <li>Live gauges and sparklines</li>
              <li>Sortable process diagnostics</li>
              <li>Protected termination workflow</li>
            </ul>
            <a :href="withBase('/guide/tui')">
              Explore Studio <span aria-hidden="true">→</span>
            </a>
          </article>

          <div class="surface-side">
            <article>
              <div class="surface-index">02 / CLI</div>
              <h3>Pipe the signal anywhere.</h3>
              <p>
                One-shot snapshots, bounded agent payloads, and streaming NDJSON
                make Monitor comfortable in shell scripts and CI.
              </p>
              <div class="code-sample">
                <code>monitor snapshot --compact</code>
                <span>{ "cpu": { "usage_percent": 18.4 } }</span>
              </div>
              <a :href="withBase('/guide/cli')">
                Read the CLI reference <span aria-hidden="true">→</span>
              </a>
            </article>

            <article>
              <div class="surface-index">03 / MCP</div>
              <h3>Give agents a bounded view.</h3>
              <p>
                Four read-only tools diagnose system state. Four mutating tools
                require explicit confirmation and still honor protected-process
                rules.
              </p>
              <div class="tool-line">
                <span>monitor_analyze</span>
                <span>read-only</span>
              </div>
              <div class="tool-line">
                <span>monitor_kill</span>
                <span>confirm required</span>
              </div>
              <a :href="withBase('/guide/mcp')">
                Configure MCP <span aria-hidden="true">→</span>
              </a>
            </article>
          </div>
        </div>
      </div>
    </section>

    <section class="home-section diagnosis-section">
      <div class="home-shell diagnosis-grid">
        <div class="diagnosis-intro">
          <span class="section-kicker">From symptom to evidence</span>
          <h2>More useful than a wall of changing numbers.</h2>
          <p>
            Monitor keeps the fast overview, then gives you a direct route into
            the process, profile, history, or incident behind the spike.
          </p>
          <a :href="withBase('/guide/anomaly-detection')">
            How anomaly detection works <span aria-hidden="true">→</span>
          </a>
        </div>

        <div class="diagnosis-list">
          <article>
            <span>01</span>
            <div>
              <h3>Detect drift</h3>
              <p>
                Catch CPU spikes against rolling per-process baselines, RSS
                growth with regression, zombies, disk fill, and swap pressure.
              </p>
            </div>
          </article>
          <article>
            <span>02</span>
            <div>
              <h3>Trace the cause</h3>
              <p>
                Move from a hot PID to process details, pprof data for Go, or a
                macOS sample without changing tools.
              </p>
            </div>
          </article>
          <article>
            <span>03</span>
            <div>
              <h3>Keep the evidence</h3>
              <p>
                Capture logs, history, baselines, diffs, and incident bundles so
                the diagnosis survives after the machine settles down.
              </p>
            </div>
          </article>
        </div>
      </div>
    </section>

    <section class="safety-section home-shell">
      <div class="safety-copy">
        <span class="section-kicker">Agent-safe by construction</span>
        <h2>Read freely. Mutate deliberately.</h2>
        <p>
          Read-only MCP tools return bounded, structured context. Actions such
          as killing or profiling require <code>confirm: true</code>, then pass
          the same protected-process checks used by Studio and the CLI.
        </p>
        <a :href="withBase('/guide/safety')">
          Review the safety model <span aria-hidden="true">→</span>
        </a>
      </div>

      <figure class="safety-code">
        <figcaption>
          <span>MCP tool call</span>
          <span>mutating</span>
        </figcaption>
        <strong>monitor_kill</strong>
        <pre><code>{
  "pid": 8421,
  "force": false,
  "confirm": true
}</code></pre>
        <p><span aria-hidden="true" /> protection checks still apply</p>
      </figure>
    </section>

    <section class="home-section install-section">
      <div class="home-shell">
        <header class="section-heading install-heading">
          <div>
            <span class="section-kicker">Install Monitor</span>
            <h2>Homebrew first. Other paths when you need them.</h2>
          </div>
          <p>
            Start with the maintained tap, download a release archive, or build
            from source. All paths end with the same single binary.
          </p>
        </header>

        <InstallPanel />
      </div>
    </section>

    <section class="home-cta">
      <div class="home-shell cta-inner">
        <div>
          <span class="section-kicker">No daemon. No account. No cloud.</span>
          <h2>Make the next performance mystery shorter.</h2>
        </div>
        <div class="cta-actions">
          <a class="home-button primary" :href="withBase('/guide/getting-started')">
            Open the guide
          </a>
          <a class="home-button secondary" :href="withBase('/guide/installation')">
            Installation details
          </a>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.monitor-home {
  --home-ink: var(--vp-c-text-1);
  --home-muted: var(--vp-c-text-2);
  --home-line: var(--vp-c-divider);
  overflow: hidden;
  color: var(--home-ink);
}

.home-shell {
  width: min(1180px, calc(100% - 48px));
  margin: 0 auto;
}

.home-hero {
  position: relative;
  padding: clamp(72px, 10vw, 132px) 0 68px;
  background:
    radial-gradient(circle at 72% 24%, rgba(196, 61, 61, 0.12), transparent 30%),
    linear-gradient(180deg, var(--vp-c-bg) 0%, var(--vp-c-bg) 78%, var(--vp-c-bg-soft) 100%);
}

.home-hero::before {
  position: absolute;
  inset: 0;
  background-image: linear-gradient(var(--home-line) 1px, transparent 1px),
    linear-gradient(90deg, var(--home-line) 1px, transparent 1px);
  background-size: 64px 64px;
  content: '';
  mask-image: linear-gradient(to bottom, rgba(0, 0, 0, 0.16), transparent 68%);
  pointer-events: none;
}

.hero-grid {
  position: relative;
  display: grid;
  grid-template-columns: minmax(0, 0.98fr) minmax(500px, 1.02fr);
  gap: clamp(48px, 7vw, 94px);
  align-items: center;
}

.hero-copy {
  position: relative;
  z-index: 2;
}

.hero-kicker,
.section-kicker {
  color: var(--vp-c-brand-1);
  font-family: var(--vp-font-family-mono);
  font-size: 11px;
  font-weight: 730;
  letter-spacing: 0.105em;
  text-transform: uppercase;
}

.hero-kicker {
  display: flex;
  align-items: center;
  gap: 9px;
}

.hero-kicker span {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--vp-c-brand-1);
  box-shadow: 0 0 0 4px color-mix(in srgb, var(--vp-c-brand-1) 13%, transparent);
}

.hero-copy h1 {
  max-width: 720px;
  margin: 20px 0 22px;
  color: var(--home-ink);
  font-size: clamp(43px, 4.8vw, 64px);
  font-weight: 760;
  letter-spacing: -0.057em;
  line-height: 0.99;
}

.hero-lede {
  max-width: 58ch;
  margin: 0;
  color: var(--home-muted);
  font-size: clamp(16px, 1.7vw, 19px);
  line-height: 1.68;
}

.hero-command {
  max-width: 620px;
  margin-top: 28px;
}

.hero-command :deep(code) {
  font-size: 11px;
}

.hero-command :deep(button) {
  min-width: 78px;
}

.hero-actions,
.cta-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
}

.hero-actions {
  margin-top: 18px;
}

.home-button {
  display: inline-flex;
  min-height: 44px;
  align-items: center;
  justify-content: center;
  gap: 7px;
  border: 1px solid transparent;
  border-radius: 10px;
  padding: 0 18px;
  font-size: 13px;
  font-weight: 720;
  text-decoration: none;
  transition: background-color 170ms ease, border-color 170ms ease,
    color 170ms ease, transform 170ms ease;
}

.home-button:hover {
  text-decoration: none;
}

.home-button:active {
  transform: scale(0.98);
}

.home-button:focus-visible,
.surface-main a:focus-visible,
.surface-side a:focus-visible,
.diagnosis-intro a:focus-visible,
.safety-copy a:focus-visible {
  border-radius: 5px;
  outline: 2px solid var(--vp-c-brand-1);
  outline-offset: 4px;
}

.home-button.primary {
  background: var(--vp-c-brand-1);
  color: var(--vp-button-brand-text);
}

.home-button.primary:hover {
  background: var(--vp-c-brand-2);
}

.home-button.secondary {
  border-color: var(--home-line);
  background: color-mix(in srgb, var(--vp-c-bg) 88%, transparent);
  color: var(--home-ink);
}

.home-button.secondary:hover {
  border-color: color-mix(in srgb, var(--vp-c-brand-1) 40%, var(--home-line));
  color: var(--vp-c-brand-1);
}

.hero-support {
  display: flex;
  flex-wrap: wrap;
  gap: 7px 18px;
  margin-top: 22px;
  color: var(--vp-c-text-3);
  font-family: var(--vp-font-family-mono);
  font-size: 10px;
}

.hero-support span {
  position: relative;
}

.hero-support span + span::before {
  position: absolute;
  top: 50%;
  left: -11px;
  width: 3px;
  height: 3px;
  border-radius: 50%;
  background: var(--vp-c-text-3);
  content: '';
}

.hero-visual {
  position: relative;
  z-index: 1;
  min-width: 0;
  padding: 35px 0 27px;
}

.hero-visual::before {
  position: absolute;
  z-index: -1;
  inset: 2% 5% 0 10%;
  border: 1px solid color-mix(in srgb, var(--vp-c-brand-1) 20%, transparent);
  border-radius: 28px;
  background: color-mix(in srgb, var(--vp-c-brand-1) 5%, transparent);
  content: '';
  transform: rotate(2.5deg);
}

.preview-meta {
  display: flex;
  justify-content: space-between;
  margin: 0 8px 10px;
  color: var(--vp-c-text-3);
  font-family: var(--vp-font-family-mono);
  font-size: 9px;
  letter-spacing: 0.07em;
  text-transform: uppercase;
}

.preview-note {
  position: absolute;
  right: -14px;
  bottom: 4px;
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 176px;
  border: 1px solid rgba(255, 255, 255, 0.12);
  border-radius: 10px;
  padding: 11px 13px;
  background: #202630;
  color: #a9b2bf;
  box-shadow: 0 12px 34px rgba(14, 18, 24, 0.22);
  font-family: var(--vp-font-family-mono);
  font-size: 9px;
}

.preview-note strong {
  color: #f4f6f8;
  font-size: 10px;
}

.signal-rail {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  margin-top: -1px;
  border-top: 1px solid var(--home-line);
  border-bottom: 1px solid var(--home-line);
}

.signal-rail div {
  padding: 25px 24px;
  border-right: 1px solid var(--home-line);
}

.signal-rail div:first-child {
  padding-left: 0;
}

.signal-rail div:last-child {
  padding-right: 0;
  border-right: 0;
}

.signal-rail dt {
  color: var(--home-ink);
  font-size: 13px;
  font-weight: 720;
}

.signal-rail dd {
  margin: 4px 0 0;
  color: var(--home-muted);
  font-size: 11px;
}

.home-section {
  padding: clamp(82px, 10vw, 132px) 0;
}

.section-heading {
  display: grid;
  grid-template-columns: minmax(0, 1.1fr) minmax(300px, 0.9fr);
  gap: 56px;
  align-items: end;
  margin-bottom: 44px;
}

.section-heading h2,
.diagnosis-intro h2,
.safety-copy h2,
.home-cta h2 {
  margin: 12px 0 0;
  color: var(--home-ink);
  font-size: clamp(34px, 4.5vw, 54px);
  font-weight: 750;
  letter-spacing: -0.052em;
  line-height: 1.04;
}

.section-heading p {
  max-width: 52ch;
  margin: 0;
  color: var(--home-muted);
  font-size: 15px;
  line-height: 1.7;
}

.surface-layout {
  display: grid;
  grid-template-columns: minmax(0, 1.08fr) minmax(0, 0.92fr);
  gap: 16px;
}

.surface-main,
.surface-side article {
  position: relative;
  overflow: hidden;
  border: 1px solid var(--home-line);
  border-radius: 20px;
  background: var(--vp-c-bg);
}

.surface-main {
  min-height: 526px;
  padding: clamp(28px, 4vw, 48px);
  background:
    linear-gradient(150deg, color-mix(in srgb, var(--vp-c-brand-1) 7%, transparent), transparent 48%),
    var(--vp-c-bg);
}

.surface-main::after {
  position: absolute;
  right: -20%;
  bottom: -38%;
  width: 78%;
  aspect-ratio: 1;
  border: 1px solid color-mix(in srgb, var(--vp-c-brand-1) 18%, transparent);
  border-radius: 50%;
  content: '';
}

.surface-index {
  color: var(--vp-c-brand-1);
  font-family: var(--vp-font-family-mono);
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 0.09em;
  text-transform: uppercase;
}

.surface-main h3,
.surface-side h3 {
  border: 0;
  color: var(--home-ink);
  letter-spacing: -0.035em;
  line-height: 1.1;
}

.surface-main h3 {
  max-width: 12ch;
  margin: 58px 0 16px;
  padding: 0;
  font-size: clamp(32px, 4vw, 47px);
}

.surface-main > p {
  max-width: 52ch;
  margin: 0;
  color: var(--home-muted);
  font-size: 14px;
  line-height: 1.7;
}

.surface-command {
  display: flex;
  width: fit-content;
  align-items: center;
  gap: 10px;
  margin-top: 28px;
  border: 1px solid var(--home-line);
  border-radius: 8px;
  padding: 9px 12px;
  background: var(--vp-c-bg-soft);
  color: var(--vp-c-text-2);
  font-family: var(--vp-font-family-mono);
  font-size: 12px;
}

.surface-command span {
  color: var(--vp-c-brand-1);
}

.surface-main ul {
  display: grid;
  gap: 8px;
  margin: 32px 0 36px;
  padding: 0;
  color: var(--home-muted);
  font-size: 12px;
  list-style: none;
}

.surface-main li {
  display: flex;
  align-items: center;
  gap: 9px;
}

.surface-main li::before {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: var(--vp-c-brand-1);
  content: '';
}

.surface-main a,
.surface-side a,
.diagnosis-intro a,
.safety-copy a {
  position: relative;
  z-index: 1;
  color: var(--vp-c-brand-1);
  font-size: 12px;
  font-weight: 720;
  text-decoration: none;
}

.surface-main a:hover,
.surface-side a:hover,
.diagnosis-intro a:hover,
.safety-copy a:hover {
  text-decoration: underline;
  text-underline-offset: 4px;
}

.surface-side {
  display: grid;
  gap: 16px;
}

.surface-side article {
  padding: 28px 30px;
}

.surface-side h3 {
  margin: 22px 0 10px;
  padding: 0;
  font-size: 24px;
}

.surface-side p {
  margin: 0;
  color: var(--home-muted);
  font-size: 12px;
  line-height: 1.65;
}

.code-sample {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin: 18px 0;
  border: 1px solid var(--home-line);
  border-radius: 9px;
  padding: 11px 13px;
  background: var(--vp-c-bg-soft);
  font-family: var(--vp-font-family-mono);
  font-size: 9px;
}

.code-sample code {
  color: var(--home-ink);
}

.code-sample span {
  color: var(--vp-c-text-3);
}

.tool-line {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  margin-top: 9px;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--home-line);
  font-family: var(--vp-font-family-mono);
  font-size: 9px;
}

.tool-line:first-of-type {
  margin-top: 18px;
}

.tool-line span:first-child {
  color: var(--home-ink);
}

.tool-line span:last-child {
  color: var(--vp-c-text-3);
}

.surface-side .tool-line + a {
  display: inline-block;
  margin-top: 18px;
}

.diagnosis-section {
  border-top: 1px solid var(--home-line);
  border-bottom: 1px solid var(--home-line);
  background: var(--vp-c-bg-soft);
}

.diagnosis-grid {
  display: grid;
  grid-template-columns: minmax(0, 0.8fr) minmax(420px, 1.2fr);
  gap: clamp(58px, 9vw, 120px);
}

.diagnosis-intro {
  align-self: start;
}

.diagnosis-intro h2 {
  margin-bottom: 20px;
}

.diagnosis-intro p {
  margin: 0 0 24px;
  color: var(--home-muted);
  font-size: 14px;
  line-height: 1.7;
}

.diagnosis-list {
  border-top: 1px solid var(--home-line);
}

.diagnosis-list article {
  display: grid;
  grid-template-columns: 38px minmax(0, 1fr);
  gap: 20px;
  padding: 30px 0;
  border-bottom: 1px solid var(--home-line);
}

.diagnosis-list article > span {
  color: var(--vp-c-brand-1);
  font-family: var(--vp-font-family-mono);
  font-size: 10px;
  font-weight: 700;
}

.diagnosis-list h3 {
  margin: 0 0 8px;
  border: 0;
  padding: 0;
  color: var(--home-ink);
  font-size: 20px;
  letter-spacing: -0.025em;
}

.diagnosis-list p {
  margin: 0;
  color: var(--home-muted);
  font-size: 13px;
  line-height: 1.65;
}

.safety-section {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(360px, 0.78fr);
  gap: clamp(54px, 9vw, 120px);
  align-items: center;
  margin-top: clamp(82px, 10vw, 132px);
  margin-bottom: clamp(82px, 10vw, 132px);
  border-radius: 28px;
  padding: clamp(34px, 6vw, 72px);
  background: #20252d;
  color: #f5f7f9;
  box-shadow: 0 30px 80px rgba(20, 24, 30, 0.16);
}

.safety-copy h2 {
  color: #f5f7f9;
}

.safety-copy p {
  max-width: 58ch;
  margin: 20px 0 24px;
  color: #aeb6c2;
  font-size: 14px;
  line-height: 1.7;
}

.safety-copy code {
  color: #ffc2bd;
  font-family: var(--vp-font-family-mono);
}

.safety-code {
  overflow: hidden;
  margin: 0;
  border: 1px solid rgba(255, 255, 255, 0.11);
  border-radius: 14px;
  background: #171c23;
  box-shadow: inset 0 1px 0 rgba(255, 255, 255, 0.04);
}

.safety-code > figcaption {
  display: flex;
  justify-content: space-between;
  padding: 10px 14px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.08);
  color: #737f90;
  font-family: var(--vp-font-family-mono);
  font-size: 8px;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.safety-code > figcaption span:last-child {
  color: #ff8a80;
}

.safety-code > strong {
  display: block;
  padding: 18px 18px 0;
  color: #ff9d96;
  font-family: var(--vp-font-family-mono);
  font-size: 12px;
}

.safety-code pre {
  margin: 0;
  padding: 12px 18px 18px;
  background: transparent;
  color: #dce2e9;
  font-family: var(--vp-font-family-mono);
  font-size: 11px;
  line-height: 1.7;
}

.safety-code p {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0;
  padding: 10px 14px;
  border-top: 1px solid rgba(255, 255, 255, 0.08);
  color: #7f8b9a;
  font-size: 9px;
}

.safety-code p span {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: #70b99a;
}

.install-section {
  border-top: 1px solid var(--home-line);
  background: var(--vp-c-bg-soft);
}

.install-heading {
  margin-bottom: 36px;
}

.home-cta {
  padding: 74px 0;
  background: var(--vp-c-bg);
}

.cta-inner {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 40px;
}

.home-cta h2 {
  max-width: 780px;
  font-size: clamp(30px, 4vw, 46px);
}

.cta-actions {
  flex: 0 0 auto;
}

@media (max-width: 1040px) {
  .hero-grid {
    grid-template-columns: 1fr;
    gap: 44px;
  }

  .hero-copy {
    max-width: 760px;
  }

  .hero-visual {
    width: min(760px, 100%);
    margin-left: auto;
  }

  .surface-layout,
  .diagnosis-grid,
  .safety-section {
    grid-template-columns: 1fr;
  }

  .surface-main {
    min-height: auto;
  }

  .surface-main h3 {
    margin-top: 36px;
  }

  .surface-side {
    grid-template-columns: 1fr 1fr;
  }

  .safety-code {
    width: min(560px, 100%);
  }
}

@media (max-width: 760px) {
  .home-shell {
    width: min(100% - 32px, 1180px);
  }

  .home-hero {
    padding-top: 58px;
  }

  .hero-copy h1 {
    font-size: clamp(42px, 13vw, 60px);
  }

  .preview-note {
    right: 8px;
  }

  .signal-rail {
    grid-template-columns: 1fr 1fr;
  }

  .signal-rail div,
  .signal-rail div:first-child,
  .signal-rail div:last-child {
    padding: 18px 14px;
    border-right: 1px solid var(--home-line);
    border-bottom: 1px solid var(--home-line);
  }

  .signal-rail div:nth-child(2n) {
    border-right: 0;
  }

  .signal-rail div:nth-last-child(-n + 2) {
    border-bottom: 0;
  }

  .section-heading {
    grid-template-columns: 1fr;
    gap: 20px;
  }

  .surface-side {
    grid-template-columns: 1fr;
  }

  .safety-section {
    width: min(100% - 24px, 1180px);
    border-radius: 20px;
  }

  .cta-inner {
    align-items: flex-start;
    flex-direction: column;
  }
}

@media (max-width: 520px) {
  .home-hero {
    padding-top: 46px;
  }

  .hero-kicker {
    align-items: flex-start;
    line-height: 1.5;
  }

  .hero-copy h1 {
    margin-top: 16px;
  }

  .hero-actions,
  .hero-actions .home-button,
  .cta-actions,
  .cta-actions .home-button {
    width: 100%;
  }

  .hero-support span + span::before {
    display: none;
  }

  .preview-meta span:last-child,
  .preview-note {
    display: none;
  }

  .hero-visual {
    padding-bottom: 0;
  }

  .section-heading h2,
  .diagnosis-intro h2,
  .safety-copy h2 {
    font-size: 36px;
  }

  .surface-main,
  .surface-side article {
    border-radius: 15px;
  }

  .surface-main {
    padding: 26px 22px;
  }

  .surface-main h3 {
    font-size: 34px;
  }

  .surface-main::after {
    display: none;
  }

  .surface-side article {
    padding: 24px 22px;
  }
}

@media (prefers-reduced-motion: reduce) {
  .home-button {
    transition: none;
  }
}
</style>
