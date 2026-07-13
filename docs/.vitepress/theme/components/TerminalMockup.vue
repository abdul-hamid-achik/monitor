<script setup>
// Static showcase — no props needed
</script>

<template>
  <div class="terminal-mockup">
    <div class="tm-titlebar">
      <span class="tm-dot tm-red" />
      <span class="tm-dot tm-yellow" />
      <span class="tm-dot tm-green" />
      <span class="tm-title">monitor — studio</span>
      <span class="tm-badge">● real</span>
    </div>
    <div class="tm-body">
      <!-- CPU section -->
      <div class="tm-section-label">CPU</div>
      <div class="tm-metric">
        <span class="tm-m-label">Total</span>
        <span class="tm-m-bar"><span class="tm-bar-fill" style="width:82%" /></span>
        <span class="tm-m-value">82.3%</span>
      </div>
      <div class="tm-metric">
        <span class="tm-m-label">Core 0</span>
        <span class="tm-m-bar"><span class="tm-bar-fill" style="width:61%" /></span>
        <span class="tm-m-value">61.2%</span>
      </div>
      <div class="tm-metric">
        <span class="tm-m-label">Core 1</span>
        <span class="tm-m-bar"><span class="tm-bar-fill tm-warn" style="width:91%" /></span>
        <span class="tm-m-value">91.7%</span>
      </div>
      <div class="tm-metric">
        <span class="tm-m-label">Core 2</span>
        <span class="tm-m-bar"><span class="tm-bar-fill" style="width:43%" /></span>
        <span class="tm-m-value">43.1%</span>
      </div>
      <div class="tm-metric">
        <span class="tm-m-label">Core 3</span>
        <span class="tm-m-bar"><span class="tm-bar-fill" style="width:74%" /></span>
        <span class="tm-m-value">73.8%</span>
      </div>

      <div class="tm-divider" />

      <!-- Memory + Disk -->
      <div class="tm-metric">
        <span class="tm-m-label">Memory</span>
        <span class="tm-m-bar"><span class="tm-bar-fill" style="width:61%" /></span>
        <span class="tm-m-value">9.8 / 16 GB</span>
      </div>
      <div class="tm-metric">
        <span class="tm-m-label">Disk</span>
        <span class="tm-m-bar"><span class="tm-bar-fill" style="width:31%" /></span>
        <span class="tm-m-value">156 / 512 GB</span>
      </div>
      <div class="tm-metric">
        <span class="tm-m-label">Temp</span>
        <span class="tm-m-bar"><span class="tm-bar-fill" style="width:52%" /></span>
        <span class="tm-m-value">52°C</span>
      </div>

      <div class="tm-divider" />

      <!-- Process table -->
      <div class="tm-section-label">Top Processes</div>
      <div class="tm-table">
        <div class="tm-row tm-row-header">
          <span>PID</span>
          <span>CPU%</span>
          <span>RSS</span>
          <span>COMMAND</span>
        </div>
        <div class="tm-row">
          <span class="tm-pid">8421</span>
          <span class="tm-cpu">34.2%</span>
          <span class="tm-rss">2.1 GB</span>
          <span class="tm-cmd">node</span>
        </div>
        <div class="tm-row">
          <span class="tm-pid">9102</span>
          <span class="tm-cpu">12.7%</span>
          <span class="tm-rss">1.4 GB</span>
          <span class="tm-cmd">chrome</span>
        </div>
        <div class="tm-row">
          <span class="tm-pid">7788</span>
          <span class="tm-cpu">8.3%</span>
          <span class="tm-rss">890 MB</span>
          <span class="tm-cmd">go</span>
        </div>
        <div class="tm-row">
          <span class="tm-pid">6651</span>
          <span class="tm-cpu">3.1%</span>
          <span class="tm-rss">420 MB</span>
          <span class="tm-cmd">rust-analyzer</span>
        </div>
      </div>

      <!-- Anomaly alert -->
      <div class="tm-alert">
        <span class="tm-alert-dot" />
        <span class="tm-alert-text">⚠ CPU spike — chrome (9102) at 91.7% on Core 1 (baseline median: 22%)</span>
      </div>
    </div>
  </div>
</template>