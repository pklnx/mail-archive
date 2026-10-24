# Handover

State of the work for the next session. Update before a session ends.

## Open PRs
- None besides this handover update.

## Open tickets
- #260 (bug, ready): flaky e2e "Blocked script execution in 'about:blank'". Part 1 (diagnostics, PR #262) is merged. Strong lead for part 2: the message comes from Playwright's trace snapshotter (`trace: retain-on-failure` records every test), not from app code; the test passes with `--trace off`. Part 2 also owes the `flaky-tests.md` row for the #259 rerun and fixes the run count there (16 local runs, not 13).
- #265 (bug): install the link blocker again before `load` after Back to a mail (residual in `security.md`; scope widened in a comment).
- #269 (bug): a cancelled migration can return a pool connection that still holds goose's lock (measured 10 of 10, confirmed by the #268 plan reviewer). Run goose on a dedicated, unpooled connection; regression test with signals only.

## State (2026-10-24)
- #209 is done: part 1 (PR #266) neutralizes declarative shadow roots and `srcdoc` frames as the last step on the served bytes; part 2 (PR #267, maintainer option A) blocks links from the start.
- The coordinator's decision tiers (green, yellow, red) and the merge rules are in `pklnx/dev-process` (`decisions.md`, `coordinator.md`). Apply them in every session.
- Merge rules learned 2026-10-24: never merge a head that no approving review covers (only exception: a pure base merge with the evidence posted); read every reviewer's full report and post a "Reports read" comment before merging.

## Done (2026-10-24)
- #213 (red main, PRs #223, #226), #214 (web hardening #230, migration lock #225), #215 (process hygiene #222), #211 (pklnx theme, PR #227).
- #253 (migration lock waits visibly, PR #259) and #255 (PR #256); #258 (SideNav stuck when crossing `lg`, PR #261); #260 part 1 (PR #262).
- #209 (PRs #266, #267); #268 (cancelled lock wait reports `context.Canceled`, PR #270); #271 (process rules: sub-issues by planner or coordinator, serving layer counts as risky).

## State (2026-10-23 evening)
- Release v26.10-10 is out (#200, message view scrolling).
- Release v26.10-11 is out on main e88dc92, image digest sha256:eeab5d412f2a0013d0c162f274f8ff06192a38993fe56e057f602f5b3533361f. It contains:
  - #207, #203 part 1: a same-origin mail frame without scripts, a parent-side auto-height (`frameFit.ts`), a same-origin link blocker (click/auxclick/dragstart, `composedPath`), and a Fetch Metadata guard with a navigation allowlist in `protect`.
  - #210, #203 part 2: a compact header and the conversation overlay.
  - #208, #206: standards mode for mails with a doctype.
- The maintainer checks 26.10-11 on a real iPhone after the release.

## Lessons (2026-10-23)
- Stacking PRs on an in-review PR and rebasing them with `--onto` after the squash merge worked well under deadline.
- e2e must never evaluate inside the sandboxed frame (`contentFrame`/`frame.evaluate`). It causes CI-only "Blocked script execution in 'about:blank'" failures. Read via `iframe.contentDocument` from the parent page.
- Port 18181 contention between parallel agents: wait, never kill.
- Two coordinator sessions are active on this repo. Check open issues and PRs before starting work, to avoid overlapping changes.

## Done (2026-10-23, evening)
- Release v26.10-10: #200 (message view scrolling).
- Release v26.10-11: #207 and #210 (#203 parts 1 and 2), #208 (#206). See State above.

## Done (2026-10-23)
- #182: inline `cid:` images as `data:` URLs (PR #192).
- #187: a claimed sync stays queued, with a stale bound via the claim time (PR #193).
- #190: `web-e2e` is skipped on docs-only PRs (PR #194). The maintainer added `web-e2e` to the required checks.
- Carbon parts 4a #172 (PR #191), 4b #177 (PR #195), 5 #173 (PR #197) and 6 #174 (PR #198), which completes #168.
- #196: phone quick filters and an "Edit filters" button (PR #199).
- Release v26.10-9, published 2026-10-23 and verified (checksum, ghcr tags `26.10-9`/`26.10`/`latest` on one digest).

## Done (2026-10-22/23, before the Carbon parts)
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
- 2026-10-23: #215 (process hygiene): the label `second-review` is back (marker for risky PRs that get an independent second session review; defined in `.github/labels.json`, created by `labels.yml` on merge). GitHub texts are signed `— Claude, <role>` without model names.

## Maintainer actions
- Check release 26.10-11 on a real iPhone (message view, compact header, conversation overlay, link blocking).
- Keep `audit` and `docs-audit` (`pnpm audit`, #216) off the required checks of the `main` ruleset. The required list stays test, lint, vulncheck, docker, compose, web, web-e2e and secret-scan.

## Manual checks for the maintainer
- #186: `docker build`, confirming no postinstall or telemetry runs.
- #181: first-run timing of `web-e2e` in CI, under 5 minutes. The slow runs (8.9 to 14.5 minutes) come from the apt download of `playwright install --with-deps`, not from the tests; the timeout is 20 since #238, which replaced the apt step with the headless shell and a library check (apt only as a fallback).
- #159: upgrade a real volume created by `postgres:18-alpine` (bundle <= 26.10-6): `./ma status` refuses, `./ma migrate` rebuilds and records the marker, then commands work.
- #158/#160: look at the profile tabs and the sidebar in a browser (EN/DE, light/dark, 320 px and 1024 px width, touch drag, many accounts).
- #115 (optional, documented in `docs/development/architecture.md`): a real power-loss test on a disposable VM, hard links and directory fsync on Docker Desktop file shares, and a start of the distroless image with the `./data:/data` bind mount.
- #125/#127 (needs a Docker daemon): change the build tag to `golang:1.27.2-alpine` and confirm `docker build .` fails at the toolchain guard.
- #152/#153: a real piler export into a journal account (`--dry-run`, then the run); check the counts and the Origin row in the web UI (EN and DE). The invocation is in `docs/guide/importing.md` ("piler into a journaled mailbox").
- #153: time `./ma migrate` (00029 reads `message_locations` once) on a large archive.

## Environment
- pnpm shims: every agent creates its own new shim directory (check with `ls -la` first) with a wrapper script, never a symlink, and never writes to an existing shim or binary; never `corepack enable`. Fetch with an explicit refspec, never through `FETCH_HEAD`. A refused action is reported and never retried in another form.
- pnpm via corepack is broken in cloud sessions (corepack points to a missing pnpm version). Use `node /root/.cache/node/corepack/v1/pnpm/12.9.1/bin/pnpm.mjs`.
- The e2e suite needs `DEMO_DATABASE_URL` (a Postgres the suite may write to).
- Docker Hub can answer 500 or 429 on image pulls. Since #216 CI pulls through mirror.gcr.io with Docker Hub as the fallback (`scripts/ci-docker-mirror.sh`, `scripts/ci-pull-images.sh`, buildx `buildkitd-config-inline`); the PostgreSQL services of `test` and `web-e2e` have no fallback. A pull failure that still happens: rerun only per the flaky-test rule.
