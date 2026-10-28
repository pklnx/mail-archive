# Workflow

Roles: maintainer (human) decides, sets `plan` and may set `ready`; coordinator (Claude) plans, delegates, merges; implementer (one PR each), reviewer (review only) and plan-reviewer (never the planner) are defined in `.claude/agents/`; models per dev-process `models.md`. If a session does not offer a role as agent type, start a general agent with "Act as the role in `.claude/agents/<role>.md`". All runs in Claude sessions on the subscription; no Claude GitHub Action.

Language: chat with the maintainer in German; GitHub and code in English. Every GitHub text ends with `— Claude, <role>` (no model names on GitHub).

## Ticket flow
1. Issue (template). 2. Maintainer labels `plan` → `## Plan` comment (approach, files, data model, external services, tests, open questions, size), label becomes `review`. 3. Independent plan review by a plan-reviewer agent; an approving reviewer or the maintainer replaces `review` with `ready`; only `ready` issues are built.
4. Plan with several parts: one sub-issue per part, created by the planner or the coordinator when the plan is posted; title "<what> (#N, part K)", linked as GitHub sub-issue (the link needs the sub-issue's `id`, not its number). The main issue's `ready` covers them; it closes with the last part.
5. One branch + PR per part; body "Closes #<sub>." and "Part K of #N.".
6. Review: session reviewer always for app code; risky PRs (deletes, schema changes (`db-migration`), rewriting user data, changes to the serving layer (static file serving, response headers, CSP, routing)) get a second, independent session reviewer, a separate reviewer agent that does not see the first review beforehand: the coordinator adds `second-review` to every risky PR (also every migration PR) as a marker; the label triggers nothing. PRs without app code (docs, CI, test-only): one session review (Sonnet) + green required checks.
7. Merge: approving review + green required checks; squash merge pinned to the reviewed head SHA. Never auto-merge, never change rulesets or repo settings.
   - A PR that touches the secret scan's allowlist or configuration (files listed in `docs/development/process.md`, "4. Pull request") is merged only after the maintainer's explicit OK. The coordinator asks via AskUserQuestion, names the changed entries (file, rule id, path or fingerprint) and never posts secret values; the OK is noted on the PR.

## GitHub operations
Local sessions use `gh`; cloud sessions the GitHub MCP tools (via ToolSearch). Commands per operation: `.claude/github-operations.md`, read before the first GitHub write. An MCP `issue_write` labels update replaces the whole set.

Blocking in reviews only: crash, data loss/corruption, hang, wrong result in a main function, security/privacy problem, broken main flow, missing/failing test for new behaviour, violation of plan or maintainer decision. Everything else: a `followup` issue if the reviewer names a realistic near-term case, else it stays in the review.

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
Start: read `.claude/HANDOVER.md`. Follow-ups are open `followup` issues, queried when needed. Rules in `.claude/rules/` load with matching files. End: replace the state in `HANDOVER.md`, never append.

Pointers: `docs/development/process.md`, `.claude/agents/`, `.claude/github-operations.md`, [pklnx/dev-process](https://github.com/pklnx/dev-process) (coordination, models).
