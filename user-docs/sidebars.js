// @ts-check

const sidebars = {
  docsSidebar: [
    'intro',
    {
      type: 'category',
      label: 'Core Concepts',
      collapsed: false,
      items: [
        'concepts/what-is-a-sandbox',
        'concepts/bookings-and-slots',
        'concepts/notebook-lifecycle',
        'concepts/statuses-and-events',
        'concepts/roles-credits-and-limits',
      ],
    },
    {
      type: 'category',
      label: 'Getting Started',
      collapsed: false,
      items: [
        'getting-started/authentication',
        'getting-started/create-profile',
        'getting-started/launch-jupyter-lite',
        'getting-started/create-a-booking',
        'getting-started/open-your-notebook',
      ],
    },
    {
      type: 'category',
      label: 'API Guides',
      collapsed: false,
      items: [
        'api-guides/bookings',
        'api-guides/slots-and-calendar',
        'api-guides/notebooks',
        'api-guides/profiles',
        'api-guides/api-reference',
      ],
    },
    {
      type: 'category',
      label: 'Tutorials',
      collapsed: false,
      items: [
        'tutorials/getting-started/launch-a-cpu-sandbox',
        'tutorials/getting-started/launch-a-gpu-sandbox',
        'tutorials/booking-workflows/extend-cancel-terminate',
        'tutorials/developer-workflows/curl-quickstart',
      ],
    },
    {
      type: 'category',
      label: 'Troubleshooting',
      collapsed: false,
      items: [
        'troubleshooting/common-errors',
        'troubleshooting/notebook-stuck-opening',
      ],
    },
  ],
};

module.exports = sidebars;
