# Handover

State of the work for the next session. Update before a session ends.

## Open PRs
- None besides this handover update.

## Next tickets
- #168 Carbon Design System migration. Plan revised and approved (`ready`). Maintainer decisions 2026-10-22: account add, edit and import are pages; the shell SideNav holds the app sections as a rail, accounts and folders go in a page-level TreeView column that keeps #160; phones get Carbon's hamburger overlay and the bottom bar is dropped in 4a; screenshot exception: docs screenshots and docs text are updated only in part 6.
  - Parts 1-3 are merged: #169 (PR #181), #170 (PR #186), #171 (PR #188). **Part 3 is the maintainer's evaluation point.** Parts 4a #172, 4b #177 (created when the plan revision split part 4), 5 #173 and 6 #174 wait for the maintainer's sign-off on look and effort.
- Unplanned bugs, label `bug` only, wait for `plan`:
  - #182: inline `cid:` images are blocked in the sandboxed frame by CORP; the e2e guard allows that console message for now.
  - #187: an account shows `idle` between leaving the sync queue and taking the sync lock; may have widened with #183.
- Optional release: since 26.10-8, main has #155, #166, #176, #169 (e2e) and Carbon parts 2-3. No release without the maintainer.

## Done (2026-10-22/23)
- #155 complete account purge: CLI PR #165, web UI PR #167.
- #166 deletion run and blob sweep on dedicated session connections (PR #180).
- #176 bounded blob lock waits: #178 PR #183 (one wait slot per process); account delete/remove on one connection: #179 PR #184.
- PR #185: race in `TestReconcileButton` fixed, with a row in flaky-tests.md.
- #169 Playwright e2e suite, CI job `web-e2e` (PR #181).
- #170 Carbon scaffold: header, theme on `<html>`, login on Carbon, local Plex fonts with CDN rules stripped and a dist check, IBM telemetry install scripts blocked via `web/pnpm-workspace.yaml` and `IBM_TELEMETRY_DISABLED` (PR #186).
- #171 Accounts page on Carbon: add, edit and import pages via URL keys, `DangerModal`, purge as a danger modal, `.legacy` per view, `app-` prefixed helpers with a collision test (PR #188).

## Done (2026-10-21/22)
- #115: durable blob publication and removal, merged via #121 (local Opus review + Action second review).
- #116: shared-message isolation and retention regression tests, merged via #123 (Action review).
- #117: root demo artifact removed, merged via #118.
- #119: agent workflow, briefs and handover.
- #120: Go 1.27.3 (GO-2026-6809) and docs vite 8.3.3.
- #122: Claude GitHub Action (plans, plan reviews, second reviews, answer-only mentions; GITHUB_TOKEN, no App).
- #125: Go patch version pinned in the Dockerfile with a build guard (PR #127; plan, plan review and second review by the Action).
- #133: repo prepared for private visibility, reverted on 2026-10-22 because the repository goes back to public.
- #134: secret scanning in CI (`secret-scan`, gitleaks v8.30.1, PR #135). One-time full-history scan of all refs: no findings.
- Release v26.10-6 (2026-10-22): Go 1.27.3 image, #112, #115.
- 2026-10-22: Claude GitHub Action removed again (#138); plans and reviews run in Claude sessions on the subscription. #128 closed as not planned. The maintainer deleted the `ANTHROPIC_API_KEY` secret and the labels `needs-plan`, `plan-review` and `second-review`.
- #124: bubblewrap and socat for the Action's sandbox. All four triggers tested on GitHub: `second-review` (#121, #123, #124), `needs-plan`, `plan-review` and `@claude` (#125).
- 2026-10-22: #129 (gh/MCP operations table in CLAUDE.md and the briefs, PR #140), #130 (`addMessage` sets UIDVALIDITY, PR #142), #131 (toolchain mismatch message, PR #141).
- 2026-10-22: #144 (maintainer OK for changes that can weaken the secret scan, PR #147).
- 2026-10-22: #145 (Debian `postgres:18` instead of `postgres:18-alpine`, PR #146). Full-text search was up to 2.3x slower on Alpine (musl). Maintainer decision: no installation ran the Alpine image, so no REINDEX upgrade step, no `breaking` label and no `./ma migrate` guard; the upgrade row was removed again in PR #149.

- 2026-10-22: correction to #145: the production install runs `postgres:18-alpine` (bundles up to 26.10-6), so the premise of PR #149 was wrong. The REINDEX row is back in `docs/guide/operations.md` (PR #151, `breaking`); #150 stays open for the release notes and the optional migrate guard.
- 2026-10-22: #152 (import files into an smtp account's `JOURNAL` folder, `./ma import ACCOUNT --journal [--label TEXT]`, migration 00029 `smtp_imports`, origin row "Imported from files: <label>"), PR #153. Plan reviewed (one blocking finding fixed: Message-ID parsed from the blob), two independent Opus reviews, both approve.
- 2026-10-22: #150 closed (release notes of 26.10-7/-8 edited by the maintainer; PR #159: `./ma migrate` rebuilds indexes of an Alpine-built database, other commands refuse until then). #156 (PR #158, profile tabs; tab bar shrink fix in PR #161). #157 (PR #160, resizable and collapsible sidebar). PR #161: screenshots regenerated, new rule: UI PRs update screenshots and docs text.

## Maintainer actions
- Add `web-e2e` to the required checks in the ruleset (the coordinator must not change rulesets).
- Sign off part 3 (look and effort): screenshots are in the session; the PR #188 body has an effort note (18 files, +1881/-755).
- Release decision.

## Manual checks for the maintainer
- #155: a real purge on a copy of the piler case.
- #168 parts 2-3: browser check of the Carbon login, accounts pages and purge modal (EN/DE, light/dark, phone).
- #186: a real passkey login and the setup screen's CodeSnippet copy button.
- #186: `docker build`, confirming no postinstall or telemetry runs.
- #181: first-run timing of `web-e2e` in CI, under 5 minutes.
- #159: upgrade a real volume created by `postgres:18-alpine` (bundle ≤ 26.10-6): `./ma status` refuses, `./ma migrate` rebuilds and records the marker, then commands work.
- #158/#160: look at the profile tabs and the sidebar in a browser (EN/DE, light/dark, 320 px and 1024 px width, touch drag, many accounts).
- #115 (optional, documented in `docs/development/architecture.md`): a real power-loss test on a disposable VM, hard links and directory fsync on Docker Desktop file shares, and a start of the distroless image with the `./data:/data` bind mount.
- #125/#127 (needs a Docker daemon): change the build tag to `golang:1.27.2-alpine` and confirm `docker build .` fails at the toolchain guard.
- #152/#153: a real piler export into a journal account (`--dry-run`, then the run); check the counts and the Origin row in the web UI (EN and DE). The invocation is in `docs/guide/importing.md` ("piler into a journaled mailbox").
- #153: time `./ma migrate` (00029 reads `message_locations` once) on a large archive.

## Environment
- pnpm via corepack is broken in cloud sessions. Use `node /root/.cache/node/corepack/v1/pnpm/12.9.1/bin/pnpm.mjs`.
