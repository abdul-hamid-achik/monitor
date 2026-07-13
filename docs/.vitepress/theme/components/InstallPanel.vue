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
        v-for="method in methods"
        :key="method.id"
        type="button"
        :class="{ active: activeMethod === method.id }"
        :aria-pressed="activeMethod === method.id"
        @click="activeMethod = method.id"
      >
        <span>{{ method.label }}</span>
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
.install-panel {
  container-name: install-panel;
  container-type: inline-size;
  overflow: hidden;
  border: 1px solid var(--vp-c-divider);
  border-radius: 22px;
  background: var(--vp-c-bg);
  box-shadow: 0 24px 70px rgba(20, 24, 32, 0.08);
}

.install-tabs {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 1px;
  padding: 8px;
  background: var(--vp-c-bg-soft);
  border-bottom: 1px solid var(--vp-c-divider);
}

.install-tabs button {
  display: flex;
  min-width: 0;
  flex-direction: column;
  align-items: flex-start;
  gap: 3px;
  border: 1px solid transparent;
  border-radius: 14px;
  padding: 13px 16px;
  background: transparent;
  color: var(--vp-c-text-2);
  cursor: pointer;
  text-align: left;
  transition: background-color 180ms ease, border-color 180ms ease,
    color 180ms ease, transform 180ms ease;
}

.install-tabs button:hover {
  color: var(--vp-c-text-1);
  background: color-mix(in srgb, var(--vp-c-bg) 68%, transparent);
}

.install-tabs button:active {
  transform: scale(0.98);
}

.install-tabs button:focus-visible {
  outline: 2px solid var(--vp-c-brand-1);
  outline-offset: 2px;
}

.install-tabs button.active {
  border-color: var(--vp-c-divider);
  background: var(--vp-c-bg);
  color: var(--vp-c-text-1);
  box-shadow: 0 8px 24px rgba(20, 24, 32, 0.06);
}

.install-tabs span {
  font-size: 14px;
  font-weight: 720;
}

.install-tabs small {
  color: var(--vp-c-text-3);
  font-size: 11px;
}

.install-tabs button.active small {
  color: var(--vp-c-brand-1);
}

.install-content {
  padding: clamp(24px, 4vw, 48px);
}

.method-panel {
  display: grid;
  grid-template-columns: minmax(0, 0.8fr) minmax(420px, 1.2fr);
  gap: 26px 48px;
  align-items: end;
}

.method-copy :is(h2, h3) {
  margin: 6px 0 10px;
  border: 0;
  padding: 0;
  color: var(--vp-c-text-1);
  font-size: clamp(22px, 3vw, 30px);
  letter-spacing: -0.035em;
  line-height: 1.12;
}

.method-copy p {
  max-width: 52ch;
  margin: 0;
  color: var(--vp-c-text-2);
  font-size: 14px;
  line-height: 1.65;
}

.method-kicker {
  color: var(--vp-c-brand-1);
  font-family: var(--vp-font-family-mono);
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.11em;
  text-transform: uppercase;
}

.install-steps {
  display: grid;
  grid-column: 1 / -1;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 0;
  margin: 8px 0 0;
  padding: 22px 0 0;
  border-top: 1px solid var(--vp-c-divider);
  list-style: none;
}

.install-steps li {
  display: flex;
  gap: 12px;
  align-items: flex-start;
  min-width: 0;
  padding: 0 20px;
  border-right: 1px solid var(--vp-c-divider);
}

.install-steps li:first-child {
  padding-left: 0;
}

.install-steps li:last-child {
  padding-right: 0;
  border-right: 0;
}

.install-steps li > span {
  color: var(--vp-c-brand-1);
  font-family: var(--vp-font-family-mono);
  font-size: 11px;
  font-weight: 700;
  line-height: 1.7;
}

.install-steps div {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 5px;
}

.install-steps strong {
  color: var(--vp-c-text-1);
  font-size: 13px;
}

.install-steps code,
.method-footnote code,
.method-copy code {
  color: var(--vp-c-text-2);
  font-family: var(--vp-font-family-mono);
  font-size: 11px;
  overflow-wrap: anywhere;
}

.release-link {
  grid-column: 2;
  width: fit-content;
  color: var(--vp-c-brand-1);
  font-size: 13px;
  font-weight: 700;
  text-decoration: none;
}

.release-link:hover {
  text-decoration: underline;
  text-underline-offset: 4px;
}

.release-link:focus-visible {
  border-radius: 4px;
  outline: 2px solid var(--vp-c-brand-1);
  outline-offset: 4px;
}

.method-footnote {
  grid-column: 2;
  margin: 0;
  color: var(--vp-c-text-3);
  font-size: 12px;
  line-height: 1.6;
}

@media (max-width: 820px) {
  .method-panel {
    grid-template-columns: 1fr;
  }

  .release-link,
  .method-footnote {
    grid-column: 1;
  }

  .install-steps {
    grid-template-columns: 1fr;
    gap: 14px;
  }

  .install-steps li,
  .install-steps li:first-child,
  .install-steps li:last-child {
    padding: 0 0 14px;
    border-right: 0;
    border-bottom: 1px solid var(--vp-c-divider);
  }

  .install-steps li:last-child {
    padding-bottom: 0;
    border-bottom: 0;
  }
}

@container install-panel (max-width: 780px) {
  .method-panel {
    grid-template-columns: 1fr;
  }

  .release-link,
  .method-footnote {
    grid-column: 1;
  }

  .install-steps {
    grid-template-columns: 1fr;
    gap: 14px;
  }

  .install-steps li,
  .install-steps li:first-child,
  .install-steps li:last-child {
    padding: 0 0 14px;
    border-right: 0;
    border-bottom: 1px solid var(--vp-c-divider);
  }

  .install-steps li:last-child {
    padding-bottom: 0;
    border-bottom: 0;
  }
}

@media (max-width: 620px) {
  .install-panel {
    border-radius: 16px;
  }

  .install-tabs {
    grid-template-columns: 1fr;
  }

  .install-tabs button {
    flex-direction: row;
    align-items: baseline;
    justify-content: space-between;
    padding: 10px 12px;
  }

  .install-content {
    padding: 22px 16px 24px;
  }
}

@container install-panel (max-width: 520px) {
  .install-panel {
    border-radius: 16px;
  }

  .install-tabs {
    grid-template-columns: 1fr;
  }

  .install-tabs button {
    flex-direction: row;
    align-items: baseline;
    justify-content: space-between;
    padding: 10px 12px;
  }

  .install-content {
    padding: 22px 16px 24px;
  }
}

@media (prefers-reduced-motion: reduce) {
  .install-tabs button {
    transition: none;
  }
}
</style>
