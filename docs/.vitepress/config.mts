import { defineConfig } from 'vitepress'

// https://vitepress.dev/reference/site-config
export default defineConfig({
  title: 'Monitor',
  description:
    'A terminal-based, agent-harnessable system monitor for macOS and Linux.',
  lastUpdated: true,
  cleanUrls: true,

  // Root deploy (the monitorcli.dev custom domain on Vercel). Set
  // DOCS_BASE=/monitor/ to build for a GitHub Pages project site instead.
  base: process.env.DOCS_BASE ?? '/',


  head: [
    ['link', { rel: 'icon', type: 'image/svg+xml', href: '/favicon.svg' }],
    ['link', { rel: 'icon', type: 'image/x-icon', href: '/favicon.ico' }],
    ['link', { rel: 'apple-touch-icon', sizes: '180x180', href: '/apple-touch-icon.png' }],
    ['meta', { name: 'description', content: 'monitor documentation site.' }],
  ],

  sitemap: { hostname: 'https://monitorcli.dev' },
  themeConfig: {
    logo: { src: '/logo.svg', dark: '/logo-dark.svg' },

    notFound: {
      code: '404',
      title: 'PAGE NOT FOUND',
      quote:
        'No signal on this route — it is not in the process tree. Head back and keep watching what matters.',
      linkText: 'Take me home',
    },
    nav: [
      { text: 'Guide', link: '/guide/getting-started' },
      { text: 'Reference', link: '/reference/architecture' },
      {
        text: 'v1.x',
        items: [
          {
            text: 'Changelog',
            link: 'https://github.com/abdul-hamid-achik/monitor/releases',
          },
        ],
      },
    ],

    sidebar: {
      '/guide/': [
        {
          text: 'Introduction',
          items: [
            { text: 'Getting Started', link: '/guide/getting-started' },
            { text: 'The TUI', link: '/guide/tui' },
          ],
        },
        {
          text: 'The agent surface',
          items: [
            { text: 'CLI Reference', link: '/guide/cli' },
            { text: 'MCP Server', link: '/guide/mcp' },
            { text: 'Ecosystem Integration', link: '/guide/ecosystem' },
          ],
        },
        {
          text: 'Operations',
          items: [
            { text: 'Anomaly Detection', link: '/guide/anomaly-detection' },
            { text: 'Process Safety', link: '/guide/safety' },
          ],
        },
      ],
      '/reference/': [
        {
          text: 'Reference',
          items: [
            { text: 'Architecture', link: '/reference/architecture' },
            { text: 'Configuration', link: '/reference/configuration' },
          ],
        },
      ],
    },

    socialLinks: [
      {
        icon: 'github',
        link: 'https://github.com/abdul-hamid-achik/monitor',
      },
    ],

    editLink: {
      pattern:
        'https://github.com/abdul-hamid-achik/monitor/edit/main/docs/:path',
      text: 'Edit this page on GitHub',
    },

    search: { provider: 'local' },

    footer: {
      message: 'Released under the MIT License.',
      copyright: 'Copyright © Abdul Hamid Achik',
    },
  },
})
