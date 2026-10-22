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
| #153 reviews | `internal/archive/journal_import.go:130-133`: a second copy of the same bytes in one batch counts as present even when the first copy is left out as journaled or deleted by retention. Counts only. | open |
| #153 second review | `docs/guide/importing.md:159-162`: "nothing is stored twice inside the account" overstates it; messages without Message-ID and the delivery race can give two copies. | open |
| #153 second review | `cmd/mail-archive/import.go:184-190`: without `--label`, a `--from` path over 200 characters fails with a `--label:` error the user cannot place; truncate the default or name `--from`. | open |
| #153 second review | `cmd/mail-archive/import.go:304-305`: a removed smtp account without `--journal` gets the hint "add --journal", which then says removed. Cosmetic. | open |
| #153 second review | Optional: `CHECK (fill_id IS NULL OR import_id IS NULL)` on `message_locations`. | open |
| #153 second review | `docs/guide/operations.md` 00029 row: deliveries wait while the ALTER holds its lock; suggest stopping the web server on very large archives, like the 00027 row. | open |
| #153 first review | `TestFillCancelAndResume` (`internal/web/fills_test.go:292-304`) races: the fill status turns ok before `data.fill_done` is written (`internal/archive/fillrunner.go:235`) and the test polls with `time.Sleep`. Wait for the audit entry (e.g. via `AfterRun`). Failed once locally, not in CI. | open |
| #158 review | Move `RetentionPage` onto the shared `Tabs` component. | open |
| #160 review | `sidebarWidth` aria text is not locale-formatted ("13.25 rem" in German); a 28 rem sidebar at 1024 px leaves the reader narrow. | open |
| #161 review | `docs/guide/web-ui.md` does not name the width bounds (12–28 rem) or Home/End/Enter on the handle. | open |
| #176 plan | Attester (web): `TryLockAttest` keeps an idle pooled connection for the whole attestation, beside its snapshot connection. Take the writer lock on the snapshot connection instead (one long-held connection fewer). Bounded: `WithAttestSnapshot` waits for the fence at most 30 s. | open |
