# Handover

State of the work for the next session. Update before a session ends.

## Open PRs
- None besides this handover update.

## Next tickets
- Not planned yet, from review follow-ups: #129 (tool-neutral CLAUDE.md/briefs), #130 (`addMessage` test helper UIDVALIDITY), #131 (`check-go-toolchain.sh` message). Label `plan` to have a Claude session plan them.

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
- 2026-10-22: Claude GitHub Action removed again; plans and reviews run in Claude sessions on the subscription. #128 closed as not planned.
- #124: bubblewrap and socat for the Action's sandbox. All four triggers tested on GitHub: `second-review` (#121, #123, #124), `needs-plan`, `plan-review` and `@claude` (#125).

## Manual checks for the maintainer
- Optional cleanup after the Action removal: delete the repository secret `ANTHROPIC_API_KEY` and the labels `needs-plan`, `plan-review` and `second-review` (the label sync does not delete labels).
- #115 (optional, documented in `docs/development/architecture.md`): a real power-loss test on a disposable VM, hard links and directory fsync on Docker Desktop file shares, and a start of the distroless image with the `./data:/data` bind mount.
- #125/#127 (needs a Docker daemon): change the build tag to `golang:1.27.2-alpine` and confirm `docker build .` fails at the toolchain guard.
