<script setup lang="ts">
import { withBase } from 'vitepress'
import CommandCopy from './CommandCopy.vue'
import InstallPanel from './InstallPanel.vue'
import SignalFlow from './SignalFlow.vue'
import TerminalMockup from './TerminalMockup.vue'

const installCommand = 'brew install --cask abdul-hamid-achik/tap/monitor'
</script>

<template>
  <div class="monitor-home">
    <section class="home-hero">
      <div class="hero-grid home-shell">
        <div class="hero-copy">
          <a
            class="release-pill"
            href="https://github.com/abdul-hamid-achik/monitor/releases/tag/v1.15.1"
          >
            <span aria-hidden="true" />
            v1.15.1 is live
            <i aria-hidden="true">↗</i>
          </a>

          <p class="hero-kicker">Local observability for developers and agents</p>
          <h1>
            Catch the process
            <span>before the evidence disappears.</span>
          </h1>
          <p class="hero-lede">
            Monitor turns volatile machine state into grouped local issues,
            bounded diagnoses, and durable evidence—without an account, a
            daemon, or a cloud dependency.
          </p>

          <div class="hero-actions">
            <a class="home-button primary" :href="withBase('/guide/getting-started')">
              Start investigating
              <span aria-hidden="true">→</span>
            </a>
            <a class="home-button secondary" :href="withBase('/guide/mcp')">
              Connect an agent
            </a>
          </div>

          <CommandCopy class="hero-command" :command="installCommand" />

          <div class="hero-proof" aria-label="Supported interfaces and platforms">
            <span><i aria-hidden="true" /> Local-first</span>
            <span>Studio + JSON + MCP</span>
            <span>macOS + Linux</span>
          </div>
        </div>

        <div class="hero-visual">
          <div class="visual-label">
            <span>Live machine state</span>
            <span><i aria-hidden="true" /> collecting · 1s</span>
          </div>
          <TerminalMockup />

          <aside class="issue-receipt" aria-label="Detected issue summary">
            <div>
              <span>Issue · ISS-5F8E3A1C9D42B760</span>
              <strong>CPU spike recurring</strong>
            </div>
            <dl>
              <div><dt>process</dt><dd>node · 8421</dd></div>
              <div><dt>events</dt><dd>7 grouped</dd></div>
              <div><dt>evidence</dt><dd>verified</dd></div>
            </dl>
          </aside>
        </div>
      </div>
    </section>

    <dl class="signal-rail home-shell">
      <div>
        <dt>01</dt>
        <dd><strong>One collector</strong><span>Every interface sees the same facts</span></dd>
      </div>
      <div>
        <dt>02</dt>
        <dd><strong>Local issue model</strong><span>Events grouped into durable threads</span></dd>
      </div>
      <div>
        <dt>03</dt>
        <dd><strong>Bounded by default</strong><span>Stable JSON and intentional limits</span></dd>
      </div>
      <div>
        <dt>04</dt>
        <dd><strong>Agent-safe actions</strong><span>Typed input plus confirmation gates</span></dd>
      </div>
    </dl>

    <section class="home-section investigation-section">
      <div class="home-shell">
        <header class="section-heading investigation-heading">
          <div>
            <span class="section-kicker">A complete local investigation</span>
            <h2>From a transient spike to evidence you can keep.</h2>
          </div>
          <p>
            Monitor follows one coherent path from live signal to issue,
            diagnosis, and incident bundle. Select a stage to see the contract
            exposed to both humans and agents.
          </p>
        </header>

        <SignalFlow />
      </div>
    </section>

    <section class="home-section surfaces-section">
      <div class="home-shell">
        <header class="section-heading">
          <div>
            <span class="section-kicker">One model, three surfaces</span>
            <h2>Stay visual. Go scriptable. Hand off safely.</h2>
          </div>
          <p>
            Use the interface that matches the moment without changing the
            underlying process model, safety rules, or evidence format.
          </p>
        </header>

        <div class="surface-grid">
          <article class="surface-card studio-card">
            <div class="card-topline"><span>01 / Studio</span><i>human</i></div>
            <div class="studio-card-copy">
              <h3>The machine, composed for focus.</h3>
              <p>
                Nine keyboard-driven views keep live metrics, trends,
                processes, settings, and actions close without becoming a wall of
                dashboards.
              </p>
              <a :href="withBase('/guide/tui')">Explore Studio <span aria-hidden="true">→</span></a>
            </div>
            <div class="studio-mini" aria-hidden="true">
              <div><span>CPU</span><strong>82.3%</strong><i>high</i></div>
              <svg viewBox="0 0 420 96" preserveAspectRatio="none">
                <path d="M0 76 L34 70 L68 73 L102 50 L136 59 L170 43 L204 47 L238 22 L272 35 L306 14 L340 27 L374 9 L420 18" />
              </svg>
            </div>
          </article>

          <article class="surface-card cli-card">
            <div class="card-topline"><span>02 / CLI</span><i>automation</i></div>
            <h3>Signal that pipes cleanly.</h3>
            <p>
              One-shot JSON, streaming NDJSON, stable exit codes, and bounded
              outputs fit scripts and CI without a translation layer.
            </p>
            <pre><code><span>$</span> monitor snapshot --compact | jq '.cpu.usage_percent'
18.4</code></pre>
            <a :href="withBase('/guide/cli')">Read the CLI guide <span aria-hidden="true">→</span></a>
          </article>

          <article class="surface-card mcp-card">
            <div class="card-topline"><span>03 / MCP</span><i>agents</i></div>
            <h3>Context with sharp edges.</h3>
            <p>
              Ten typed tools advertise read-only, destructive, and idempotent
              behavior. Mutations require confirmation and still honor process
              protection.
            </p>
            <div class="tool-stack">
              <div><code>monitor_analyze</code><span>read-only</span></div>
              <div><code>monitor_issues</code><span>read-only</span></div>
              <div><code>monitor_kill</code><span>confirm</span></div>
            </div>
            <a :href="withBase('/guide/mcp')">Configure MCP <span aria-hidden="true">→</span></a>
          </article>
        </div>
      </div>
    </section>

    <section class="ecosystem-section">
      <div class="home-shell ecosystem-grid">
        <div class="ecosystem-copy">
          <span class="section-kicker">Local developer stack</span>
          <h2>Built to outlive an ephemeral environment.</h2>
          <p>
            Monitor observes the run. Chalupa provides the environment.
            file.cheap retains its artifacts. codemap and vecgrep add code and
            semantic context when an investigation needs to go deeper.
          </p>
          <a :href="withBase('/guide/ecosystem')">
            See integration contracts <span aria-hidden="true">→</span>
          </a>
        </div>

        <div
          class="ecosystem-map"
          role="group"
          aria-label="Monitor ecosystem integration map"
        >
          <div class="ecosystem-core">
            <img :src="withBase('/favicon.svg')" alt="" />
            <span>observe + group</span>
            <strong>Monitor</strong>
          </div>
          <div class="ecosystem-node chalupa"><span>environment</span><strong>chalupa.run</strong></div>
          <div class="ecosystem-node fcheap"><span>artifacts</span><strong>file.cheap</strong></div>
          <div class="ecosystem-node codemap"><span>code context</span><strong>codemap</strong></div>
          <div class="ecosystem-node vecgrep"><span>semantic search</span><strong>vecgrep</strong></div>
          <svg viewBox="0 0 600 360" preserveAspectRatio="none" aria-hidden="true">
            <path d="M300 180 C218 180 208 76 128 76" />
            <path d="M300 180 C382 180 392 76 472 76" />
            <path d="M300 180 C218 180 208 284 128 284" />
            <path d="M300 180 C382 180 392 284 472 284" />
          </svg>
        </div>
      </div>
    </section>

    <section class="safety-section home-shell">
      <div class="safety-copy">
        <span class="section-kicker">Agent-safe by construction</span>
        <h2>Read freely.<br />Mutate deliberately.</h2>
        <p>
          Read-only tools return bounded structured context. Actions such as
          killing or profiling require <code>confirm: true</code>, then pass the
          same protected-process checks used by Studio and the CLI.
        </p>
        <a :href="withBase('/guide/safety')">Review the safety model <span aria-hidden="true">→</span></a>
      </div>

      <figure class="safety-code">
        <figcaption><span>MCP request</span><i>destructiveHint: true</i></figcaption>
        <strong>monitor_kill</strong>
        <pre><code>{
  "pid": <span>8421</span>,
  "force": <span>false</span>,
  "confirm": <span>true</span>
}</code></pre>
        <p><i aria-hidden="true" /> typed input accepted · protection checks pending</p>
      </figure>
    </section>

    <section class="home-section install-section">
      <div class="home-shell">
        <header class="section-heading install-heading">
          <div>
            <span class="section-kicker">Install Monitor</span>
            <h2>One binary. Your machine stays yours.</h2>
          </div>
          <p>
            Start with the maintained Homebrew tap, download a release archive,
            or build from source. No account or background service required.
          </p>
        </header>
        <InstallPanel />
      </div>
    </section>

    <section class="home-cta">
      <div class="home-shell cta-inner">
        <div>
          <span class="section-kicker">The next incident starts locally</span>
          <h2>Keep the signal.<br />Lose the guesswork.</h2>
        </div>
        <div class="cta-side">
          <p>Install Monitor, open Studio, and turn the next unexplained spike into evidence.</p>
          <div class="cta-actions">
            <a class="home-button primary" :href="withBase('/guide/getting-started')">Open the guide <span aria-hidden="true">→</span></a>
            <a class="home-button secondary" href="https://github.com/abdul-hamid-achik/monitor">View source</a>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.monitor-home {
  --home-line: var(--vp-c-divider);
  overflow: hidden;
  color: var(--vp-c-text-1);
}

.home-shell {
  width: min(1240px, calc(100% - 56px));
  margin: 0 auto;
}

.home-hero {
  position: relative;
  padding: clamp(86px, 9vw, 132px) 0 112px;
  background: linear-gradient(180deg, var(--vp-c-bg) 0%, var(--vp-c-bg) 74%, var(--vp-c-bg-alt) 100%);
}

.home-hero::before {
  position: absolute;
  inset: 0;
  background-image: linear-gradient(var(--home-line) 1px, transparent 1px),
    linear-gradient(90deg, var(--home-line) 1px, transparent 1px);
  background-size: 72px 72px;
  content: '';
  mask-image: linear-gradient(to bottom, rgba(0, 0, 0, 0.22), transparent 68%);
  opacity: 0.55;
  pointer-events: none;
}

.hero-grid {
  position: relative;
  display: grid;
  grid-template-columns: minmax(0, 0.78fr) minmax(590px, 1.22fr);
  gap: clamp(54px, 7vw, 104px);
  align-items: center;
}

.hero-copy {
  position: relative;
  z-index: 2;
}

.release-pill {
  display: inline-flex;
  align-items: center;
  gap: 9px;
  margin-bottom: 32px;
  border: 1px solid var(--vp-c-divider);
  border-radius: 999px;
  padding: 7px 11px;
  background: color-mix(in srgb, var(--vp-c-bg-elv) 76%, transparent);
  color: var(--vp-c-text-2);
  font-family: var(--vp-font-family-mono);
  font-size: 10px;
  font-weight: 620;
  letter-spacing: 0.04em;
  text-decoration: none;
  transition: border-color 160ms ease, color 160ms ease, transform 160ms ease;
}

.release-pill:hover {
  border-color: color-mix(in srgb, var(--vp-c-brand-1) 48%, var(--vp-c-divider));
  color: var(--vp-c-text-1);
  transform: translateY(-1px);
}

.release-pill > span {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: #55a87d;
  box-shadow: 0 0 0 4px rgba(85, 168, 125, 0.12);
}

.release-pill i {
  color: var(--vp-c-brand-1);
  font-style: normal;
}

.hero-kicker,
.section-kicker {
  margin: 0;
  color: var(--vp-c-brand-1);
  font-family: var(--vp-font-family-mono);
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 0.13em;
  text-transform: uppercase;
}

.hero-copy h1 {
  max-width: 10.8ch;
  margin: 18px 0 24px;
  color: var(--vp-c-text-1);
  font-size: clamp(52px, 6.2vw, 78px);
  font-weight: 685;
  letter-spacing: -0.064em;
  line-height: 0.96;
}

.hero-copy h1 span {
  display: block;
  color: var(--vp-c-text-3);
}

.hero-lede {
  max-width: 55ch;
  margin: 0;
  color: var(--vp-c-text-2);
  font-size: clamp(16px, 1.55vw, 18px);
  line-height: 1.68;
}

.hero-actions,
.cta-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  margin-top: 32px;
}

.home-button {
  display: inline-flex;
  min-height: 45px;
  align-items: center;
  justify-content: center;
  gap: 12px;
  border: 1px solid var(--vp-c-divider);
  border-radius: 10px;
  padding: 0 18px;
  font-size: 13px;
  font-weight: 680;
  text-decoration: none;
  transition: background-color 160ms ease, border-color 160ms ease, color 160ms ease, transform 160ms ease;
}

.home-button:hover {
  transform: translateY(-2px);
}

.home-button.primary {
  border-color: var(--vp-c-brand-1);
  background: var(--vp-c-brand-1);
  color: var(--vp-button-brand-text);
  box-shadow: 0 13px 28px color-mix(in srgb, var(--vp-c-brand-1) 20%, transparent);
}

.home-button.primary:hover {
  border-color: var(--vp-c-brand-2);
  background: var(--vp-c-brand-2);
}

.home-button.secondary {
  background: color-mix(in srgb, var(--vp-c-bg-elv) 82%, transparent);
  color: var(--vp-c-text-1);
}

.home-button.secondary:hover {
  border-color: color-mix(in srgb, var(--vp-c-text-1) 28%, var(--vp-c-divider));
  background: var(--vp-c-bg-elv);
}

.hero-command {
  max-width: 500px;
  margin-top: 16px;
}

.hero-proof {
  display: flex;
  flex-wrap: wrap;
  gap: 10px 18px;
  margin-top: 22px;
  color: var(--vp-c-text-3);
  font-size: 10px;
  font-weight: 570;
}

.hero-proof span {
  display: inline-flex;
  align-items: center;
  gap: 7px;
}

.hero-proof i {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: #55a87d;
}

.hero-visual {
  position: relative;
  z-index: 1;
  min-width: 0;
  padding-bottom: 72px;
}

.hero-visual::before {
  position: absolute;
  top: -40px;
  right: -38px;
  width: 78%;
  height: 76%;
  border-radius: 50%;
  background: color-mix(in srgb, var(--vp-c-brand-1) 10%, transparent);
  content: '';
  filter: blur(58px);
  pointer-events: none;
}

.visual-label {
  position: relative;
  display: flex;
  justify-content: space-between;
  margin: 0 2px 12px;
  color: var(--vp-c-text-3);
  font-family: var(--vp-font-family-mono);
  font-size: 9px;
  letter-spacing: 0.04em;
  text-transform: uppercase;
}

.visual-label span:last-child {
  display: inline-flex;
  align-items: center;
  gap: 7px;
}

.visual-label i {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: #55a87d;
}

.hero-visual :deep(.terminal-mockup) {
  position: relative;
  z-index: 1;
  transform: perspective(1600px) rotateY(-2deg) rotateX(0.7deg);
  transform-origin: center left;
}

.issue-receipt {
  position: absolute;
  z-index: 2;
  right: -24px;
  bottom: 0;
  width: min(420px, 72%);
  overflow: hidden;
  border: 1px solid color-mix(in srgb, var(--vp-c-brand-1) 34%, var(--vp-c-divider));
  border-radius: 15px;
  background: color-mix(in srgb, var(--vp-c-bg-elv) 94%, transparent);
  box-shadow: var(--monitor-shadow-lg);
  backdrop-filter: blur(18px);
}

.issue-receipt > div:first-child {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 16px 18px 14px;
  border-bottom: 1px solid var(--vp-c-divider);
}

.issue-receipt span,
.issue-receipt dt {
  color: var(--vp-c-text-3);
  font-family: var(--vp-font-family-mono);
  font-size: 8px;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.issue-receipt strong {
  font-size: 14px;
  font-weight: 680;
}

.issue-receipt dl {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  margin: 0;
}

.issue-receipt dl div {
  min-width: 0;
  padding: 11px 14px 13px;
  border-right: 1px solid var(--vp-c-divider);
}

.issue-receipt dl div:last-child { border-right: 0; }
.issue-receipt dd { overflow: hidden; margin: 4px 0 0; font-size: 10px; font-weight: 620; text-overflow: ellipsis; white-space: nowrap; }

.signal-rail {
  position: relative;
  z-index: 3;
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  margin-top: -40px;
  border: 1px solid var(--vp-c-divider);
  border-radius: 18px;
  background: var(--vp-c-bg-elv);
  box-shadow: var(--monitor-shadow-md);
}

.signal-rail > div {
  display: flex;
  min-width: 0;
  gap: 14px;
  padding: 22px 24px;
  border-right: 1px solid var(--vp-c-divider);
}

.signal-rail > div:last-child { border-right: 0; }
.signal-rail dt { color: var(--vp-c-brand-1); font-family: var(--vp-font-family-mono); font-size: 9px; font-weight: 700; }
.signal-rail dd { display: flex; min-width: 0; flex-direction: column; gap: 4px; margin: 0; }
.signal-rail strong { font-size: 12px; font-weight: 680; }
.signal-rail dd span { overflow: hidden; color: var(--vp-c-text-3); font-size: 10px; text-overflow: ellipsis; white-space: nowrap; }

.home-section { padding: clamp(100px, 12vw, 154px) 0; }
.investigation-section { background: var(--vp-c-bg-alt); }
.surfaces-section { background: var(--vp-c-bg); }

.section-heading {
  display: grid;
  grid-template-columns: minmax(0, 1.25fr) minmax(300px, 0.75fr);
  gap: 56px;
  align-items: end;
  margin-bottom: 54px;
}

.section-heading h2,
.ecosystem-copy h2,
.safety-copy h2,
.home-cta h2 {
  max-width: 13ch;
  margin: 14px 0 0;
  color: var(--vp-c-text-1);
  font-size: clamp(40px, 5.5vw, 64px);
  font-weight: 670;
  letter-spacing: -0.058em;
  line-height: 0.99;
}

.section-heading > p {
  max-width: 48ch;
  margin: 0 0 3px;
  color: var(--vp-c-text-2);
  font-size: 15px;
  line-height: 1.72;
}

.surface-grid {
  display: grid;
  grid-template-columns: 1.16fr 0.84fr;
  gap: 18px;
}

.surface-card {
  position: relative;
  overflow: hidden;
  border: 1px solid var(--vp-c-divider);
  border-radius: 22px;
  padding: clamp(26px, 4vw, 42px);
  background: var(--vp-c-bg-elv);
  box-shadow: var(--monitor-shadow-sm);
}

.studio-card {
  grid-row: span 2;
  display: flex;
  min-height: 610px;
  flex-direction: column;
}

.card-topline {
  display: flex;
  justify-content: space-between;
  color: var(--vp-c-text-3);
  font-family: var(--vp-font-family-mono);
  font-size: 9px;
  letter-spacing: 0.09em;
  text-transform: uppercase;
}

.card-topline i { color: var(--vp-c-brand-1); font-style: normal; }
.studio-card-copy { max-width: 500px; margin-top: auto; padding-top: 62px; }
.surface-card h3 { max-width: 12ch; margin: 0 0 18px; color: var(--vp-c-text-1); font-size: clamp(30px, 4.2vw, 48px); font-weight: 670; letter-spacing: -0.05em; line-height: 1.02; }
.studio-card h3 { max-width: 10ch; }
.surface-card p { max-width: 52ch; margin: 0; color: var(--vp-c-text-2); font-size: 14px; line-height: 1.7; }
.surface-card > h3 { margin-top: 72px; }
.surface-card a,
.ecosystem-copy a,
.safety-copy a { display: inline-flex; gap: 10px; margin-top: 24px; color: var(--vp-c-text-1); font-size: 12px; font-weight: 680; text-decoration: none; }
.surface-card a:hover,
.ecosystem-copy a:hover,
.safety-copy a:hover { color: var(--vp-c-brand-1); }

.studio-mini {
  position: relative;
  min-height: 175px;
  margin-top: 42px;
  overflow: hidden;
  border: 1px solid rgba(255, 255, 255, 0.1);
  border-radius: 14px;
  background: #141b25;
}

.studio-mini > div { display: flex; align-items: baseline; gap: 10px; padding: 22px 24px 0; color: #8e9bad; font-family: var(--vp-font-family-mono); font-size: 9px; text-transform: uppercase; }
.studio-mini strong { color: #eef2f7; font-size: 25px; letter-spacing: -0.06em; }
.studio-mini i { margin-left: auto; color: #ff8a80; font-style: normal; }
.studio-mini svg { position: absolute; right: 0; bottom: 0; left: 0; width: 100%; height: 102px; }
.studio-mini path { fill: none; stroke: #ff8a80; stroke-linecap: round; stroke-width: 2; vector-effect: non-scaling-stroke; }

.cli-card,
.mcp-card { min-height: 296px; }
.cli-card > h3,
.mcp-card > h3 { max-width: 14ch; margin-top: 52px; font-size: clamp(28px, 3.4vw, 38px); }
.cli-card pre { overflow: hidden; margin: 26px 0 0; border: 1px solid rgba(255,255,255,0.1); border-radius: 11px; padding: 15px; background: #141b25; color: #cbd4df; font-family: var(--vp-font-family-mono); font-size: 9px; line-height: 1.7; }
.cli-card pre span { color: #ff8a80; }

.tool-stack { margin-top: 24px; border-top: 1px solid var(--vp-c-divider); }
.tool-stack div { display: flex; align-items: center; justify-content: space-between; padding: 11px 0; border-bottom: 1px solid var(--vp-c-divider); }
.tool-stack code { color: var(--vp-c-text-1); font-size: 10px; }
.tool-stack span { color: var(--vp-c-text-3); font-family: var(--vp-font-family-mono); font-size: 8px; text-transform: uppercase; }

.ecosystem-section { padding: clamp(100px, 12vw, 150px) 0; border-block: 1px solid var(--vp-c-divider); background: var(--vp-c-bg-alt); }
.ecosystem-grid { display: grid; grid-template-columns: minmax(0, 0.72fr) minmax(520px, 1.28fr); gap: clamp(60px, 9vw, 130px); align-items: center; }
.ecosystem-copy h2 { max-width: 10ch; }
.ecosystem-copy p { max-width: 50ch; margin: 24px 0 0; color: var(--vp-c-text-2); font-size: 15px; line-height: 1.72; }
.ecosystem-map { position: relative; min-height: 460px; }
.ecosystem-map > svg { position: absolute; inset: 50px 0; width: 100%; height: calc(100% - 100px); }
.ecosystem-map > svg path { fill: none; stroke: var(--vp-c-divider); stroke-dasharray: 4 7; stroke-width: 1; vector-effect: non-scaling-stroke; }
.ecosystem-core,
.ecosystem-node { position: absolute; z-index: 1; display: flex; flex-direction: column; border: 1px solid var(--vp-c-divider); background: var(--vp-c-bg-elv); box-shadow: var(--monitor-shadow-sm); }
.ecosystem-core { top: 50%; left: 50%; width: 156px; align-items: center; border-radius: 22px; padding: 26px 20px; transform: translate(-50%, -50%); }
.ecosystem-core img { width: 34px; height: 34px; margin-bottom: 17px; }
.ecosystem-core span,
.ecosystem-node span { color: var(--vp-c-text-3); font-family: var(--vp-font-family-mono); font-size: 8px; letter-spacing: 0.07em; text-transform: uppercase; }
.ecosystem-core strong { margin-top: 4px; font-size: 18px; font-weight: 690; }
.ecosystem-node { width: 150px; border-radius: 13px; padding: 15px 17px; }
.ecosystem-node strong { margin-top: 4px; font-size: 12px; font-weight: 660; }
.ecosystem-node.chalupa { top: 28px; left: 20px; }
.ecosystem-node.fcheap { top: 28px; right: 20px; }
.ecosystem-node.codemap { bottom: 28px; left: 20px; }
.ecosystem-node.vecgrep { right: 20px; bottom: 28px; }

.safety-section { display: grid; grid-template-columns: minmax(0, 0.9fr) minmax(460px, 1.1fr); gap: clamp(64px, 10vw, 150px); align-items: center; padding-top: clamp(100px, 12vw, 160px); padding-bottom: clamp(100px, 12vw, 160px); }
.safety-copy h2 { max-width: 11ch; }
.safety-copy p { max-width: 54ch; margin: 24px 0 0; color: var(--vp-c-text-2); font-size: 15px; line-height: 1.72; }
.safety-copy code { color: var(--vp-c-brand-1); }
.safety-code { overflow: hidden; margin: 0; border: 1px solid rgba(255,255,255,0.11); border-radius: 18px; background: #131923; color: #d9e0ea; box-shadow: 0 34px 80px rgba(16,20,28,0.22); font-family: var(--vp-font-family-mono); }
.safety-code figcaption { display: flex; justify-content: space-between; padding: 13px 17px; border-bottom: 1px solid rgba(255,255,255,0.08); background: #19212d; color: #8491a3; font-size: 9px; }
.safety-code figcaption i { color: #ff8a80; font-style: normal; }
.safety-code > strong { display: block; padding: 24px 26px 0; color: #f2f5f8; font-size: 12px; }
.safety-code pre { margin: 0; padding: 18px 26px 28px; color: #aeb9c8; font-size: 12px; line-height: 1.75; }
.safety-code pre span { color: #ff8a80; }
.safety-code > p { display: flex; align-items: center; gap: 8px; margin: 0; padding: 13px 17px; border-top: 1px solid rgba(255,255,255,0.08); color: #8491a3; font-size: 9px; }
.safety-code > p i { width: 6px; height: 6px; border-radius: 50%; background: #73c49b; }

.install-section { background: var(--vp-c-bg-alt); }
.install-heading h2 { max-width: 12ch; }
.home-cta { padding: clamp(90px, 11vw, 132px) 0; background: var(--vp-c-bg-elv); }
.cta-inner { display: grid; grid-template-columns: 1.05fr 0.95fr; gap: 70px; align-items: end; }
.home-cta h2 { max-width: 11ch; }
.cta-side { justify-self: end; max-width: 470px; }
.cta-side > p { margin: 0; color: var(--vp-c-text-2); font-size: 15px; line-height: 1.7; }

@media (max-width: 1100px) {
  .hero-grid { grid-template-columns: minmax(0, 0.8fr) minmax(500px, 1.2fr); gap: 52px; }
  .hero-copy h1 { font-size: clamp(50px, 6vw, 68px); }
  .ecosystem-grid { grid-template-columns: 0.8fr 1.2fr; gap: 60px; }
}

@media (max-width: 900px) {
  .home-shell { width: min(100% - 40px, 760px); }
  .home-hero { padding-top: 78px; }
  .hero-grid,
  .ecosystem-grid,
  .safety-section { grid-template-columns: 1fr; }
  .hero-copy { max-width: 660px; }
  .hero-copy h1 { max-width: 11ch; font-size: clamp(54px, 9vw, 74px); }
  .hero-visual { margin-top: 18px; }
  .signal-rail { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .signal-rail > div:nth-child(2) { border-right: 0; }
  .signal-rail > div:nth-child(-n+2) { border-bottom: 1px solid var(--vp-c-divider); }
  .section-heading { grid-template-columns: 1fr; gap: 24px; }
  .surface-grid { grid-template-columns: 1fr; }
  .studio-card { grid-row: auto; min-height: 540px; }
  .ecosystem-copy { max-width: 600px; }
  .ecosystem-map { width: min(100%, 620px); margin: 0 auto; }
  .safety-code { width: min(100%, 640px); }
  .cta-inner { grid-template-columns: 1fr; gap: 34px; }
  .cta-side { justify-self: start; }
}

@media (max-width: 620px) {
  .home-shell { width: min(100% - 28px, 520px); }
  .home-hero { padding: 62px 0 86px; }
  .release-pill { margin-bottom: 25px; }
  .hero-copy h1 { margin-top: 14px; font-size: clamp(46px, 14.7vw, 66px); }
  .hero-lede { font-size: 16px; }
  .hero-actions { align-items: stretch; flex-direction: column; }
  .home-button { width: 100%; }
  .hero-proof { gap: 8px 14px; }
  .visual-label { font-size: 8px; }
  .hero-visual { padding-bottom: 126px; }
  .hero-visual :deep(.terminal-mockup) { min-width: 640px; transform: scale(0.78); transform-origin: top left; }
  .hero-visual :deep(.terminal-mockup) { margin-bottom: -104px; }
  .issue-receipt { right: -2px; width: 92%; }
  .signal-rail { grid-template-columns: 1fr; margin-top: -28px; }
  .signal-rail > div { border-right: 0; border-bottom: 1px solid var(--vp-c-divider); padding: 18px; }
  .signal-rail > div:last-child { border-bottom: 0; }
  .home-section { padding: 94px 0; }
  .section-heading { margin-bottom: 36px; }
  .section-heading h2,
  .ecosystem-copy h2,
  .safety-copy h2,
  .home-cta h2 { font-size: clamp(38px, 12vw, 52px); }
  .surface-card { border-radius: 18px; padding: 25px 22px; }
  .studio-card { min-height: 500px; }
  .surface-card > h3 { margin-top: 48px; }
  .ecosystem-map { min-height: 510px; }
  .ecosystem-core { top: 50%; }
  .ecosystem-node { width: 132px; padding: 13px; }
  .ecosystem-node.chalupa,
  .ecosystem-node.codemap { left: 0; }
  .ecosystem-node.fcheap,
  .ecosystem-node.vecgrep { right: 0; }
  .ecosystem-map > svg { display: none; }
  .safety-section { width: min(100% - 28px, 520px); gap: 48px; }
  .safety-code pre { font-size: 10px; }
  .cta-actions { flex-direction: column; }
}

@media (prefers-reduced-motion: reduce) {
  .release-pill,
  .home-button { transition: none; }
  .release-pill:hover,
  .home-button:hover { transform: none; }
}
</style>
