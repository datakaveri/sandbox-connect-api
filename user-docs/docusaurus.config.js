// @ts-check

const config = {
  title: 'Sandbox Connect',
  tagline: 'User docs and tutorials for booking-based sandbox notebooks',
  favicon: 'img/favicon.ico',

  url: 'https://docs.example.com',
  baseUrl: '/',

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
        { href: '/openapi/swagger.yaml', label: 'OpenAPI', position: 'right' },
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
