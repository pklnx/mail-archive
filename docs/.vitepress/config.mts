import { defineConfig } from "vitepress";

const repo = "https://github.com/pklnx/mail-archive";

export default defineConfig({
  title: "mail-archive",
  description: "A read-only, deduplicated archive of your IMAP mailboxes, with full-text search and a web UI.",
  lang: "en",
  base: "/mail-archive/",
  cleanUrls: true,
  lastUpdated: true,
  // Fail the build on broken internal links; links to the local UI are fine.
  ignoreDeadLinks: "localhostLinks",
  head: [
    ["link", { rel: "icon", type: "image/svg+xml", href: "/mail-archive/logo.svg" }],
    ["meta", { name: "theme-color", content: "#2563eb" }],
  ],
  themeConfig: {
    logo: "/logo.svg",
    nav: [
      { text: "Guide", link: "/guide/getting-started" },
      { text: "Reference", link: "/reference/configuration" },
      { text: "Development", link: "/development/" },
    ],
    sidebar: [
      {
        text: "Guide",
        items: [
          { text: "Getting started", link: "/guide/getting-started" },
          { text: "Accounts and providers", link: "/guide/accounts" },
          { text: "Syncing", link: "/guide/syncing" },
          { text: "Web UI and search", link: "/guide/web-ui" },
          { text: "Users", link: "/guide/users" },
          { text: "Backups and upgrades", link: "/guide/operations" },
        ],
      },
      {
        text: "Reference",
        items: [
          { text: "Configuration", link: "/reference/configuration" },
          { text: "Command line", link: "/reference/cli" },
          { text: "JSON API", link: "/reference/api" },
          { text: "Security", link: "/reference/security" },
        ],
      },
      {
        text: "Development",
        items: [
          { text: "Contributing", link: "/development/" },
          { text: "Issues, plans and releases", link: "/development/process" },
          { text: "Architecture", link: "/development/architecture" },
        ],
      },
    ],
    socialLinks: [{ icon: "github", link: repo }],
    editLink: { pattern: `${repo}/edit/main/docs/:path`, text: "Edit this page on GitHub" },
    search: { provider: "local" },
    outline: { level: [2, 3] },
    footer: {
      message: "Entirely vibe-coded: written by an AI under human direction. Review before trusting it with important data.",
    },
  },
});
