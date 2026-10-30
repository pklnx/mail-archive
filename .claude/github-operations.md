# GitHub operations

Read before the first GitHub write (CLAUDE.md, "GitHub operations"). Moved unchanged from `CLAUDE.md` (#291).

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
| Create a milestone | `gh api -X POST repos/pklnx/mail-archive/milestones -f title=1.3.0 -f description='<goal>'` | the GitHub MCP server has no tool for it: `gh api` where available, else the maintainer creates it |
| Rename or close a milestone | `gh api -X PATCH repos/pklnx/mail-archive/milestones/<number> -f title=<X.Y.Z>` / `-f state=closed` | as for create |
| List milestones (with numbers) | `gh api 'repos/pklnx/mail-archive/milestones?state=all'` | `gh api` where available; else read an issue's `milestone` via `mcp__github__issue_read` method `get` |
| Assign an issue or PR to a milestone | `gh issue edit <n> --milestone 1.3.0` (`gh pr edit` for a PR) | `mcp__github__issue_write` method `update` with `milestone`: the milestone **number**, not its title |
