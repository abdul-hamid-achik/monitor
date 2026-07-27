import { defineConfig } from 'vitepress'

const base = process.env.DOCS_BASE ?? '/'

// https://vitepress.dev/reference/site-config
export default defineConfig({
  title: 'Monitor',
  description:
    'Local-first observability for macOS and Linux with grouped issues, incident evidence, a terminal Studio, JSON CLI, and safe MCP server.',
  lastUpdated: true,
  cleanUrls: true,

  // Root deploy (the monitorcli.dev custom domain on Vercel). Set
  // DOCS_BASE=/monitor/ to build for a GitHub Pages project site instead.
  base,


  head: [
    // Icons
    ['link', { rel: 'icon', type: 'image/svg+xml', href: `${base}favicon.svg` }],
    ['link', { rel: 'apple-touch-icon', href: `${base}apple-touch-icon.png` }],

    // Primary meta
    ['meta', { name: 'keywords', content: 'local observability, local issue tracker, system monitor, terminal monitor, macOS monitor, Linux monitor, CLI monitor, MCP server, process monitor, anomaly detection, pprof profiler, Bubble Tea TUI, Go system monitor, agent monitoring tool' }],
    ['meta', { name: 'author', content: 'Abdul Hamid Achik' }],
    ['meta', { name: 'robots', content: 'index, follow' }],

    // Open Graph
    ['meta', { property: 'og:type', content: 'website' }],
    ['meta', { property: 'og:title', content: 'Monitor — Local-first observability from your terminal' }],
    ['meta', { property: 'og:description', content: 'A live terminal Studio, automation-ready JSON, and safe MCP tools in one local-first binary.' }],
    ['meta', { property: 'og:url', content: 'https://monitorcli.dev' }],
    ['meta', { property: 'og:site_name', content: 'Monitor' }],

    // Twitter Card
    ['meta', { name: 'twitter:card', content: 'summary' }],
    ['meta', { name: 'twitter:title', content: 'Monitor — Local-first observability from your terminal' }],
    ['meta', { name: 'twitter:description', content: 'A live terminal Studio, automation-ready JSON, and safe MCP tools in one local-first binary.' }],
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
    logo: { src: '/favicon.svg', alt: '' },
    siteTitle: 'Monitor',

    notFound: {
      code: '404',
      title: 'PAGE NOT FOUND',
      quote:
        'No signal on this route — it is not in the process tree. Head back and keep watching what matters.',
      linkText: 'Take me home',
    },
    nav: [
      { text: 'Install', link: '/guide/installation' },
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
            { text: 'Installation', link: '/guide/installation' },
            { text: 'Getting Started', link: '/guide/getting-started' },
            { text: 'The TUI', link: '/guide/tui' },
          ],
        },
        {
          text: 'The agent surface',
          items: [
            { text: 'CLI Reference', link: '/guide/cli' },
            { text: 'MCP Server', link: '/guide/mcp' },
            { text: 'Local Issues', link: '/guide/issues' },
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
            { text: 'Telemetry Contract', link: '/reference/telemetry' },
            { text: 'Incident Bundle Contract', link: '/contracts/monitor-incident-v1' },
            { text: 'Configuration', link: '/reference/configuration' },
          ],
        },
      ],
      '/contracts/': [
        {
          text: 'Contracts',
          items: [
            { text: 'Monitor Incident v1', link: '/contracts/monitor-incident-v1' },
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
