<script setup lang="ts">
import { withBase } from 'vitepress'
import CommandCopy from './CommandCopy.vue'

const installCommand = 'brew install --cask abdul-hamid-achik/tap/monitor'

// Read-only tools answer freely; mutating ones refuse without confirm:true.
const tools = [
  ['monitor_issue', 'read'],
  ['monitor_issues', 'read'],
  ['monitor_analyze', 'read'],
  ['monitor_snapshot', 'read'],
  ['monitor_processes', 'read'],
  ['monitor_doctor', 'read'],
  ['monitor_profile_capture', 'confirm'],
  ['monitor_investigate', 'confirm'],
  ['monitor_record', 'confirm'],
  ['monitor_kill', 'confirm'],
]

// The Studio replica draws the same data shapes the TUI does: KPI gauges
// coloured by threshold, 60 one-second samples per activity chart, and a
// PID/NAME/CPU process list.
const kpis = [
  { label: 'CPU', value: '32.3%', note: '10 cores', fill: 32, tone: 'good' },
  { label: 'Memory', value: '78.9%', note: '12.6 GB / 16.0 GB', fill: 79, tone: 'warn' },
  { label: 'Thermal', value: '52.0 C', note: 'real sensor', fill: 52, tone: 'good' },
  { label: 'Disk', value: '73.0%', note: '/ · 365.0 GB used', fill: 73, tone: 'warn' },
]
const cpuSeries = [
  24, 31, 30, 32, 29, 28, 30, 35, 41, 38, 33, 30, 31, 29, 34, 45, 52, 47, 36, 31,
  30, 29, 31, 35, 37, 33, 30, 28, 31, 30, 34, 38, 36, 31, 30, 29, 33, 36, 34, 31,
  30, 58, 66, 44, 34, 31, 30, 33, 36, 34, 31, 30, 29, 31, 33, 35, 40, 37, 34, 32,
]
const memSeries = cpuSeries.map((_, i) => 77 + ((i * 7) % 5) * 0.4 + (i > 40 ? 1.2 : 0))
// Mirrors docs/guide/runtimes.md: every runtime gets crash parsing; hot
// lines depend on what the runtime's profiler exposes.
const runtimes = [
  { name: 'node', hot: 'live', ok: true },
  { name: 'deno', hot: 'live', ok: true },
  { name: 'bun', hot: 'at exit', ok: true },
  { name: 'go', hot: 'pprof', ok: true },
  { name: 'python', hot: 'errors only', ok: false },
  { name: 'ruby', hot: 'errors only', ok: false },
]
const topCPU = [
  ['8421', 'node', '82.3%'],
  ['9102', 'chrome', '12.7%'],
  ['7788', 'go', '8.3%'],
  ['1324', 'WebKit.GPU', '5.4%'],
  ['4410', 'ghostty', '2.9%'],
  ['3155', 'postgres', '1.6%'],
  ['662', 'Finder', '0.4%'],
]
</script>

<template>
  <div class="home">
    <!-- ── Hero ───────────────────────────────────────────────────────── -->
    <section class="shell hero">
      <div class="hero-copy">
        <p class="eyebrow">
          <span class="live-dot">●</span> v2.0.0 · local-first · macOS + Linux
        </p>
        <h1>Crashes that point<br />at the line.</h1>
        <p class="lede">
          Wrap any command in <code>monitor run</code>. Crashes from Node, Deno,
          Bun, Python, Ruby and Go become grouped issues with a culprit
          <code>file:line</code>, the source around it and the commit that last
          touched it. No SDK. No account. Nothing leaves your machine.
        </p>
        <div class="actions">
          <a class="btn primary" :href="withBase('/guide/first-issue')">Your first issue →</a>
          <a class="btn" href="https://github.com/abdul-hamid-achik/monitor">GitHub</a>
        </div>
        <CommandCopy class="install" :command="installCommand" />
      </div>

      <aside class="tf runtimes" aria-label="Supported runtimes">
        <span class="tf-title">runtimes</span>
        <table>
          <thead><tr><th /><th>crashes</th><th>hot lines</th></tr></thead>
          <tbody>
            <tr v-for="rt in runtimes" :key="rt.name">
              <td>{{ rt.name }}</td>
              <td><span class="good">●</span></td>
              <td :class="rt.ok ? '' : 'dim'"><span :class="rt.ok ? 'good' : 'dim'">{{ rt.ok ? '●' : '○' }}</span> {{ rt.hot }}</td>
            </tr>
          </tbody>
        </table>
        <a :href="withBase('/guide/runtimes')">runtimes matrix →</a>
      </aside>
    </section>

    <!-- The hero visual is a terminal drawn with Studio's own chrome. -->
    <section class="shell">
      <div class="term" aria-label="Example terminal session">
        <div class="term-bar">
          <span><b class="accent">◆</b> <b>monitor</b> <i>~/shop</i></span>
          <span class="live-dot">● live</span>
        </div>
        <pre class="term-body"><span class="dim">$</span> monitor run -- node src/server.js
<span class="accent">monitor &gt;</span> <span class="dim">node src/server.js · pid 99880 · shop/node · scanning stderr</span>
<span class="crit">TypeError: Cannot read properties of undefined (reading 'amount')</span>
<span class="dim">    at applyDiscount (src/cart.js:3:31)
    at checkout (src/cart.js:7:17)
    at handle (src/server.js:4:10)</span>
<span class="accent">monitor &gt;</span> <b class="warn">NEW BA42</b> fatal TypeError … <b>src/cart.js:3</b> applyDiscount()

<span class="dim">$</span> monitor issue latest
<b>BA42</b>  TypeError: Cannot read properties of undefined (reading 'amount')  <span class="dim">new · fatal</span>
<span class="dim">shop / node · first seen just now · 1 event</span>

<b class="accent">CULPRIT</b>  src/cart.js:3 in applyDiscount()
<span class="dim">     2 |   const rule = cart.discounts[code];</span>
<span class="hl">&gt;    3 |   return cart.subtotal - rule.amount;</span>
<span class="dim">     4 | }</span>
<b class="accent">TOUCHED</b>  f2dc0d0 "feat: spring discount"  <span class="dim">just now · local git blame</span>
<b class="accent">NEXT</b>     monitor issue ba42 --md   <span class="dim">paste-ready fix context for your agent</span>

<span class="dim">$</span> <span class="cursor" aria-hidden="true"></span></pre>
        <div class="keys term-foot">
          <span><b>run</b>catch</span>
          <span><b>issue</b>explain</span>
          <span><b>hot</b>profile</span>
          <span><b>studio</b>watch</span>
          <span><b>mcp</b>hand off</span>
        </div>
      </div>
    </section>

    <!-- ── Three commands ────────────────────────────────────────────── -->
    <section class="shell block">
      <header class="head">
        <h2>Three commands, one loop.</h2>
        <p>Catch it, explain it, find the hot line. Every step also speaks <code>--json</code>.</p>
      </header>
      <div class="steps">
        <article class="tf">
          <span class="tf-title">1 run</span>
          <p>Launch through monitor. Output still streams to your terminal first; crashes are parsed, scrubbed and grouped.</p>
          <pre><span class="dim">$</span> monitor run -- python app.py
<span class="accent">monitor &gt;</span> <b class="warn">NEW 7C1E</b> KeyError app.py:42</pre>
        </article>
        <article class="tf">
          <span class="tf-title">2 issue</span>
          <p>One command explains an issue: snippet, chained causes, blast radius, last commit. <code>--md</code> hands it to an agent.</p>
          <pre><span class="dim">$</span> monitor issues
<span class="dim">ID    EVENTS  WHERE</span>
BA42  12      src/cart.js:3</pre>
        </article>
        <article class="tf">
          <span class="tf-title">3 hot</span>
          <p>Which line inside a function burns CPU. Node, Deno, Bun and Go profiles, resolved through source maps.</p>
          <pre><span class="dim">heavyStringify · hot.js:3-6 · 95.8% self</span>
   4 | 32.7% <span class="bar" style="--w: 8ch" />
<span class="hl">&gt;  5 | 66.1% <span class="bar" style="--w: 17ch" /></span></pre>
        </article>
      </div>
    </section>

    <!-- ── Studio ───────────────────────────────────────────────────── -->
    <section class="shell block">
      <header class="head">
        <h2>And when you want to watch it live.</h2>
        <p><code>monitor studio</code> is the same frames, the same colors, driven by the keyboard. What you see on this page is what you get in the terminal.</p>
      </header>

      <div class="studio" aria-label="monitor studio overview">
        <div class="term-bar">
          <span><b class="accent">◆</b> <b>monitor</b> <i>macbook-pro</i></span>
          <span class="live-dot">● LIVE · sampled 22:48:26</span>
        </div>
        <div class="studio-tabs">
          <span class="on">▸1 overview</span><span>2 cpu</span><span>3 memory</span><span>4 thermal</span><span>5 disk</span><span>6 network</span><span>7 processes</span><span>8 settings</span><span>9 trends</span>
        </div>
        <div class="studio-grid">
          <div v-for="kpi in kpis" :key="kpi.label" class="tf kpi">
            <span class="tf-title">{{ kpi.label }}</span>
            <b>{{ kpi.value }}</b>
            <span class="gauge" :class="kpi.tone"><i :style="{ width: kpi.fill + '%' }" /></span>
            <em>{{ kpi.note }}</em>
          </div>
          <div class="tf wide">
            <span class="tf-title">Activity · 60s</span>
            <div class="series"><span>CPU</span><b>32.3%</b></div>
            <div class="chart accent-bars" aria-hidden="true">
              <i v-for="(v, i) in cpuSeries" :key="i" :style="{ height: v + '%' }" />
            </div>
            <div class="series"><span>MEM</span><b>78.9%</b></div>
            <div class="chart good-bars" aria-hidden="true">
              <i v-for="(v, i) in memSeries" :key="i" :style="{ height: v + '%' }" />
            </div>
          </div>
          <div class="tf side">
            <span class="tf-title">Top CPU</span>
            <table class="procs">
              <thead><tr><th>PID</th><th>NAME</th><th>CPU</th></tr></thead>
              <tbody>
                <tr v-for="[pid, name, cpu] in topCPU" :key="pid"><td>{{ pid }}</td><td>{{ name }}</td><td>{{ cpu }}</td></tr>
              </tbody>
            </table>
          </div>
          <div class="tf full">
            <span class="tf-title">Attention</span>
            <pre><span class="warn">●</span> CPU SPIKE | node (pid 8421) at 82.3% vs 24.1% baseline</pre>
          </div>
        </div>
        <div class="keys term-foot">
          <span><b>tab</b>switch</span><span><b>p</b>pause</span><span><b>r</b>refresh</span><span><b>?</b>help</span><span><b>q</b>quit</span>
        </div>
      </div>
      <p class="more"><a :href="withBase('/guide/tui')">Studio guide →</a></p>
    </section>

    <!-- ── Agents ───────────────────────────────────────────────────── -->
    <section class="shell block">
      <header class="head">
        <h2>Built for agents, safe by default.</h2>
        <p>Ten MCP tools share one service with the CLI. Reads return bounded, scrubbed context; anything that acts refuses without <code>confirm: true</code>.</p>
      </header>
      <div class="agents">
        <div class="tf">
          <span class="tf-title">mcp.json</span>
          <pre>{
  "mcpServers": {
    "monitor": {
      "command": "<span class="accent">monitor</span>",
      "args": ["mcp", "serve"]
    }
  }
}</pre>
        </div>
        <div class="tf">
          <span class="tf-title">tools</span>
          <ul class="tools">
            <li v-for="[name, kind] in tools" :key="name">
              <code>{{ name }}</code>
              <span :class="kind === 'confirm' ? 'warn' : 'good'">{{ kind === 'confirm' ? 'confirm' : 'read-only' }}</span>
            </li>
          </ul>
        </div>
      </div>
      <p class="more"><a :href="withBase('/guide/mcp')">MCP guide →</a> <a :href="withBase('/guide/safety')">Safety model →</a></p>
    </section>

    <!-- ── Install ──────────────────────────────────────────────────── -->
    <section class="shell block last">
      <div class="tf cta">
        <span class="tf-title">install</span>
        <div>
          <h2>One binary. Your machine stays yours.</h2>
          <p>No daemon, no account, no cloud. Homebrew, a release archive, or <code>go build</code>.</p>
        </div>
        <div class="cta-side">
          <CommandCopy :command="installCommand" />
          <div class="actions">
            <a class="btn primary" :href="withBase('/guide/installation')">Install guide →</a>
            <a class="btn" :href="withBase('/guide/getting-started')">Getting started</a>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.home {
  --mono: var(--vp-font-family-mono);
  color: var(--vp-c-text-1);
}

.shell {
  width: min(1120px, calc(100% - 48px));
  margin: 0 auto;
}

code {
  white-space: nowrap;
  border: 1px solid var(--vp-c-divider);
  border-radius: 4px;
  padding: 1px 5px;
  background: var(--vp-c-bg-soft);
  font-family: var(--mono);
  font-size: 0.86em;
}

pre {
  margin: 0;
  overflow-x: auto;
  color: var(--vp-c-text-1);
  font-family: var(--mono);
  font-size: 13px;
  line-height: 1.65;
}

.accent { color: var(--vp-c-brand-1); }
.good { color: var(--m-good); }
.warn { color: var(--m-warn); }
.crit { color: var(--m-crit); }
.dim { color: var(--vp-c-text-3); }

/* The culprit line: Studio's selected-row treatment. */
.hl {
  display: inline-block;
  min-width: 100%;
  background: var(--vp-c-brand-1);
  color: var(--m-select-fg);
  font-weight: 600;
}

/* Profile bars: CSS blocks, since box-drawing █ leaves gaps in Geist Mono. */
.bar {
  display: inline-block;
  width: var(--w);
  height: 0.9em;
  vertical-align: -0.1em;
  background: currentColor;
}

pre > .bar { color: var(--vp-c-brand-1); }

/* Hero */
.hero {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 300px;
  gap: 48px;
  align-items: end;
  padding: clamp(56px, 8vw, 96px) 0 48px;
}

.runtimes {
  padding: 22px 18px 16px;
  font-family: var(--mono);
  font-size: 13px;
}

.runtimes table {
  width: 100%;
  border-collapse: collapse;
}

.runtimes th {
  padding: 0 0 8px;
  color: var(--vp-c-text-3);
  font-weight: 400;
  text-align: left;
}

.runtimes td {
  padding: 3px 0;
}

.runtimes td:first-child {
  color: var(--vp-c-text-1);
  font-weight: 600;
}

.runtimes a {
  display: inline-block;
  margin-top: 12px;
  color: var(--vp-c-brand-1);
  text-decoration: none;
}

.runtimes a:hover { text-decoration: underline; }

.eyebrow {
  margin: 0 0 22px;
  color: var(--vp-c-text-3);
  font-family: var(--mono);
  font-size: 13px;
}

h1 {
  margin: 0;
  font-family: var(--mono);
  font-size: clamp(40px, 6.4vw, 76px);
  font-weight: 650;
  letter-spacing: -0.045em;
  line-height: 1.02;
}

.lede {
  max-width: 62ch;
  margin: 26px 0 0;
  color: var(--vp-c-text-2);
  font-size: clamp(16px, 1.5vw, 18px);
  line-height: 1.65;
}

.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  margin-top: 30px;
}

.btn {
  display: inline-flex;
  align-items: center;
  min-height: 40px;
  border: 1px solid var(--m-frame);
  border-radius: 6px;
  padding: 0 16px;
  color: var(--vp-c-text-1);
  font-family: var(--mono);
  font-size: 14px;
  font-weight: 600;
  text-decoration: none;
  transition: border-color 140ms ease, background-color 140ms ease;
}

.btn:hover { border-color: var(--vp-c-text-3); }

.btn.primary {
  border-color: var(--vp-c-brand-1);
  background: var(--vp-c-brand-1);
  color: var(--m-select-fg);
}

.btn.primary:hover { background: var(--vp-c-brand-2); }

.install {
  max-width: 540px;
  margin-top: 16px;
}

/* Terminal chrome shared by the hero session and the Studio replica. */
.term,
.studio {
  border: 1px solid var(--m-frame);
  border-radius: 10px;
  background: var(--vp-c-bg);
  font-family: var(--mono);
}

.term-bar {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  padding: 12px 18px;
  font-size: 13px;
}

.term-bar i {
  margin-left: 8px;
  color: var(--vp-c-text-3);
  font-style: normal;
}

.term-body {
  padding: 16px 18px 18px;
  border-block: 1px solid var(--vp-c-divider);
}

.term-foot {
  padding: 10px 18px 12px;
}

/* Sections */
.block {
  padding-top: clamp(80px, 10vw, 128px);
}

.block.last {
  padding-bottom: clamp(80px, 10vw, 128px);
}

.head {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 24px 48px;
  align-items: end;
  margin-bottom: 40px;
}

h2 {
  margin: 0;
  border: 0;
  font-family: var(--mono);
  font-size: clamp(26px, 3.4vw, 38px);
  font-weight: 650;
  letter-spacing: -0.035em;
  line-height: 1.12;
}

.head p,
.cta p {
  margin: 0;
  color: var(--vp-c-text-2);
  font-size: 16px;
  line-height: 1.65;
}

.steps {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 20px;
}

.steps .tf {
  display: flex;
  flex-direction: column;
  gap: 18px;
  padding: 24px 20px 20px;
}

.steps p {
  margin: 0;
  color: var(--vp-c-text-2);
  font-size: 15px;
  line-height: 1.6;
}

.steps pre {
  margin-top: auto;
  border-top: 1px dashed var(--vp-c-divider);
  padding-top: 14px;
  font-size: 12.5px;
}

/* Studio replica */
.studio-tabs {
  display: flex;
  flex-wrap: wrap;
  gap: 0 4px;
  padding: 0 18px 10px;
  border-bottom: 1px solid var(--vp-c-divider);
  color: var(--vp-c-text-2);
  font-size: 13px;
}

.studio-tabs span { padding: 0 8px; }

.studio-tabs .on {
  background: var(--vp-c-brand-1);
  color: var(--m-select-fg);
  font-weight: 700;
}

.studio-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 24px 12px;
  padding: 26px 18px 18px;
}

.studio-grid .tf { padding: 16px 14px 12px; }
.kpi { display: flex; flex-direction: column; gap: 6px; font-size: 13px; }
.kpi b { font-size: 15px; }
.kpi em { color: var(--vp-c-text-2); font-style: normal; }

/* Studio's heavy-rule gauge: a coloured share on a frame-coloured track. */
.gauge {
  display: block;
  height: 3px;
  border-radius: 2px;
  background: var(--m-frame);
}

.gauge i {
  display: block;
  height: 100%;
  border-radius: inherit;
}

.gauge.good i { background: var(--m-good); }
.gauge.warn i { background: var(--m-warn); }
.gauge.crit i { background: var(--m-crit); }

.series {
  display: flex;
  justify-content: space-between;
  font-size: 13px;
}

.series span { color: var(--vp-c-text-2); }

/* Block-glyph bar charts, one bar per one-second sample. */
.chart {
  display: flex;
  align-items: flex-end;
  height: 64px;
  margin: 4px 0 10px;
}

.chart i {
  flex: 1;
  min-width: 0;
}

.accent-bars i { background: var(--vp-c-brand-1); }
.good-bars i { background: var(--m-good); }

.procs {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
  line-height: 1.55;
}

.procs th {
  padding: 0;
  color: var(--vp-c-text-2);
  font-weight: 400;
  text-align: left;
}

.procs td { padding: 0; }
.procs :is(th, td):first-child { width: 6ch; padding-right: 1.5ch; text-align: right; }
.procs :is(th, td):last-child { text-align: right; }

/* A block cursor at the end of the hero session. */
.cursor {
  display: inline-block;
  width: 0.6em;
  height: 1.1em;
  vertical-align: -0.2em;
  background: var(--vp-c-brand-1);
  animation: blink 1.1s steps(1) infinite;
}

@keyframes blink {
  50% { opacity: 0; }
}

@media (prefers-reduced-motion: reduce) {
  .cursor { animation: none; }
}
.studio-grid .wide { grid-column: span 3; }
.studio-grid .side { grid-column: span 1; }
.studio-grid .full { grid-column: 1 / -1; }
.studio-grid pre { font-size: 13px; }

.more {
  display: flex;
  gap: 24px;
  margin: 18px 0 0;
  font-family: var(--mono);
  font-size: 14px;
}

.more a {
  color: var(--vp-c-brand-1);
  text-decoration: none;
}

.more a:hover { text-decoration: underline; }

/* Agents */
.agents {
  display: grid;
  grid-template-columns: minmax(0, 0.9fr) minmax(0, 1.1fr);
  gap: 20px;
}

.agents .tf { padding: 24px 20px 18px; }

.tools {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 24px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.tools li {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  padding: 7px 0;
  border-bottom: 1px solid var(--vp-c-divider);
  font-family: var(--mono);
  font-size: 12.5px;
}

.tools code {
  border: 0;
  padding: 0;
  background: none;
  font-size: 12.5px;
}

/* Install CTA */
.cta {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 32px 48px;
  align-items: center;
  padding: 44px 36px 36px;
}

.cta h2 { margin-bottom: 14px; }
.cta .actions { margin-top: 14px; }

@media (max-width: 900px) {
  .hero { grid-template-columns: 1fr; }
  .runtimes { max-width: 420px; }
  .head,
  .agents,
  .cta { grid-template-columns: 1fr; }
  .steps { grid-template-columns: 1fr; }
  .studio-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .studio-grid .wide,
  .studio-grid .side { grid-column: 1 / -1; }
}

@media (max-width: 560px) {
  .shell { width: calc(100% - 32px); }
  pre,
  .studio-grid pre { font-size: 11.5px; }
  .term-bar { padding: 10px 14px; font-size: 12px; }
  .term-body { padding: 14px; }
  .tools { grid-template-columns: 1fr; }
  .cta { padding: 36px 20px 24px; }
  .btn { flex: 1 1 auto; justify-content: center; }
}
</style>
