---
layout: home

hero:
  name: mail-archive
  text: All your mailboxes, one read-only archive
  image:
    src: /logo.svg
    alt: mail-archive logo
  tagline: Copies mail from IMAP accounts into deduplicated .eml files and PostgreSQL, with full-text search and a web UI. The servers are never modified.
  actions:
    - theme: brand
      text: Get started
      link: /guide/getting-started
    - theme: alt
      text: View on GitHub
      link: https://github.com/pklnx/mail-archive

features:
  - title: Read-only
    details: Folders are opened with EXAMINE and fetched with BODY.PEEK[]. Nothing changes on the server, not even the \Seen flag. Mail deleted on the server stays in the archive and is marked as only in archive.
  - title: Many mailboxes, one archive
    details: Identical messages are stored once. For every message the archive remembers each account, folder and UID where it was found.
  - title: Plain files
    details: Raw messages are .eml files that any mail client opens. Metadata and the search index live in PostgreSQL.
  - title: Full-text search
    details: Subject, sender and body, with German and English stemming. Quotes, OR and -word work as expected.
  - title: Web UI
    details: Browse, search and read mail, manage accounts and start syncs. Light and dark mode, German and English, works on phones.
  - title: Runs anywhere Docker runs
    details: Docker Compose setup with images for amd64 and arm64, so it runs natively on Apple Silicon Macs.
---

<div class="screenshot">
  <img class="light-only" src="/screenshots/mail-light.png" alt="The mail-archive web UI: accounts and folders, the message list and an open message">
  <img class="dark-only" src="/screenshots/mail-dark.png" alt="The mail-archive web UI in dark mode">
</div>

::: warning Entirely vibe-coded
All code, configuration and documentation were written by an AI (Claude) under
human direction, without line-by-line human review. Review it yourself before
trusting it with important data, and keep independent backups of your mail.
:::
