# Handover

Current state only. At session end replace these sections; never append. History lives in PRs, issues and git.

## Where development continues
From 2026-11-13 on, the maintainer continues on a Mac mini with OrbStack (local Claude Code sessions instead of the cloud sandbox). The test database there is an OrbStack container `mail-archive-pg` (`postgres:18`, user/password/db `mailarchive`, bound to 127.0.0.1), matching CI's service; `TEST_DATABASE_URL` and `DEMO_DATABASE_URL` use the Makefile/CI defaults. Locally, `gh` GraphQL works; the REST fallbacks in `.claude/github-operations.md` remain valid. The cloud-only notes (Go cache trimming, classifier refusals, container restarts) no longer apply.

## Last release
- **v1.5.3** (2026-11-12, main 1099490): security hardening from an internal review. Earlier: v1.5.2 (GHSA-w6jg-ffvw-q4f5), v1.5.1, v1.5.0 (encryption at rest, auditors, GoBD export), v1.4.0 (customer groups).

## Milestone 1.6.0: state
Maintainer's scope: #395, #396, #398+#343, #397, #538–#541. **After 1.6.0 is released, work stops** (maintainer, 2026-11-13).

**Done (merged and closed):** #395 (#454–#456), #396 (#451–#453), #398 and #343, #538 (#563), #540 (#561), #541 (#560), #550 (#557), #552 (#556); #397 parts 1–3: #446 (#569), #447 (#573), #448 (#572); #539 parts 1–2: #566 (#570), #567 (#574).

**Open, in this order:**
1. **PR #575** (#568, #539 part 3), head `0a8b232`, CI all green, labels `security`, `second-review`. Both reviewers approved at `191c5c7`; fix round 2 (`191c5c7..0a8b232`, comment 6856155430: merge of main #573; the move race closed by a save-time group check, maintainer decision 6854935915; WebKit flaky row) still needs one **delta review from each of two independent reviewers**. Then merge check, squash merge pinned to the head, close #568 with metrics, then **close #539** with a summary (all three signals, every maintainer decision on #539) and metrics.
2. **#449** (#397 part 4): branch `feat/449-storage-ui` pushed at `01e402d`, **no PR yet**. PR body draft: comment 6856722433 on #449. Open the PR (label `feature`, add `second-review`), two independent reviews (UI, API, privacy: member JSON byte-identical whether group or instance is full; no `warn` for members, coordinator decision), merge.
3. **#450** (#397 part 5): branch `feat/450-alert-mail` pushed at `e5b4c09`, **no PR yet**. PR body draft: comment 6856831936 on #450. Missing: docs screenshots (`make docs-screenshots`: new `profile-notifications.png`, changed `profile*.png`; revert anti-aliasing-only diffs; embed the new image in `email-alerts.md`). Migration `00045_alert_mail`. Coordinator decision: the notification address lives in table `user_notify_emails`, not on `users` (columns on `users` broke `./ma status` while the migration is pending). Open the PR (labels `feature`, `db-migration`, add `second-review`), two reviews (external service: outbound SMTP, TLS, password file; admins only; no leak markers in mails), merge. Then **close #397** with metrics.
4. **PR #576** (secret scan, opened from the Mac setup session): `make secrets` finds `00042_deletion_marks` in migration lists (`attest_test.go`, `storage_test.go`) and four fake values on branches of closed PRs. The `secret-scan` run of the push of 158a1e4 to main failed for this. It changes `.gitleaks.toml` and adds `.gitleaksignore`: **merge only after the maintainer's explicit OK** via AskUserQuestion listing the entries (CLAUDE.md, rule 7); note the OK on the PR. Alternative named in the PR: delete the stale branches of #548, #514, #506.
5. **Dependabot #527** (go-dependencies: cbor, go-pdfium, yaml; `second-review`, auth via go-webauthn) and **#522** (vitepress): rebase, CI, review. Optional for 1.6.0; the maintainer decides.
6. **Release 1.6.0:** present the release notes (draft below) to the maintainer and get the OK; then release per `docs/development/process.md`.

Rules that held this milestone: at most 2 PRs in CI at once; implementers commit locally and push only after the coordinator's go; every risky PR (`db-migration`, auth, external services, serving layer, cross-group privacy) gets two independent session reviewers who do not read each other.

## Release notes 1.6.0 (draft, collected from the PRs)
- **Before upgrading:** back up; stop web and worker before `./ma migrate` (a process of a version before 1.6.0 that still runs after the migration behaves as before 1.6.0 until it stops: search, and how syncs, fills and deliveries skip and store messages). Migrations 00039–00045 (00045 with #450).
- **Free disk reserve on by default:** `MAIL_ARCHIVE_MIN_FREE_DISK=1GiB` (`0` turns it off). Below it, syncs/fills/imports pause, SMTP answers `452 4.3.1`, `./ma sync` exits 75. Check free disk before upgrading.
- **Storage quotas** per customer group and instance (`MAIL_ARCHIVE_STORAGE_QUOTA`, `…_WARN_PERCENT`, `…_SMTP_OVERFLOW_PERCENT`, `./ma group set-quota`), `./ma usage`, storage alerts by webhook (and mail with #450). `serve` writes storage states; cron-only setups get alerts from `./ma sync`.
- **Webhooks default to `MAIL_ARCHIVE_NOTIFY_WEBHOOK_DETAIL=ids`** (no account, mailbox, folder names or error texts); set `full` for the old content. New `metrics` API-key scope (admins only) for `GET /api/metrics` (Prometheus).
- **Encryption and setup:** missing or wrong archive key stops `serve`; mount the key file; `/healthz` returns 503 while starting. First admin from a setup token; an empty instance without archive key refuses to start unless `ENCRYPTION_AT_SETUP=off`.
- **SMTP:** AUTH and TLS default to `required` (provide a certificate, or set `off` before upgrading); SMTP secrets expire; at most 16 connections; Docker log rotation.
- Warning for plain HTTP outside localhost (#560).
- **Legal holds:** filter holds over other users' mail only via `./ma hold add` (web answers 403 `filter_hold_cli`); review old web holds with `./ma hold list` (column CREATED BY).
- **Privacy across customer groups (#539):** deletions skip re-archiving only in the deleting account's group; attachment text becomes searchable per location after its own extraction (repair at start, every 15 minutes and in `./ma reindex`); the same work is done for a message new to the group whether or not another group has it.
- **Cost of the group lookups** on the save path (PG16, 2 cores, per batch of 100): about 0.1 s at 40 folders per group, 0.25–0.3 s at 400, about 0.7 s at 1,500 (up to 0.9–1 s when every message carries a deletion mark); a 5,000-folder group 0 about 1.4–2 s. Applies to every instance, also single-group ones (maintainer decision).
- **Without Docker:** `backup`/`restore-dump` refuse TLS settings given only as `PG*` variables unless they match; they refuse when `HOME` is unset on a TLS connection, and when only one of `~/.postgresql/postgresql.crt`/`.key` exists.

## Follow-ups (open `followup` issues)
#571 (storage docs: snapshot memory measured 190–290 MB at 1.5M messages, repair window, fold bound), #565 (legal-hold reference docs, web test), #564 (pg tools: verify-* without root certificate, lone client key), #562 (retention end vs. copies in other groups), #559 (`created_at` signals across groups), #554 (initial JS bundle headroom: about 10 KB left after #449 and #450), #547, #546, #533, #532, #530, #516.

## Active decisions
- **Privacy across customer groups (#539, maintainer 2026-11-13):** the promise holds across groups in 1.6.0; within a group the signals remain. Accepted and documented in `security.md`: failed extractions (after three failed attempts in a row, or `extractor unavailable`, until `jobs retry attachment_text`); shared job queue ("sooner or later"); purges by a version before 1.6.0; the upgrade window while a pre-1.6.0 process runs. No single-group shortcut (dropped by the maintainer); moves never overlap a batch's lookup (save-time group check).
- **Group lookups on the save path** are driven by the group's own folders through `message_locations_folder_sha_idx`, fenced (`LIMIT 1` inside the `LATERAL`) so neither the custom nor the generic cached plan uses `message_locations_sha_idx`; plan tests check both. Cost depends only on the group's own size.
- **Storage states:** one writer: `serve` holds an advisory lock on a dedicated connection and retakes it after a loss; commands persist only when no `serve` runs.
- **Plan reviews:** after three rounds finding holes in the same mechanism, rebuild on a principle (#539 part 2 was rebuilt as a per-location flag).
- **Security work** (maintainer, 2026-11-12): in this public repository; public issues describe problems without exploit steps. The private review repository is archived.
- **IMAP limits, served HTML mail, operating model, coordinator autonomy, customer groups, encryption at rest, target hardware, product details, releases:** unchanged from 1.5.x (see `docs/development/process.md`, `security.md`, `operations.md`); in short: one shared instance in full-service mode; the coordinator decides UI wording, technical trade-offs and tightening alone and asks about scope, releases and anything that weakens `security.md`; plans stay within 4 GB RAM / 2 cores; patch releases carry only `bug`/`docs`; no backports.
- **Merge mechanics:** squash merge pinned to the reviewed head SHA; a branch behind main is refused for required checks, bring main in with a merge commit; never force-push.
- **Reviewers and shared resources:** `pg_terminate_backend` only on the reviewer's own test database (a round-2 counter-proof on #573 terminated a backend on the shared cluster without that filter).

## Maintainer actions
- Decide on PR #576 (secret-scan allowlist and `.gitleaksignore`, or delete the stale branches) — needed before the next change to the migration lists.
- Release OK for 1.6.0 once #575, #449, #450 are merged.
- Checks on a real instance after 1.6.0: Postfix and `452 4.3.1` at RCPT and after DATA; a Prometheus scrape with a `metrics` key; an `ids` webhook alert in ntfy and Slack; `./ma hold list` CREATED BY for holds from before 1.6.0; alert mail through a real relay (smtps and STARTTLS; umlauts in Gmail and Outlook); the Storage page on a phone and in dark mode; a group quota below usage (paused sync, member banner).
- Earlier, still open: v1.5.3 operator notes into the release; 1.5.3 real-instance checks (IMAP plain/TLS/STARTTLS, Slack/Mattermost/Discord test alerts, WebKit/Firefox on HTML mail); report the two x/net/html parser divergences upstream; #520 release test of 1.5.0; manual checks from #519 (`rotate-key` via Compose, `rekey` with `--keep-key-file`); on the shared instance back up, then migrate.
