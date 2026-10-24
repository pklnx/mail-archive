# Workflow

Roles: maintainer (human) decides, sets `plan` and may set `ready`; coordinator (Claude, Opus) plans the work, delegates to subagents, merges; implementers (Opus for features, Sonnet for mechanical work) build one PR each; reviewers (Opus for risky/large, Sonnet for small) review only; an independent plan-reviewer agent (never the planner) reviews plans. Everything runs in Claude sessions on the subscription; there is no Claude GitHub Action.

Language: chat with the maintainer in German; everything on GitHub and in code in English. Every GitHub text ends with `— Claude, <role>` (no model names on GitHub).

Steps are written as operations; the commands for local sessions (`gh`) and cloud sessions (GitHub MCP tools, no `gh` CLI) are in the table under [GitHub operations](#github-operations).

## Ticket flow
1. Issue (template). 2. Maintainer labels `plan` → `## Plan` comment (approach, files, data model, external services, tests, open questions, size), label becomes `review`. 3. Independent plan review by a plan-reviewer agent; an approving reviewer or the maintainer replaces `review` with `ready`; only `ready` issues are built.
4. Plan with several parts: one sub-issue per part, created by the planner or the coordinator when the plan is posted; title "<what> (#N, part K)", linked as GitHub sub-issue (see GitHub operations; the link needs the sub-issue's `id`, not its number). The main issue's `ready` covers them; it closes with the last part.
5. One branch + PR per part; body "Closes #<sub>." and "Part K of #N.".
6. Review: session reviewer always for app code; risky PRs (deletes, schema changes (`db-migration`), rewriting user data, changes to the serving layer (static file serving, response headers, CSP, routing)) get a second, independent session reviewer, a separate reviewer agent that does not see the first review beforehand: the coordinator adds `second-review` to every risky PR (also every migration PR) as a marker; the label triggers nothing. PRs without app code (docs, CI, test-only): one session review (Sonnet) + green required checks.
7. Merge: approving review + green required checks; squash merge pinned to the reviewed head SHA (see GitHub operations). Never auto-merge, never change rulesets or repo settings.
   - A PR that touches the secret scan's allowlist or configuration (`.gitleaksignore`, `.gitleaks.toml` and files it extends, a new `gitleaks:allow` comment, `.github/workflows/secrets.yml`, `scripts/secret-scan-*`, the Makefile's gitleaks variables and targets) is merged only after the maintainer's explicit OK. The coordinator asks via AskUserQuestion, names the changed entries (file, rule id, path or fingerprint) and never posts secret values; the OK is noted on the PR.

## GitHub operations
Local sessions use `gh`; cloud sessions have no `gh` and use the GitHub MCP tools (load them via ToolSearch). The same operation, the same rules.

| Operation | Local (`gh`) | Cloud session (MCP / session tools) |
|---|---|---|
| Read an issue and its comments | `gh issue view <n> --comments` | `mcp__github__issue_read` method `get`, then `get_comments` |
| Comment on an issue or PR | `gh issue comment` / `gh pr comment` | `mcp__github__add_issue_comment` (PR number as `issue_number`) |
| Change labels | `gh issue edit --add-label/--remove-label` | `mcp__github__issue_write` method `update` with `labels`: **replaces the whole set**, so read the current labels first (`issue_read` method `get_labels`) |
| Create a sub-issue and link it | `gh issue create`, then `gh api -X POST repos/pklnx/mail-archive/issues/N/sub_issues -F sub_issue_id=<issue .id>` | `mcp__github__issue_write` method `create` with `parent_issue_number: N`; for an existing issue `mcp__github__sub_issue_write` method `add` with `sub_issue_id` (the issue's id, not its number) |
| Open a PR with a label | `gh pr create --label <l>` | `mcp__github__create_pull_request` (cannot set labels), then `mcp__github__issue_write` method `update` with `labels` on the PR number |
| Read a PR and its diff | `gh pr view`, `gh pr diff` | `mcp__github__pull_request_read` method `get`, `get_diff` (`get_files` for the file list) |
| Read check status once | `gh pr checks <n>` | `mcp__github__pull_request_read` method `get_check_runs` (plus `get_status` for commit statuses) |
| Coordinator waits for CI (wait until the checks appear first, in both columns) | background `gh pr checks <n> --watch --interval 120 >/dev/null 2>&1; gh pr checks <n>` | `mcp__claude-code-remote__subscribe_pr_activity` for the PR wakes the coordinator; optionally one `mcp__claude-code-remote__send_later` check-in as a safety net that reads `get_check_runs` (a suggestion, not a rule); `unsubscribe_pr_activity` after merge or close |
| Merge | `gh pr merge <n> --squash --match-head-commit <sha>` | `mcp__github__merge_pull_request` with `merge_method: squash`, `expectedHeadSha: <sha>` |

Blocking in reviews only: crash, data loss/corruption, hang, wrong result in a main function, security/privacy problem, broken main flow, missing/failing test for new behaviour, violation of plan or maintainer decision. Everything else is a follow-up.

## Rules
- Tests never touch real user data, real credentials or the network; secrets only in the secret store, never in chat or files.
- Every behaviour change has tests; a bug fix has a regression test that fails without the fix. Tests wait for signals, never for time (no sleeps/fixed delays).
- Locally run only the tests of what changed; CI runs everything.
- A PR that changes the web UI updates the affected docs screenshots (`make docs-screenshots`) and the docs text that describes the changed UI in the same PR.
- Agents never watch CI: push, open the PR, report. The coordinator gets woken per PR when the checks finish (wait until the checks appear first; see GitHub operations).
- ≤3 tickets in parallel, ≤2 PRs in CI at a time.
- A flaky-test rerun gets a row in `docs/development/flaky-tests.md`; a test listed twice is fixed right away.
- After 3 review rounds that keep finding new holes in a heuristic: stop patching, rebuild it on a principle or give the maintainer options.
- Ask instead of guessing on product decisions (via AskUserQuestion, recommendation first).

## Session handover
At session start read `.claude/HANDOVER.md` and `.claude/FOLLOWUPS.md`. Update `HANDOVER.md` before a session ends.

Pointers: [`docs/development/process.md`](docs/development/process.md), [`.claude/briefs/implementer.md`](.claude/briefs/implementer.md), [`.claude/briefs/reviewer.md`](.claude/briefs/reviewer.md).

Coordination rules and model allocation: [pklnx/dev-process](https://github.com/pklnx/dev-process).
