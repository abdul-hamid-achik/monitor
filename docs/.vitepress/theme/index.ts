import type { Theme } from 'vitepress'
import DefaultTheme from 'vitepress/theme'
import '@fontsource-variable/geist'
import '@fontsource-variable/geist-mono'
import HomeLanding from './components/HomeLanding.vue'
import InstallPanel from './components/InstallPanel.vue'
import './custom.css'
import { inject } from '@vercel/analytics'

export default {
  extends: DefaultTheme,
  enhanceApp({ app }) {
    if (typeof window !== 'undefined') {
      inject()
    }
    app.component('HomeLanding', HomeLanding)
    app.component('InstallPanel', InstallPanel)
  },
} satisfies Theme
