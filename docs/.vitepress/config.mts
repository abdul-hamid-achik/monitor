import { defineConfig } from 'vitepress'

// https://vitepress.dev/reference/site-config
export default defineConfig({
  title: 'Monitor — Agent-Harnessable System Monitor for macOS & Linux',
  description:
    'A terminal-based, agent-harnessable system monitor for macOS and Linux. Interactive TUI, JSON CLI commands, and an MCP server — for humans, scripts, and AI agents.',
  lastUpdated: true,
  cleanUrls: true,

  // Root deploy (the monitorcli.dev custom domain on Vercel). Set
  // DOCS_BASE=/monitor/ to build for a GitHub Pages project site instead.
  base: process.env.DOCS_BASE ?? '/',


  head: [
    // Icons
    ['link', { rel: 'icon', type: 'image/svg+xml', href: '/favicon.svg' }],
    ['link', { rel: 'icon', type: 'image/x-icon', href: '/favicon.ico' }],
    ['link', { rel: 'apple-touch-icon', sizes: '180x180', href: '/apple-touch-icon.png' }],
    ['link', { rel: 'stylesheet', href: '/custom.css' }],

    // Primary meta
    ['meta', { name: 'description', content: 'A terminal-based, agent-harnessable system monitor for macOS and Linux. Interactive TUI, JSON CLI commands, and an MCP server — for humans, scripts, and AI agents.' }],
    ['meta', { name: 'keywords', content: 'system monitor, terminal monitor, macOS monitor, Linux monitor, CLI monitor, MCP server, process monitor, anomaly detection, pprof profiler, Bubble Tea TUI, Go system monitor, agent monitoring tool, htop alternative' }],
    ['meta', { name: 'author', content: 'Abdul Hamid Achik' }],
    ['meta', { name: 'robots', content: 'index, follow' }],

    // Canonical
    ['link', { rel: 'canonical', href: 'https://monitorcli.dev' }],

    // Open Graph
    ['meta', { property: 'og:type', content: 'website' }],
    ['meta', { property: 'og:title', content: 'Monitor — Agent-Harnessable System Monitor for macOS & Linux' }],
    ['meta', { property: 'og:description', content: 'A terminal-based system monitor with an interactive TUI, JSON CLI, and MCP server. CPU, memory, temperature, processes, anomaly detection, profiling, and ecosystem integrations.' }],
    ['meta', { property: 'og:url', content: 'https://monitorcli.dev' }],
    ['meta', { property: 'og:site_name', content: 'Monitor' }],
    ['meta', { property: 'og:image', content: 'https://monitorcli.dev/og-image.png' }],
    ['meta', { property: 'og:image:width', content: '1200' }],
    ['meta', { property: 'og:image:height', content: '630' }],
    ['meta', { property: 'og:image:alt', content: 'Monitor — Agent-harnessable system monitor for macOS and Linux' }],

    // Twitter Card
    ['meta', { name: 'twitter:card', content: 'summary_large_image' }],
    ['meta', { name: 'twitter:title', content: 'Monitor — Agent-Harnessable System Monitor for macOS & Linux' }],
    ['meta', { name: 'twitter:description', content: 'A terminal-based system monitor with an interactive TUI, JSON CLI, and MCP server — for humans, scripts, and AI agents.' }],
    ['meta', { name: 'twitter:image', content: 'https://monitorcli.dev/og-image.png' }],
    ['meta', { name: 'twitter:creator', content: '@abdulachik' }],

    // JSON-LD structured data
    ['script', { type: 'application/ld+json' }, JSON.stringify({
      '@context': 'https://schema.org',
      '@type': 'SoftwareApplication',
      name: 'Monitor',
      applicationCategory: 'DeveloperApplication',
      operatingSystem: 'macOS, Linux',
      description: 'A terminal-based, agent-harnessable system monitor for macOS and Linux. Interactive TUI, JSON CLI, and MCP server.',
      url: 'https://monitorcli.dev',
      downloadUrl: 'https://github.com/abdul-hamid-achik/monitor/releases',
      codeRepository: 'https://github.com/abdul-hamid-achik/monitor',
      license: 'https://opensource.org/licenses/MIT',
      offers: { '@type': 'Offer', price: '0', priceCurrency: 'USD' },
      author: { '@type': 'Person', name: 'Abdul Hamid Achik' },
    })],
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
