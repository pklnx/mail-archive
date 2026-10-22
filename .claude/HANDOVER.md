# Handover

State of the work for the next session. Update before a session ends.

## Open PRs
- None besides this handover update.

## Next tickets
- #150 (open, maintainer): add the REINDEX step to the published 26.10-7 release notes; decide whether `./ma migrate` should detect a volume created by the Alpine image.
- Release with #152 (PR #153, `db-migration` 00029): run the Release workflow when the maintainer wants it; the production install needs it for its piler re-import.

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

## Manual checks for the maintainer
- #115 (optional, documented in `docs/development/architecture.md`): a real power-loss test on a disposable VM, hard links and directory fsync on Docker Desktop file shares, and a start of the distroless image with the `./data:/data` bind mount.
- #125/#127 (needs a Docker daemon): change the build tag to `golang:1.27.2-alpine` and confirm `docker build .` fails at the toolchain guard.
- #152/#153: a real piler export into a journal account (`--dry-run`, then the run); check the counts and the Origin row in the web UI (EN and DE). The invocation is in `docs/guide/importing.md` ("piler into a journaled mailbox").
- #153: time `./ma migrate` (00029 reads `message_locations` once) on a large archive.
