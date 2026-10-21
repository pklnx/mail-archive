# Handover

State of the work for the next session. Update before a session ends.

## Open PRs
- None besides this handover update.

## Next tickets
- None `ready`. Open follow-ups are in `FOLLOWUPS.md`.

## Done (2026-10-21)
- #115: durable blob publication and removal, merged via #121 (local Opus review + Action second review).
- #116: shared-message isolation and retention regression tests, merged via #123 (Action review).
- #117: root demo artifact removed, merged via #118.
- #119: agent workflow, briefs and handover.
- #120: Go 1.27.3 (GO-2026-6809) and docs vite 8.3.3.
- #122: Claude GitHub Action (plans, plan reviews, second reviews, answer-only mentions; GITHUB_TOKEN, no App).
- #125: Go patch version pinned in the Dockerfile with a build guard (PR #127; plan, plan review and second review by the Action).
- #124: bubblewrap and socat for the Action's sandbox. All four triggers tested on GitHub: `second-review` (#121, #123, #124), `needs-plan`, `plan-review` and `@claude` (#125).

## Manual checks for the maintainer
- #115 (optional, documented in `docs/development/architecture.md`): a real power-loss test on a disposable VM, hard links and directory fsync on Docker Desktop file shares, and a start of the distroless image with the `./data:/data` bind mount.
