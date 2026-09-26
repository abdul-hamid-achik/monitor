<script setup lang="ts">
import { ref } from 'vue'
import CommandCopy from './CommandCopy.vue'

type InstallMethod = 'homebrew' | 'release' | 'source'

withDefaults(
  defineProps<{
    headingLevel?: 'h2' | 'h3'
  }>(),
  {
    headingLevel: 'h3',
  },
)

const activeMethod = ref<InstallMethod>('homebrew')

const methods: Array<{
  id: InstallMethod
  label: string
  note: string
}> = [
  { id: 'homebrew', label: 'Homebrew', note: 'Recommended' },
  { id: 'release', label: 'Release archive', note: 'No package manager' },
  { id: 'source', label: 'Build from source', note: 'Go 1.25+' },
]

const brewCommand = 'brew install --cask abdul-hamid-achik/tap/monitor'
const archiveCommand =
  'tar -xzf monitor_VERSION_SYSTEM_ARCH.tar.gz\nmkdir -p "$HOME/.local/bin"\ninstall -m 0755 monitor "$HOME/.local/bin/monitor"'
const sourceCommand =
  'git clone https://github.com/abdul-hamid-achik/monitor.git\ncd monitor\nmkdir -p bin\ngo build -o bin/monitor ./cmd/monitor'
</script>

<template>
  <div class="install-panel">
    <div class="install-tabs" role="group" aria-label="Installation methods">
      <button
        v-for="(method, index) in methods"
        :key="method.id"
        type="button"
        :class="{ active: activeMethod === method.id }"
        :aria-pressed="activeMethod === method.id"
        @click="activeMethod = method.id"
      >
        <span>{{ activeMethod === method.id ? '▸' : ' ' }}{{ index + 1 }} {{ method.label.toLowerCase() }}</span>
        <small>{{ method.note }}</small>
      </button>
    </div>

    <div class="install-content">
      <div v-if="activeMethod === 'homebrew'" class="method-panel">
        <div class="method-copy">
          <div class="method-kicker">Fastest path</div>
          <component :is="headingLevel">
            Install a verified release with Homebrew
          </component>
          <p>
            The tap selects the right binary for macOS or Linux and for Apple
            Silicon, Intel, or ARM64 automatically.
          </p>
        </div>

        <CommandCopy :command="brewCommand" />

        <ol class="install-steps">
          <li>
            <span>01</span>
            <div>
              <strong>Verify the binary</strong>
              <code>monitor --version</code>
            </div>
          </li>
          <li>
            <span>02</span>
            <div>
              <strong>Open Studio</strong>
              <code>monitor studio</code>
            </div>
          </li>
          <li>
            <span>03</span>
            <div>
              <strong>Check integrations</strong>
              <code>monitor doctor</code>
            </div>
          </li>
        </ol>
      </div>

      <div v-else-if="activeMethod === 'release'" class="method-panel">
        <div class="method-copy">
          <div class="method-kicker">Direct download</div>
          <component :is="headingLevel">Use a prebuilt release archive</component>
          <p>
            Download the archive matching your operating system and CPU from
            GitHub Releases, then place the binary somewhere on your
            <code>PATH</code>.
          </p>
        </div>

        <CommandCopy :command="archiveCommand" prompt="" />

        <a
          class="release-link"
          href="https://github.com/abdul-hamid-achik/monitor/releases/latest"
        >
          Browse the latest release <span aria-hidden="true">→</span>
        </a>
        <p class="method-footnote">
          Replace <code>VERSION_SYSTEM_ARCH</code> with the downloaded archive's
          values, and make sure <code>~/.local/bin</code> is on your
          <code>PATH</code>.
        </p>
      </div>

      <div v-else class="method-panel">
        <div class="method-copy">
          <div class="method-kicker">Developer install</div>
          <component :is="headingLevel">Build the current source</component>
          <p>
            Use this path when contributing or testing unreleased changes. It
            requires Go 1.25 or newer.
          </p>
        </div>

        <CommandCopy :command="sourceCommand" prompt="" />

        <p class="method-footnote">
          The binary is written to <code>bin/monitor</code>. Run it there or
          install it onto your <code>PATH</code> with <code>task install</code>.
        </p>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* A Studio frame: numbered tabs across the top (the active one filled with
   the accent, like `monitor studio`'s tab row), content underneath. */
.install-panel {
  overflow: hidden;
  border: 1px solid var(--m-frame);
  border-radius: 8px;
  background: var(--vp-c-bg);
}

.install-tabs {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 6px;
  padding: 12px 14px;
  border-bottom: 1px solid var(--vp-c-divider);
}

.install-tabs button {
  display: inline-flex;
  align-items: baseline;
  gap: 10px;
  border: 0;
  padding: 3px 10px;
  background: transparent;
  color: var(--vp-c-text-2);
  cursor: pointer;
  font-family: var(--vp-font-family-mono);
  font-size: 13.5px;
  white-space: pre;
}

.install-tabs button:hover {
  color: var(--vp-c-text-1);
}

.install-tabs button.active {
  background: var(--vp-c-brand-1);
  color: var(--m-select-fg);
  font-weight: 700;
}

.install-tabs small {
  color: var(--vp-c-text-3);
  font-size: 11.5px;
  font-weight: 400;
}

.install-tabs button.active small {
  color: inherit;
  opacity: 0.75;
}

.install-content {
  padding: clamp(20px, 3.5vw, 32px);
}

.method-panel {
  display: flex;
  flex-direction: column;
  gap: 18px;
}

.method-copy :is(h2, h3) {
  margin: 4px 0 8px;
  border: 0;
  padding: 0;
  color: var(--vp-c-text-1);
  font-family: var(--vp-font-family-mono);
  font-size: clamp(19px, 2.4vw, 23px);
  font-weight: 600;
  letter-spacing: -0.03em;
  line-height: 1.2;
}

.method-copy p {
  max-width: 62ch;
  margin: 0;
  color: var(--vp-c-text-2);
  font-size: 15px;
  line-height: 1.6;
}

.method-kicker {
  color: var(--vp-c-text-3);
  font-family: var(--vp-font-family-mono);
  font-size: 12.5px;
  text-transform: lowercase;
}

.install-steps {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 28px;
  margin: 4px 0 0;
  padding: 16px 0 0;
  border-top: 1px dashed var(--vp-c-divider);
  list-style: none;
}

.install-steps li {
  display: flex;
  gap: 10px;
  align-items: baseline;
  margin: 0;
}

.install-steps li > span {
  color: var(--vp-c-brand-1);
  font-family: var(--vp-font-family-mono);
  font-size: 12.5px;
  font-weight: 700;
}

.install-steps div {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.install-steps strong {
  color: var(--vp-c-text-1);
  font-size: 14px;
  font-weight: 600;
}

.install-steps code,
.method-footnote code,
.method-copy code {
  border: 0;
  padding: 0;
  background: none;
  color: var(--vp-c-text-2);
  font-family: var(--vp-font-family-mono);
  font-size: 12.5px;
  overflow-wrap: anywhere;
}

.release-link {
  width: fit-content;
  color: var(--vp-c-brand-1);
  font-family: var(--vp-font-family-mono);
  font-size: 14px;
  font-weight: 600;
  text-decoration: none;
}

.release-link:hover {
  text-decoration: underline;
  text-underline-offset: 4px;
}

.method-footnote {
  margin: 0;
  color: var(--vp-c-text-3);
  font-size: 13px;
  line-height: 1.6;
}
</style>
