import type { Theme } from 'vitepress'
import DefaultTheme from 'vitepress/theme'
import HomeLanding from './components/HomeLanding.vue'
import InstallPanel from './components/InstallPanel.vue'
import TerminalMockup from './components/TerminalMockup.vue'
import './custom.css'

export default {
  extends: DefaultTheme,
  enhanceApp({ app }) {
    app.component('HomeLanding', HomeLanding)
    app.component('InstallPanel', InstallPanel)
    app.component('TerminalMockup', TerminalMockup)
  },
} satisfies Theme
