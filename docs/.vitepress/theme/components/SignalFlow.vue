<script setup lang="ts">
import { computed, ref } from 'vue'

type FlowStep = {
  id: string
  index: string
  label: string
  title: string
  description: string
  command: string
  output: string[]
}

const steps: FlowStep[] = [
  {
    id: 'signal',
    index: '01',
    label: 'Signal',
    title: 'A process leaves its baseline.',
    description:
      'Monitor compares current CPU, memory, disk, and process state with bounded local history.',
    command: 'monitor watch --interval 1s',
    output: ['cpu 82.3%', 'median 24.1%', 'delta +58.2'],
  },
  {
    id: 'issue',
    index: '02',
    label: 'Issue',
    title: 'Repeated events become one issue.',
    description:
      'Related occurrences are grouped by a stable failure fingerprint so you investigate a durable thread, not alert noise.',
    command: 'monitor issues show ISS-5F8E3A1C9D42B760',
    output: ['events 7', 'state open', 'severity high'],
  },
  {
    id: 'diagnose',
    index: '03',
    label: 'Diagnose',
    title: 'The hot PID gets a bounded explanation.',
    description:
      'Process context, anomaly evidence, optional profiles, and code intelligence converge on the same diagnosis.',
    command: 'monitor investigate 8421 --no-save --json',
    output: ['process node', 'rule cpu_spike', 'confidence high'],
  },
  {
    id: 'evidence',
    index: '04',
    label: 'Evidence',
    title: 'The finding survives the ephemeral run.',
    description:
      'Capture a stable incident bundle locally or persist it through file.cheap for later review and automation.',
    command: 'monitor investigate 8421 --ttl 7d --json',
    output: ['bundle verified', 'tree sha256:9d…', 'retained local'],
  },
]

const activeId = ref(steps[0].id)
const activeStep = computed(
  () => steps.find((step) => step.id === activeId.value) ?? steps[0],
)

function selectStep(id: string) {
  activeId.value = id
  requestAnimationFrame(() => {
    document.getElementById(`flow-tab-${id}`)?.focus()
  })
}

function selectRelative(id: string, offset: number) {
  const currentIndex = steps.findIndex((step) => step.id === id)
  const nextIndex = (currentIndex + offset + steps.length) % steps.length
  selectStep(steps[nextIndex].id)
}
</script>

<template>
  <div class="signal-flow">
    <div class="flow-tabs" role="tablist" aria-label="Investigation stages">
      <button
        v-for="step in steps"
        :id="`flow-tab-${step.id}`"
        :key="step.id"
        type="button"
        role="tab"
        :aria-selected="activeId === step.id"
        :aria-controls="`flow-panel-${step.id}`"
        :tabindex="activeId === step.id ? 0 : -1"
        :class="{ active: activeId === step.id }"
        @click="activeId = step.id"
        @keydown.left.prevent="selectRelative(step.id, -1)"
        @keydown.right.prevent="selectRelative(step.id, 1)"
        @keydown.home.prevent="selectStep(steps[0].id)"
        @keydown.end.prevent="selectStep(steps[steps.length - 1].id)"
      >
        <span>{{ step.index }}</span>
        <strong>{{ step.label }}</strong>
      </button>
    </div>

    <div
      :id="`flow-panel-${activeStep.id}`"
      class="flow-panel"
      role="tabpanel"
      :aria-labelledby="`flow-tab-${activeStep.id}`"
    >
      <div class="flow-copy">
        <span>{{ activeStep.index }} / {{ activeStep.label }}</span>
        <h3>{{ activeStep.title }}</h3>
        <p>{{ activeStep.description }}</p>
      </div>

      <div class="flow-terminal" aria-live="polite">
        <div class="flow-terminal-bar">
          <span>investigation</span>
          <i>local</i>
        </div>
        <code><b aria-hidden="true">$</b> {{ activeStep.command }}</code>
        <dl>
          <div v-for="item in activeStep.output" :key="item">
            <dt>{{ item.split(' ')[0] }}</dt>
            <dd>{{ item.split(' ').slice(1).join(' ') }}</dd>
          </div>
        </dl>
      </div>
    </div>
  </div>
</template>

<style scoped>
.signal-flow {
  overflow: hidden;
  border: 1px solid var(--vp-c-divider);
  border-radius: 26px;
  background: var(--vp-c-bg-elv);
  box-shadow: var(--monitor-shadow-lg);
}

.flow-tabs {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  border-bottom: 1px solid var(--vp-c-divider);
  background: var(--vp-c-bg-soft);
}

.flow-tabs button {
  position: relative;
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 10px;
  border: 0;
  border-right: 1px solid var(--vp-c-divider);
  padding: 18px 20px;
  background: transparent;
  color: var(--vp-c-text-3);
  cursor: pointer;
  text-align: left;
  transition: color 160ms ease, background-color 160ms ease;
}

.flow-tabs button:last-child {
  border-right: 0;
}

.flow-tabs button::after {
  position: absolute;
  right: 20px;
  bottom: -1px;
  left: 20px;
  height: 2px;
  background: var(--vp-c-brand-1);
  content: '';
  opacity: 0;
  transform: scaleX(0.45);
  transition: opacity 160ms ease, transform 160ms ease;
}

.flow-tabs button:hover,
.flow-tabs button.active {
  background: color-mix(in srgb, var(--vp-c-bg) 76%, transparent);
  color: var(--vp-c-text-1);
}

.flow-tabs button.active::after {
  opacity: 1;
  transform: scaleX(1);
}

.flow-tabs button:focus-visible {
  z-index: 1;
  outline: 2px solid var(--vp-c-brand-1);
  outline-offset: -4px;
}

.flow-tabs span {
  color: var(--vp-c-brand-1);
  font-family: var(--vp-font-family-mono);
  font-size: 10px;
  font-weight: 650;
}

.flow-tabs strong {
  overflow: hidden;
  font-size: 13px;
  font-weight: 670;
  text-overflow: ellipsis;
}

.flow-panel {
  display: grid;
  grid-template-columns: minmax(0, 0.88fr) minmax(360px, 1.12fr);
  gap: clamp(32px, 5vw, 76px);
  align-items: center;
  min-height: 370px;
  padding: clamp(30px, 5vw, 64px);
}

.flow-copy > span {
  color: var(--vp-c-brand-1);
  font-family: var(--vp-font-family-mono);
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 0.12em;
  text-transform: uppercase;
}

.flow-copy h3 {
  max-width: 12ch;
  margin: 16px 0;
  color: var(--vp-c-text-1);
  font-size: clamp(28px, 4vw, 43px);
  font-weight: 690;
  letter-spacing: -0.048em;
  line-height: 1.03;
}

.flow-copy p {
  max-width: 48ch;
  margin: 0;
  color: var(--vp-c-text-2);
  font-size: 15px;
  line-height: 1.7;
}

.flow-terminal {
  overflow: hidden;
  border: 1px solid rgba(255, 255, 255, 0.11);
  border-radius: 16px;
  background: #131923;
  color: #d9e0ea;
  box-shadow: 0 30px 70px rgba(16, 20, 28, 0.22);
  font-family: var(--vp-font-family-mono);
}

.flow-terminal-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 15px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.08);
  background: #19212d;
  color: #8491a3;
  font-size: 10px;
}

.flow-terminal-bar i {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: #8fd0ae;
  font-style: normal;
}

.flow-terminal-bar i::before {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: #73c49b;
  content: '';
}

.flow-terminal > code {
  display: block;
  overflow-x: auto;
  padding: 22px 22px 18px;
  color: #eef2f7;
  font-size: 11px;
  white-space: nowrap;
}

.flow-terminal > code b {
  margin-right: 5px;
  color: #ff8a80;
  font-weight: 500;
}

.flow-terminal dl {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  margin: 0;
  padding: 0 22px 22px;
}

.flow-terminal dl div {
  min-width: 0;
  padding: 13px;
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-right: 0;
  background: rgba(255, 255, 255, 0.025);
}

.flow-terminal dl div:first-child {
  border-radius: 8px 0 0 8px;
}

.flow-terminal dl div:last-child {
  border-right: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 0 8px 8px 0;
}

.flow-terminal dt {
  color: #718096;
  font-size: 9px;
  text-transform: uppercase;
}

.flow-terminal dd {
  overflow: hidden;
  margin: 5px 0 0;
  color: #d9e0ea;
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

@media (max-width: 760px) {
  .flow-tabs button {
    justify-content: center;
    padding: 14px 8px;
  }

  .flow-tabs button::after {
    right: 8px;
    left: 8px;
  }

  .flow-tabs span {
    display: none;
  }

  .flow-tabs strong {
    font-size: 11px;
  }

  .flow-panel {
    grid-template-columns: 1fr;
    min-height: 0;
    padding: 30px 22px 24px;
  }

  .flow-copy h3 {
    max-width: 15ch;
  }

  .flow-terminal > code {
    font-size: 9px;
  }

  .flow-terminal dl {
    grid-template-columns: 1fr;
  }

  .flow-terminal dl div,
  .flow-terminal dl div:last-child {
    border-right: 1px solid rgba(255, 255, 255, 0.08);
    border-bottom: 0;
    border-radius: 0;
  }

  .flow-terminal dl div:first-child {
    border-radius: 8px 8px 0 0;
  }

  .flow-terminal dl div:last-child {
    border-bottom: 1px solid rgba(255, 255, 255, 0.08);
    border-radius: 0 0 8px 8px;
  }
}

@media (prefers-reduced-motion: reduce) {
  .flow-tabs button,
  .flow-tabs button::after {
    transition: none;
  }
}
</style>
