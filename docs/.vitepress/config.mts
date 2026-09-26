import { defineConfig } from 'vitepress'

const base = process.env.DOCS_BASE ?? '/'

// https://vitepress.dev/reference/site-config
export default defineConfig({
  title: 'Monitor',
  description:
    'Local error tracking that points at the line, with no SDK — plus a live terminal Studio, JSON CLI and a safe MCP server for macOS and Linux.',
  lastUpdated: true,
  // Studio is dark-first; the site opens the same way (the toggle still
  // offers the light palette, which mirrors Studio's light variant).
  appearance: 'dark',

  markdown: {
    container: {
      tipLabel: 'tip',
      infoLabel: 'info',
      warningLabel: 'warning',
      dangerLabel: 'danger',
      detailsLabel: 'details',
    },
  },
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
    ['meta', { name: 'theme-color', content: '#fbfaf8', media: '(prefers-color-scheme: light)' }],
    ['meta', { name: 'theme-color', content: '#11161d', media: '(prefers-color-scheme: dark)' }],

    // Open Graph
    ['meta', { property: 'og:type', content: 'website' }],
    ['meta', { property: 'og:title', content: 'Monitor — crashes that point at the line' }],
    ['meta', { property: 'og:description', content: 'Crashes from Node, Deno, Bun, Python, Ruby and Go become grouped local issues with a culprit file:line. No SDK, no account.' }],
    ['meta', { property: 'og:url', content: 'https://monitorcli.dev' }],
    ['meta', { property: 'og:site_name', content: 'Monitor' }],

    // Twitter Card
    ['meta', { name: 'twitter:card', content: 'summary' }],
    ['meta', { name: 'twitter:title', content: 'Monitor — crashes that point at the line' }],
    ['meta', { name: 'twitter:description', content: 'Crashes from Node, Deno, Bun, Python, Ruby and Go become grouped local issues with a culprit file:line. No SDK, no account.' }],
    ['meta', { name: 'twitter:creator', content: '@abdulachik' }],

    // JSON-LD structured data
    ['script', { type: 'application/ld+json' }, JSON.stringify({
      '@context': 'https://schema.org',
      '@type': 'SoftwareApplication',
      name: 'Monitor',
      applicationCategory: 'DeveloperApplication',
      operatingSystem: 'macOS, Linux',
      softwareVersion: '2.0.0',
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
    // No image logo: the nav draws the Studio's "◆ monitor" wordmark in CSS.
    siteTitle: 'monitor',

    notFound: {
      code: '404',
      title: 'no such route',
      quote:
        'Nothing is listening on this path — it is not in the process tree. Head back and keep watching what matters.',
      linkText: '← back to monitor',
    },
    nav: [
      { text: 'Install', link: '/guide/installation' },
      { text: 'Guide', link: '/guide/getting-started' },
      { text: 'Reference', link: '/reference/architecture' },
      {
        text: 'v2.0.0',
        items: [
          {
            text: 'Release notes',
            link: 'https://github.com/abdul-hamid-achik/monitor/releases/tag/v2.0.0',
          },
          {
            text: 'All releases',
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
            { text: 'Your First Issue', link: '/guide/first-issue' },
            { text: 'The TUI', link: '/guide/tui' },
          ],
        },
        {
          text: 'Error tracking',
          items: [
            { text: 'Runtimes Matrix', link: '/guide/runtimes' },
            { text: 'Hot Lines', link: '/guide/hot-lines' },
            { text: 'Local Issues', link: '/guide/issues' },
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
            { text: 'Doctor v1', link: '/contracts/doctor-v1' },
            { text: 'Issue Context v1 (Draft)', link: '/contracts/issue-context-v1' },
            { text: 'Line Heatmap v1 (Draft)', link: '/contracts/line-heatmap-v1' },
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
