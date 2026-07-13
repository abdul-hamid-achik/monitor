<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'

const props = withDefaults(
  defineProps<{
    command: string
    prompt?: string
  }>(),
  {
    prompt: '$',
  },
)

type CopyState = 'idle' | 'copying' | 'copied' | 'error'

const copyState = ref<CopyState>('idle')
let resetTimer: ReturnType<typeof setTimeout> | undefined

const buttonLabel = computed(() => {
  switch (copyState.value) {
    case 'copying':
      return 'Copying'
    case 'copied':
      return 'Copied'
    case 'error':
      return 'Select and copy'
    default:
      return 'Copy'
  }
})

async function copyCommand() {
  copyState.value = 'copying'

  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(props.command)
    } else {
      const textarea = document.createElement('textarea')
      textarea.value = props.command
      textarea.style.position = 'fixed'
      textarea.style.opacity = '0'
      document.body.appendChild(textarea)
      textarea.select()

      try {
        if (!document.execCommand('copy')) {
          throw new Error('Copy command was rejected')
        }
      } finally {
        textarea.remove()
      }
    }

    copyState.value = 'copied'
  } catch {
    copyState.value = 'error'
  }

  if (resetTimer) clearTimeout(resetTimer)
  resetTimer = setTimeout(() => {
    copyState.value = 'idle'
  }, 2200)
}

onBeforeUnmount(() => {
  if (resetTimer) clearTimeout(resetTimer)
})
</script>

<template>
  <div class="command-copy" :class="`is-${copyState}`">
    <div class="command-text">
      <span class="command-prompt" aria-hidden="true">{{ prompt }}</span>
      <code>{{ command }}</code>
    </div>
    <button
      type="button"
      :disabled="copyState === 'copying'"
      :aria-label="`${buttonLabel}: ${command}`"
      @click="copyCommand"
    >
      <span aria-live="polite">{{ buttonLabel }}</span>
    </button>
  </div>
</template>

<style scoped>
.command-copy {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: stretch;
  overflow: hidden;
  border: 1px solid var(--monitor-command-border, rgba(255, 255, 255, 0.12));
  border-radius: 12px;
  background: var(--monitor-command-bg, #11151c);
  color: var(--monitor-command-text, #f8fafc);
  box-shadow: inset 0 1px 0 rgba(255, 255, 255, 0.04);
  transition: border-color 180ms ease, transform 180ms ease;
}

.command-copy:focus-within {
  border-color: var(--vp-c-brand-1);
}

.command-text {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 12px;
  padding: 14px 16px;
  overflow-x: auto;
}

.command-prompt {
  flex: 0 0 auto;
  color: var(--monitor-command-accent, #ff8a80);
  font-family: var(--vp-font-family-mono);
  font-weight: 700;
}

code {
  color: inherit;
  font-family: var(--vp-font-family-mono);
  font-size: 12px;
  line-height: 1.5;
  white-space: pre;
}

button {
  min-width: 84px;
  border: 0;
  border-left: 1px solid var(--monitor-command-border, rgba(255, 255, 255, 0.12));
  background: rgba(255, 255, 255, 0.045);
  color: #d8dee9;
  cursor: pointer;
  font-family: var(--vp-font-family-base);
  font-size: 12px;
  font-weight: 700;
  letter-spacing: 0.02em;
  transition: background-color 160ms ease, color 160ms ease, transform 160ms ease;
}

button:hover {
  background: rgba(255, 255, 255, 0.09);
  color: #ffffff;
}

button:active {
  transform: scale(0.98);
}

button:focus-visible {
  outline: 2px solid #ffffff;
  outline-offset: -4px;
}

button:disabled {
  cursor: wait;
  opacity: 0.72;
}

.is-copied {
  border-color: rgba(74, 222, 128, 0.55);
}

.is-copied button {
  color: #86efac;
}

.is-error {
  border-color: rgba(251, 191, 36, 0.65);
}

.is-error button {
  color: #fde68a;
}

@media (max-width: 520px) {
  .command-text {
    align-items: flex-start;
    overflow-x: visible;
    padding: 12px 13px;
  }

  code {
    overflow-wrap: anywhere;
    font-size: 10px;
    white-space: pre-wrap;
  }

  button {
    min-width: 72px;
  }
}

@media (prefers-reduced-motion: reduce) {
  .command-copy,
  button {
    transition: none;
  }
}
</style>
