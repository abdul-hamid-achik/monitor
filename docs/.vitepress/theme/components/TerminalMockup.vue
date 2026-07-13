<template>
  <div
    class="terminal-mockup"
    role="img"
    aria-label="Static preview of Monitor Studio showing system metrics, top processes, and an anomaly alert"
  >
    <div class="tm-titlebar">
      <div class="tm-window-controls" aria-hidden="true">
        <span />
        <span />
        <span />
      </div>
      <span class="tm-title">monitor studio</span>
      <span class="tm-source"><i aria-hidden="true" /> local</span>
    </div>

    <div class="tm-tabs">
      <span class="active">Overview</span>
      <span>CPU</span>
      <span>Memory</span>
      <span>Processes</span>
      <span>Trends</span>
    </div>

    <div class="tm-body">
      <div class="tm-summary">
        <div>
          <span>CPU</span>
          <strong>82.3<small>%</small></strong>
          <em class="hot">high</em>
        </div>
        <div>
          <span>Memory</span>
          <strong>9.8<small>GB</small></strong>
          <em>of 16 GB</em>
        </div>
        <div>
          <span>Temperature</span>
          <strong>52<small>°C</small></strong>
          <em>real sensor</em>
        </div>
        <div>
          <span>Disk</span>
          <strong>31<small>%</small></strong>
          <em>156 GB used</em>
        </div>
      </div>

      <div class="tm-workspace">
        <section class="tm-chart">
          <div class="tm-section-heading">
            <span>CPU by core</span>
            <small>rolling 60s</small>
          </div>

          <div class="tm-core-row">
            <span>P0</span>
            <i><b style="--bar: 91%" /></i>
            <strong>91.7%</strong>
          </div>
          <div class="tm-core-row">
            <span>P1</span>
            <i><b style="--bar: 74%" /></i>
            <strong>73.8%</strong>
          </div>
          <div class="tm-core-row">
            <span>E0</span>
            <i><b style="--bar: 61%" /></i>
            <strong>61.2%</strong>
          </div>
          <div class="tm-core-row">
            <span>E1</span>
            <i><b style="--bar: 43%" /></i>
            <strong>43.1%</strong>
          </div>

          <div class="tm-sparkline" aria-hidden="true">
            <svg viewBox="0 0 320 74" preserveAspectRatio="none">
              <path
                class="tm-grid-line"
                d="M0 18.5H320 M0 37H320 M0 55.5H320"
              />
              <path
                class="tm-area"
                d="M0 58 L24 54 L48 57 L72 45 L96 49 L120 38 L144 43 L168 25 L192 31 L216 18 L240 24 L264 11 L288 20 L320 14 L320 74 L0 74 Z"
              />
              <path
                class="tm-line"
                d="M0 58 L24 54 L48 57 L72 45 L96 49 L120 38 L144 43 L168 25 L192 31 L216 18 L240 24 L264 11 L288 20 L320 14"
              />
            </svg>
          </div>
        </section>

        <section class="tm-processes">
          <div class="tm-section-heading">
            <span>Top processes</span>
            <small>CPU</small>
          </div>
          <div class="tm-process tm-process-head">
            <span>PID</span>
            <span>COMMAND</span>
            <span>CPU</span>
            <span>RSS</span>
          </div>
          <div class="tm-process selected">
            <span>8421</span>
            <span>node</span>
            <span>82.3%</span>
            <span>2.1G</span>
          </div>
          <div class="tm-process">
            <span>9102</span>
            <span>chrome</span>
            <span>12.7%</span>
            <span>1.4G</span>
          </div>
          <div class="tm-process">
            <span>7788</span>
            <span>go</span>
            <span>8.3%</span>
            <span>890M</span>
          </div>
          <div class="tm-process">
            <span>6651</span>
            <span>rust-analyzer</span>
            <span>3.1%</span>
            <span>420M</span>
          </div>
        </section>
      </div>

      <div class="tm-alert">
        <span class="tm-alert-label">Anomaly</span>
        <strong>CPU spike</strong>
        <span>node (8421) at 82.3% · baseline median 24.1%</span>
      </div>
    </div>

    <div class="tm-statusbar">
      <span>1 overview</span>
      <span>9 tabs</span>
      <span class="tm-status-right">collecting · 1s</span>
    </div>
  </div>
</template>

<style scoped>
.terminal-mockup {
  overflow: hidden;
  border: 1px solid rgba(255, 255, 255, 0.12);
  border-radius: 16px;
  background: #111720;
  color: #d8dee9;
  box-shadow: 0 32px 80px rgba(14, 18, 24, 0.34),
    inset 0 1px 0 rgba(255, 255, 255, 0.055);
  font-family: var(--vp-font-family-mono);
  font-size: 11px;
  line-height: 1.4;
}

.tm-titlebar,
.tm-statusbar {
  display: flex;
  align-items: center;
}

.tm-titlebar {
  min-height: 42px;
  padding: 0 15px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.065);
  background: #171e29;
}

.tm-window-controls {
  display: flex;
  gap: 6px;
  margin-right: 13px;
}

.tm-window-controls span {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: #5f6978;
}

.tm-window-controls span:first-child {
  background: #ff756d;
}

.tm-window-controls span:nth-child(2) {
  background: #d6a94f;
}

.tm-window-controls span:last-child {
  background: #62b891;
}

.tm-title {
  color: #aab4c3;
  letter-spacing: 0.02em;
}

.tm-source {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  margin-left: auto;
  color: #8290a4;
}

.tm-source i {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: #62b891;
  box-shadow: 0 0 0 3px rgba(98, 184, 145, 0.11);
}

.tm-tabs {
  display: flex;
  gap: 18px;
  overflow: hidden;
  padding: 10px 16px 9px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.065);
  color: #667286;
  white-space: nowrap;
}

.tm-tabs span {
  position: relative;
}

.tm-tabs .active {
  color: #f8fafc;
}

.tm-tabs .active::after {
  position: absolute;
  right: 0;
  bottom: -10px;
  left: 0;
  height: 2px;
  background: #ff756d;
  content: '';
}

.tm-body {
  padding: 16px;
}

.tm-summary {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  border: 1px solid rgba(255, 255, 255, 0.075);
  border-radius: 9px;
  background: #151c26;
}

.tm-summary > div {
  display: flex;
  min-width: 0;
  flex-direction: column;
  padding: 11px 12px;
  border-right: 1px solid rgba(255, 255, 255, 0.075);
}

.tm-summary > div:last-child {
  border-right: 0;
}

.tm-summary span {
  overflow: hidden;
  color: #738096;
  font-size: 8px;
  letter-spacing: 0.08em;
  text-overflow: ellipsis;
  text-transform: uppercase;
  white-space: nowrap;
}

.tm-summary strong {
  margin: 3px 0 1px;
  color: #f3f5f7;
  font-size: 18px;
  letter-spacing: -0.04em;
}

.tm-summary strong small {
  margin-left: 2px;
  color: #8f9bad;
  font-size: 9px;
  font-weight: 500;
}

.tm-summary em {
  overflow: hidden;
  color: #718096;
  font-size: 8px;
  font-style: normal;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tm-summary em.hot {
  color: #ff8a80;
}

.tm-workspace {
  display: grid;
  grid-template-columns: 1.04fr 0.96fr;
  gap: 10px;
  margin-top: 10px;
}

.tm-chart,
.tm-processes {
  min-width: 0;
  border: 1px solid rgba(255, 255, 255, 0.075);
  border-radius: 9px;
  background: #151c26;
}

.tm-chart {
  padding: 12px;
}

.tm-processes {
  overflow: hidden;
  padding: 12px 0;
}

.tm-section-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 11px;
  color: #dce2ea;
  font-size: 9px;
  font-weight: 700;
  letter-spacing: 0.04em;
  text-transform: uppercase;
}

.tm-processes .tm-section-heading {
  padding: 0 11px;
}

.tm-section-heading small {
  color: #67758a;
  font-size: 7px;
  font-weight: 500;
}

.tm-core-row {
  display: grid;
  grid-template-columns: 20px minmax(0, 1fr) 38px;
  gap: 7px;
  align-items: center;
  margin: 5px 0;
  color: #7d899b;
  font-size: 8px;
}

.tm-core-row i {
  height: 4px;
  overflow: hidden;
  border-radius: 999px;
  background: rgba(255, 255, 255, 0.06);
}

.tm-core-row b {
  display: block;
  width: var(--bar);
  height: 100%;
  border-radius: inherit;
  background: #70b99a;
}

.tm-core-row:first-of-type b {
  background: #ff756d;
}

.tm-core-row strong {
  color: #b9c3d0;
  font-size: 8px;
  font-weight: 500;
  text-align: right;
}

.tm-sparkline {
  height: 58px;
  margin-top: 10px;
}

.tm-sparkline svg {
  width: 100%;
  height: 100%;
  overflow: visible;
}

.tm-grid-line {
  fill: none;
  stroke: rgba(255, 255, 255, 0.045);
  stroke-width: 1;
}

.tm-area {
  fill: rgba(255, 117, 109, 0.07);
}

.tm-line {
  fill: none;
  stroke: #ff756d;
  stroke-linecap: round;
  stroke-linejoin: round;
  stroke-width: 1.5;
}

.tm-process {
  display: grid;
  grid-template-columns: 38px minmax(72px, 1fr) 38px 34px;
  gap: 6px;
  padding: 6px 11px;
  color: #8895a7;
  font-size: 8px;
}

.tm-process span:nth-child(2) {
  overflow: hidden;
  color: #c3ccd8;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tm-process span:nth-child(n + 3) {
  text-align: right;
}

.tm-process-head {
  border-top: 1px solid rgba(255, 255, 255, 0.055);
  border-bottom: 1px solid rgba(255, 255, 255, 0.055);
  color: #59667a;
  font-size: 7px;
  letter-spacing: 0.04em;
}

.tm-process-head span:nth-child(2) {
  color: #59667a;
}

.tm-process.selected {
  background: rgba(255, 117, 109, 0.08);
  color: #ff8a80;
}

.tm-process.selected span:nth-child(2) {
  color: #f2f5f8;
}

.tm-alert {
  display: flex;
  align-items: center;
  gap: 9px;
  margin-top: 10px;
  padding: 9px 11px;
  border: 1px solid rgba(255, 117, 109, 0.17);
  border-radius: 8px;
  background: rgba(255, 117, 109, 0.055);
  color: #8d9aad;
  font-size: 8px;
}

.tm-alert-label {
  border-radius: 3px;
  padding: 2px 5px;
  background: rgba(255, 117, 109, 0.14);
  color: #ff9d96;
  font-size: 7px;
  font-weight: 700;
  letter-spacing: 0.06em;
  text-transform: uppercase;
}

.tm-alert strong {
  color: #e8edf3;
  font-size: 8px;
}

.tm-alert > span:last-child {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tm-statusbar {
  gap: 14px;
  min-height: 28px;
  padding: 0 15px;
  border-top: 1px solid rgba(255, 255, 255, 0.065);
  background: #171e29;
  color: #637086;
  font-size: 8px;
}

.tm-status-right {
  margin-left: auto;
  color: #72a990;
}

@media (max-width: 620px) {
  .terminal-mockup {
    font-size: 9px;
  }

  .tm-body {
    padding: 10px;
  }

  .tm-tabs {
    gap: 12px;
    padding-right: 12px;
    padding-left: 12px;
  }

  .tm-summary > div {
    padding: 8px;
  }

  .tm-summary strong {
    font-size: 14px;
  }

  .tm-workspace {
    grid-template-columns: 1fr;
  }

  .tm-chart {
    display: none;
  }

  .tm-alert > span:last-child {
    display: none;
  }
}
</style>
