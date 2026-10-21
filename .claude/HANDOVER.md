# Handover

State of the work for the next session. Update before a session ends.

## Open PRs
- #121: #115 (durable blobstore), under review.
- Claude GitHub Action PR (`ci/claude-action`): `.github/workflows/claude.yml` with plans on `needs-plan`, plan reviews on `plan-review`, second reviews on `second-review`/migration PRs and `@claude` mentions on `ANTHROPIC_API_KEY`; adds `needs-plan`, `plan-review` and `second-review` to `.github/labels.json`. Open, waiting for review.

## Next tickets
- #116: `ready`, implementation in progress.

## Done
- #119: agent workflow and session handover, merged.
- #120: Go 1.27.3 + docs vite 8.3.3 (vulncheck and docs audit fixes), merged.
- #117: merged via #118.

## Manual checks for the maintainer
- After the Action PR is merged: install the Claude GitHub App on the repository if it is not installed yet (https://github.com/apps/claude); `@claude` mentions use its token.
- Confirm the repository secret `ANTHROPIC_API_KEY` exists (Settings → Secrets and variables → Actions).
- Test each trigger once: `needs-plan` on an issue, `plan-review` on an issue with a plan, `second-review` on a PR, `@claude` in a comment.
