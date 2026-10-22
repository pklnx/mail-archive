# Follow-ups

Non-blocking findings from reviews that are not fixed in their PR. Read at session start; remove or mark done when handled.

| Source | Follow-up | Status |
|---|---|---|
| #120 review | Pin the Go patch version in the Dockerfile (`golang:1.27.3-alpine`) so security toolchain bumps reach the image without relying on a refreshed floating tag. | done (#125, PR #127) |
| #119 review | CLAUDE.md and the briefs still name `gh` commands; cloud sessions map them to GitHub MCP tools via a one-line note only. | done (#129, PR #140) |
| #122 re-review 2 | `mention`: `download_job_log` saves logs under `/home/runner/work/_temp/github-ci-logs/`, which the `_temp` deny rule makes unreadable. Deny only the sensitive parts of `_temp` if log reading is wanted. | obsolete: Claude Action removed (#128 closed) |
| #122 re-review 2 | The second reviewer may call `gh pr checks --watch` and idle until the job timeout (runner minutes, no API credit). Deny `Bash(gh pr checks --watch:*)`. | obsolete: Claude Action removed (#128 closed) |
| #124 Action review | If the sandbox fails to start, the reviewer posts a "changes requested" comment instead of failing the job and removing the label. Fail the run when no tool could run. | obsolete: Claude Action removed (#128 closed) |
| #124 Action review | `apt-get update` adds time and a mirror dependency to every Claude job; skip the install when `bwrap` and `socat` exist, or cache the packages. | obsolete: Claude Action removed (#128 closed) |
| #123 implementer | The `addMessage` test helper leaves the folder's UIDVALIDITY unset, so its locations show `superseded: true`; harmless test-data artifact. | done (#130, PR #142) |
| #127 Action review | `scripts/check-go-toolchain.sh` says "Dockerfile builds with …" also when run on a host (CI `lint`, a developer machine). Reword to "Go toolchain is …". | done (#131, PR #141) |
| #127 PR | Manual check with a Docker daemon: change the build tag to `golang:1.27.2-alpine` and confirm `docker build .` fails at the guard. | manual check, see HANDOVER |
| #135 Action review | `.gitleaksignore` and `.gitleaks.toml` are read from the PR's own checkout, so a PR could add a secret together with its allowlist entry. Require an explicit maintainer OK for changes to either file (CODEOWNERS or a reviewer-brief line). | done (#144, PR #147) |
| #135 Action review | The Action reviewer could not read checks (`gh pr checks`: "Resource not accessible by integration" via GraphQL). Fix with #128. | obsolete: Claude Action removed |
| #140 review | `.claude/briefs/implementer.md`: PR step read as if title and body were set after the label. | done (handover PR after #142) |
