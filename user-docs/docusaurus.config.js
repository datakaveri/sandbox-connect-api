// @ts-check

const isDev = process.env.NODE_ENV !== 'production';
const siteUrl = process.env.DOCS_SITE_URL || (isDev ? 'http://localhost:3000' : 'https://docs.example.com');
const baseUrl = process.env.DOCS_BASE_URL || '/user-docs/';

const config = {
  title: 'Sandbox Connect',
  tagline: 'User docs and tutorials for booking-based sandbox notebooks',

  url: siteUrl,
  baseUrl,

  organizationName: 'datakaveri',
  projectName: 'sandbox-connect-api',

  onBrokenLinks: 'warn',
  onBrokenMarkdownLinks: 'warn',

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  presets: [
    [
      'classic',
      {
        docs: {
          sidebarPath: require.resolve('./sidebars.js'),
          routeBasePath: 'docs',
          editUrl: undefined,
        },
        blog: false,
        theme: {
          customCss: require.resolve('./src/css/custom.css'),
        },
      },
    ],
  ],

  plugins: [
    [
      '@easyops-cn/docusaurus-search-local',
      {
        hashed: true,
        indexDocs: true,
        indexBlog: false,
        indexPages: true,
      },
    ],
  ],

  themeConfig: {
    navbar: {
      title: 'Sandbox Connect',
      items: [
        { to: '/docs/intro', label: 'Docs', position: 'left' },
        { to: '/docs/tutorials/getting-started/launch-a-cpu-sandbox', label: 'Tutorials', position: 'left' },
        { to: '/docs/api-guides/api-reference', label: 'OpenAPI', position: 'right' },
      ],
    },
    docs: {
      sidebar: {
        hideable: true,
      },
    },
    prism: {
      theme: require('prism-react-renderer').themes.github,
      darkTheme: require('prism-react-renderer').themes.dracula,
    },
  },
};

module.exports = config;
