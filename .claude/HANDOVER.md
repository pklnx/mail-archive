# Handover

Current state only. At session end replace these sections; never append. History lives in PRs, issues and git.

## Last release
- **v1.5.3** (2026-11-12, main 1099490, release run 43300970476, dry run 43300547722): security hardening from an internal review, no advisory (maintainer decision, internal review). #534 (legal-hold match counts and purge preview only for own accounts, hold filters without SMTP envelopes; IMAP client idle deadline, read budget, streaming caps, truncated literals never stored; passkey begin caps; "new" counts per account; pg_dump/pg_restore allowlisted environment; webhook alert values rendered literally; CSS URLs in served HTML inert, mail with inline SVG/MathML never loads remote images), #535 (IMAP run time limit, redial, stop before a message over 256 MiB with alert, listing caps, four `MAIL_ARCHIVE_*` IMAP limit settings), #536 (docs: plain HTTP outside localhost), #537 (login-check limits, error categories instead of server text). No migration. Milestone 1.5.3 closed.
- **v1.5.2** (2026-11-12, main 1be6464, release run 43219552949, dry run 43217591186): security fixes of advisory GHSA-w6jg-ffvw-q4f5 via #529: login/2FA/API-key limits under concurrency, 2FA at most 3 codes (migration 00038), served-HTML neutralising (link hints, referrer, crossorigin, frame src), `restore-dump --plaintext`, journaled-copy match on Message-ID+Date+From+Subject. Released as a patch despite `db-migration` (maintainer exception, 2026-11-12, `version` input). Milestone 1.5.2 closed.
- **v1.5.1** (2026-11-12, main f88e9d6): Go 1.27.4, x/net 0.62.0 (#524/#525).
- **v1.5.0** (2026-11-05, main 7482e83): encryption at rest (#392), auditor access (#393), GoBD export on the command line (#394). Migrations 00036 and 00037. Milestone 1.5.0 is closed. Release test #520 is open.
- Earlier: v1.4.0 (main 774a2a0), customer groups (#470); release test #504 passed.

## Open PRs
- Dependabot #527 (go-dependencies, based on a3609fd, needs `@dependabot rebase` onto main after #529; it replaces #523 and #491): fxamacker/cbor/v2 2.9.6 → 2.10.0, go-pdfium 1.21.3 → 1.21.4 (PDFium engine 8057 → 8111), go.yaml.in/yaml/v3 3.0.5 → 3.0.6. Milestone 1.6.0. Neither the new PDFium nor the cbor minor has an advisory, so they are not patch material (coordinator, 2026-11-12). It needs green CI and one review. Check two things: the PDF text extraction (`internal/attachtext`), and cbor, which is direct only in `internal/web/passkeys_test.go` but which go-webauthn uses for passkeys. It gets `second-review` (auth).
- Dependabot #522 (vitepress alpha.20 → alpha.21, docs site only): milestone 1.6.0. It is behind main and needs `@dependabot rebase`, green CI and one review.

## Open tickets
- **1.6.0:** these are `ready`:
  - #395 SMTP AUTH/TLS (#454–#456);
  - #396 start without CLI (#451–#453). Part 3 (#453) runs `encryption.EnableAtSetup` from #392.
  - #398 WebKit e2e with #343.
- **1.6.0, in `plan`:** #397 operator/quotas/mail alerts (#446–#450), with the quota per customer group and alerts to admins only.
- **Not planned yet:** #432 database encryption, #418 OCR for scanned PDFs.
- **1.7.0 (Microsoft and Google):** #391 OAuth2 (`ready`, #426–#430); #475 journaling from Microsoft 365 (after #430, not planned).
- **1.6.0 security follow-ups from the 1.5.3 review:** #538 (deletion-run counts and hold-match timing across accounts; a plan exists in the private review repository), #539 (residual cross-group signals: attachment text, tombstones, run timing), #540 (pg tools and TLS `PG*` variables without Docker), #541 (startup warning on plain HTTP); #533 (hardening backlog).
- **Follow-up:** #530 (private tracker numbers in 1.5.2 code comments → advisory id, comment-only); #516, the demo auditor's grant window after `DOCS_NOW` + 90 days (before late January 2027).
- **Device checks:** #372 round 2, Android parts open; #520 is the 1.5.0 release test.

## Active decisions
- **Security work** (maintainer, 2026-11-12): from 1.5.3 on, security work happens in this public repository; the private review repository is archived (read-only; it holds the 1.5.2/1.5.3 plans and reviews). Public issues describe the problem without exploit steps. Its Actions minutes ran out (3,000/month), so a private repository needs local tests or a self-hosted Linux runner (the maintainer offered a Mac mini; a Linux VM would be needed for service containers).
- **IMAP limits** (maintainer, 2026-11-12): a message over 256 MiB stops its folder before it with a health failure (alert, later "recovered"); the 4 GB defaults are for small and medium instances, larger ones raise `MAIL_ARCHIVE_FILL_MAX_FOLDER_MESSAGES`, `MAIL_ARCHIVE_RECONCILE_MAX_MESSAGES`, `MAIL_ARCHIVE_IMAP_MAX_FOLDERS`, `MAIL_ARCHIVE_IMAP_IDLE_TIMEOUT` (operations.md, "IMAP limits").
- **Served HTML mail** (maintainer, 2026-11-12): mail containing `<svg` or `<math` never loads remote images (parser divergences between x/net/html and Chromium); CSS images are never loaded.
- **Operating model** (maintainer, 2026-11-01): one shared instance in full-service mode, where the operator is its admin and manages the customers' users and accounts. Individual instances for single customers on request. Customer groups shipped in 1.4.0.
- **Coordinator autonomy** (maintainer, 2026-10-31):
  - The coordinator decides alone on UI wording, technical trade-offs, small product details inside an approved goal, and tightening security, and records these decisions.
  - It always asks about new goals and milestone scope, releases, and anything that widens access or weakens security or privacy (including `security.md` guarantees).
  - It reports at milestones and blockers only.
- **Customer groups** (maintainer, 2026-11-02/03):
  - One shared instance, with groups separating customers logically.
  - No group admin. Sharing works by exact name inside the group.
  - The group UI is hidden while there is only one group; the first extra group is created on the CLI.
  - Auditors belong to one group, and grants stay inside it. Exports never span groups.
- **Encryption at rest** (#392):
  - New instances start with it on (maintainer decision 6); existing ones switch offline with `encryption enable`.
  - `rekey` creates a new keyring key. `rotate-key` keeps the keyring key and replaces one slot: `--replace` names the slot, and without it the command refuses when the configured key is not the archive key (coordinator, #437 and #519 review rounds 2 and 3).
  - `rekey` drops every slot whose key is not at hand. It keeps the configured key, the keys given with `--keep-key-file` and a new recovery key, and it prints the new recovery key before the conversion. `add-key` and `remove-key` need the database.
  - The database stays plaintext until #432.
- **Merge mechanics:** the merge tool pins with `expectedHeadSha`, not `sha`. A branch behind main is refused for required checks; bring main in with a merge commit.
- **GitHub operations without an MCP tool** (maintainer, 2026-11-01): agents do them themselves, using the REST fallback in `.claude/github-operations.md`. In this cloud session the classifier refused `curl` with the token (Auto-Mode Bypass), so milestones stayed undone.
- **Disk:** the shared Go build cache fills the sandbox. Trimming it (`find /root/.cache/go-build -type f -mmin +180 -delete`) needs the maintainer to switch to "Accept edits" first.
- **Target hardware:** plans state memory and CPU cost on 4 GB RAM / 2 cores and stay within it.
- #459 (Safari): the server rewrites links in served HTML mail, and navigations to `/api` with `Sec-Fetch-Site` `cross-site` or `same-site` get 403. Stored mail, `/raw`, parts and exports stay byte-identical.
- **Product details:**
  - Target group: individuals and small businesses.
  - #321: Disable stays usable while a sync runs; only Remove is blocked.
  - German term for the deletion grace period: "Karenzzeit" (#339).
- **Releases and PR bodies:**
  - No backports, only the newest line; old tags and images stay.
  - Patch releases carry only `bug`/`docs` PRs.
  - Avoid "fix #N" and other closing keywords in PR bodies for issues that stay open. Conversely, check that "Closes #N" really closed the issue: #435 stayed open after #513.

## Maintainer actions
- Paste the operator notes into the v1.5.3 release (this session may not edit releases; text handed over in chat).
- Archive the private review repository (Settings → Archive).
- 1.5.3 checks on a real instance: sync against a real IMAP server (plain, TLS, STARTTLS) incl. a second run with nothing new; one test alert each to Slack, Mattermost and Discord if used; WebKit and Firefox on HTML mail.
- Report the two x/net/html parser divergences found in the 1.5.3 review upstream (Go issue tracker): a `<template>` inside SVG/MathML integration points ends the tree; a duplicate attribute with unquoted value `/` marks the tag self-closing.
- #520 release test of 1.5.0.
- Manual checks from #519, which no test covers: `./ma encryption rotate-key --new-key-file /keys/archive-2.key` through Compose (service `mail-archive-keys`: the new file owned by 65532 with mode 0400, then `.env` switched and `docker compose up -d`), and `./ma encryption rekey` on a real instance, including `--keep-key-file`.
- On the shared instance:
  - Back up, then run `./ma migrate` (00036, 00037).
  - Encryption at rest is optional: `./ma encryption keygen`, keep the archive key and the recovery key off the server, then `./ma encryption enable` with web and worker stopped (operations.md, "Encryption at rest").
