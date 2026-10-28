import { copyFileSync, mkdirSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { defineConfig } from "vitepress";

const repo = "https://github.com/pklnx/mail-archive";

// The licence texts of what the pklnx theme puts into the built site: the IBM
// Plex fonts (SIL OFL 1.1) and the Carbon icons in its CSS (Apache-2.0, in the
// package's third-party notices). The minifier strips the CSS header comment
// that names them, so the files are copied into every build (buildEnd below).
const designSystem = dirname(createRequire(import.meta.url).resolve("@pklnx/design-system/package.json"));
const licenseFiles = {
  "licenses/IBM-Plex-OFL.txt": join(designSystem, "project/fonts/OFL.txt"),
  "licenses/pklnx-design-system-notices.txt": join(designSystem, "THIRD_PARTY_NOTICES.md"),
};

export default defineConfig({
  title: "mail-archive",
  description: "A read-only, deduplicated archive of your IMAP mailboxes, with full-text search and a web UI.",
  lang: "en",
  base: "/mail-archive/",
  cleanUrls: true,
  lastUpdated: true,
  // Fail the build on broken internal links; links to the local UI are fine.
  ignoreDeadLinks: "localhostLinks",
  // The mail-archive tile as favicon (BRAND-ASSETS.md): PNG for Safari, SVG for the rest.
  head: [
    ["link", { rel: "icon", type: "image/png", sizes: "32x32", href: "/mail-archive/mail-archive-favicon-32.png" }],
    ["link", { rel: "icon", type: "image/svg+xml", href: "/mail-archive/mail-archive-tile.svg" }],
    // media first: VitePress merges meta tags by their first attribute other
    // than content, so with name first the second would replace the first.
    ["meta", { media: "(prefers-color-scheme: light)", name: "theme-color", content: "#ffffff" }],
    ["meta", { media: "(prefers-color-scheme: dark)", name: "theme-color", content: "#161616" }],
  ],
  buildEnd(site) {
    for (const [to, from] of Object.entries(licenseFiles)) {
      const dest = join(site.outDir, to);
      mkdirSync(dirname(dest), { recursive: true });
      copyFileSync(from, dest);
    }
  },
  themeConfig: {
    // Decorative: the site title next to it is the name.
    logo: { light: "/mail-archive-mark-light.svg", dark: "/mail-archive-mark-dark.svg", alt: "" },
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
          { text: "Importing mail", link: "/guide/importing" },
          { text: "Journaling with Postfix", link: "/guide/journaling" },
          { text: "Web UI and search", link: "/guide/web-ui" },
          { text: "Users", link: "/guide/users" },
          { text: "Audit log", link: "/guide/audit-log" },
          { text: "Backups and upgrades", link: "/guide/operations" },
          { text: "Tamper evidence", link: "/guide/tamper-evidence" },
          { text: "Retention and legal hold", link: "/guide/retention" },
        ],
      },
      {
        text: "Reference",
        items: [
          { text: "Configuration", link: "/reference/configuration" },
          { text: "Command line", link: "/reference/cli" },
          { text: "JSON API", link: "/reference/api" },
          { text: "OpenAPI", link: "/reference/openapi" },
          { text: "Security", link: "/reference/security" },
        ],
      },
      {
        text: "Development",
        items: [
          { text: "Contributing", link: "/development/" },
          { text: "Issues, plans and releases", link: "/development/process" },
          { text: "Architecture", link: "/development/architecture" },
          { text: "Flaky tests", link: "/development/flaky-tests" },
          { text: "Review gate", link: "/development/review-gate" },
        ],
      },
    ],
    socialLinks: [{ icon: "github", link: repo }],
    editLink: { pattern: `${repo}/edit/main/docs/:path`, text: "Edit this page on GitHub" },
    search: { provider: "local" },
    outline: { level: [2, 3] },
    footer: {
      message: "Entirely vibe-coded: written by an AI under human direction. Review before trusting it with important data.",
      copyright:
        'Fonts: IBM Plex, <a href="/mail-archive/licenses/IBM-Plex-OFL.txt">SIL Open Font License 1.1</a>. ' +
        'Icons: Carbon, <a href="/mail-archive/licenses/pklnx-design-system-notices.txt">Apache-2.0</a>.',
    },
  },
});
