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
/* A one-line terminal: Studio frame border, accent prompt, keycap-style copy. */
.command-copy {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: stretch;
  overflow: hidden;
  border: 1px solid var(--m-frame);
  border-radius: 6px;
  background: var(--vp-c-bg);
  color: var(--vp-c-text-1);
  transition: border-color 140ms ease;
}

.command-copy:focus-within,
.command-copy:hover {
  border-color: var(--vp-c-text-3);
}

.command-text {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 10px;
  padding: 11px 14px;
  overflow-x: auto;
}

.command-prompt {
  flex: 0 0 auto;
  color: var(--vp-c-brand-1);
  font-family: var(--vp-font-family-mono);
  font-weight: 700;
}

code {
  border: 0;
  padding: 0;
  background: none;
  color: inherit;
  font-family: var(--vp-font-family-mono);
  font-size: 13px;
  line-height: 1.5;
  white-space: pre;
}

button {
  min-width: 72px;
  border: 0;
  border-left: 1px solid var(--m-frame);
  background: transparent;
  color: var(--vp-c-text-2);
  cursor: pointer;
  font-family: var(--vp-font-family-mono);
  font-size: 12.5px;
  font-weight: 600;
  transition: color 140ms ease, background-color 140ms ease;
}

button:hover {
  background: var(--vp-c-bg-soft);
  color: var(--vp-c-brand-1);
}

button:focus-visible {
  outline: 2px solid var(--vp-c-brand-1);
  outline-offset: -4px;
}

button:disabled {
  cursor: wait;
  opacity: 0.72;
}

.is-copied {
  border-color: var(--m-good);
}

.is-copied button {
  color: var(--m-good);
}

.is-error {
  border-color: var(--m-warn);
}

.is-error button {
  color: var(--m-warn);
}

@media (max-width: 520px) {
  .command-text {
    align-items: flex-start;
    overflow-x: visible;
    padding: 10px 12px;
  }

  code {
    overflow-wrap: anywhere;
    font-size: 11.5px;
    white-space: pre-wrap;
  }
}

@media (prefers-reduced-motion: reduce) {
  .command-copy,
  button {
    transition: none;
  }
}
</style>
