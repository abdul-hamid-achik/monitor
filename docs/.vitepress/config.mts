import { defineConfig } from 'vitepress'

// https://vitepress.dev/reference/site-config
export default defineConfig({
  title: 'Monitor',
  description:
    'A terminal-based, agent-harnessable system monitor for macOS and Linux.',
  lastUpdated: true,
  cleanUrls: true,

  themeConfig: {
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
          items: [{ text: 'Process Safety', link: '/guide/safety' }],
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
