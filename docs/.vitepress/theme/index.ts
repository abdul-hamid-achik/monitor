import type { Theme } from 'vitepress'
import DefaultTheme from 'vitepress/theme'
import TerminalMockup from './components/TerminalMockup.vue'

export default {
  extends: DefaultTheme,
  enhanceApp({ app }) {
    app.component('TerminalMockup', TerminalMockup)
  },
} satisfies Theme