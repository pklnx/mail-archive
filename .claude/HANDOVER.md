# Handover

State of the work for the next session. Update before a session ends.

## Open PRs
- #121: #115 (durable blobstore), under review.
- Claude GitHub Action PR (`ci/claude-action`): `.github/workflows/claude.yml` with plans on `needs-plan`, plan reviews on `plan-review`, second reviews on `second-review`/migration PRs (also the first migration push) and answer-only `@claude` mentions on `ANTHROPIC_API_KEY` (GITHUB_TOKEN, no App); adds `needs-plan`, `plan-review` and `second-review` to `.github/labels.json`. Reviewed (approve on 7d56f28 with follow-ups); follow-ups and the maintainer's no-manual-steps changes pushed, waiting for re-review.

## Next tickets
- #116: `ready`, implementation in progress.

## Done
- #119: agent workflow and session handover, merged.
- #120: Go 1.27.3 + docs vite 8.3.3 (vulncheck and docs audit fixes), merged.
- #117: merged via #118.

## Manual checks for the maintainer
- None.

## Coordinator after the Action PR is merged
- Test each trigger once: `needs-plan` on an issue, `plan-review` on an issue with a plan, `second-review` on a PR, `@claude` in a comment. A missing `ANTHROPIC_API_KEY` shows as a clear error in the run.
