# Workflow

Roles: maintainer (human) decides, sets `plan` and may set `ready`; coordinator (Claude, Opus) plans the work, delegates to subagents, merges; implementers (Opus for features, Sonnet for mechanical work) build one PR each; reviewers (Opus for risky/large, Sonnet for small) review only; an independent plan-reviewer agent (never the planner) reviews plans. The Claude GitHub Action writes plans (`needs-plan`) and is second reviewer on risky PRs (once the Claude GitHub Action is set up; until then a second local reviewer takes this role).

Language: chat with the maintainer in German; everything on GitHub and in code in English. Every GitHub text ends with `— Claude (<model>), <role>`.

Cloud sessions have no `gh` CLI: the GitHub MCP tools replace the `gh` commands, and the coordinator is woken by the PR activity subscription instead of a background `gh pr checks --watch`.

## Ticket flow
1. Issue (template). 2. Maintainer labels `plan`/`needs-plan` → `## Plan` comment (approach, files, data model, external services, tests, open questions, size) + independent plan review (label `review`). 3. An approving plan-reviewer agent or the maintainer replaces `review` with `ready`; only `ready` issues are built.
4. Plan with several parts: one sub-issue per part, title "<what> (#N, part K)", linked as GitHub sub-issue (`gh api -X POST repos/pklnx/mail-archive/issues/N/sub_issues -F sub_issue_id=<issue .id>`). The main issue's `ready` covers them; it closes with the last part.
5. One branch + PR per part; body "Closes #<sub>." and "Part K of #N.".
6. Review: local reviewer always for app code; Action review as second reviewer on risky PRs (deletes, schema changes (`db-migration`), rewriting user data). PRs without app code (docs, CI, test-only): Action review + green required checks only.
7. Merge: approving review + green required checks; `gh pr merge <n> --squash --match-head-commit <sha>`. Never auto-merge, never change rulesets or repo settings.

Blocking in reviews only: crash, data loss/corruption, hang, wrong result in a main function, security/privacy problem, broken main flow, missing/failing test for new behaviour, violation of plan or maintainer decision. Everything else is a follow-up.

## Rules
- Tests never touch real user data, real credentials or the network; secrets only in the secret store, never in chat or files.
- Every behaviour change has tests; a bug fix has a regression test that fails without the fix. Tests wait for signals, never for time (no sleeps/fixed delays).
- Locally run only the tests of what changed; CI runs everything.
- Agents never watch CI: push, open the PR, report. The coordinator gets woken per PR by a background `gh pr checks <n> --watch --interval 120 >/dev/null 2>&1; gh pr checks <n>` (wait until the checks appear first).
- ≤3 tickets in parallel, ≤2 PRs in CI at a time.
- A flaky-test rerun gets a row in `docs/development/flaky-tests.md`; a test listed twice is fixed right away.
- After 3 review rounds that keep finding new holes in a heuristic: stop patching, rebuild it on a principle or give the maintainer options.
- Ask instead of guessing on product decisions (via AskUserQuestion, recommendation first).

## Session handover
At session start read `.claude/HANDOVER.md` and `.claude/FOLLOWUPS.md`. Update `HANDOVER.md` before a session ends.

Pointers: [`docs/development/process.md`](docs/development/process.md), [`.claude/briefs/implementer.md`](.claude/briefs/implementer.md), [`.claude/briefs/reviewer.md`](.claude/briefs/reviewer.md).
