# Issues, plans and releases

Work goes through four steps: **issue → plan → approval → pull request**.
Nothing is implemented before its plan is approved. The maintainer decides
what gets planned and what gets built; labels carry these decisions.

## 1. Issue

Every change starts as a GitHub issue:

- **Bug report** and **Feature request** templates ask for what is needed
  (steps and version for bugs, problem and "done when" for features).
- Findings from test reports become issues, one per topic, with a link to
  the report.
- Security problems go to the
  [private vulnerability report](https://github.com/pklnx/mail-archive/security/advisories/new),
  not to a public issue.

## 2. Plan

The maintainer labels an issue **`plan`** to ask for an implementation plan
from a Claude session, or **`needs-plan`** to have the Claude GitHub Action
write it. The plan is posted as a `## Plan` comment on the issue, and the label
changes to **`review`**. A plan covers:

- the approach and the files or components it touches,
- API, database or behavior changes (a schema change means `db-migration`),
- how it will be tested,
- open questions and decisions to make,
- the expected size (one pull request or several).

## 3. Review and approval

An independent plan-reviewer agent (never the planner) reviews the plan and
the maintainer may read it too; both can ask questions or for changes in the
issue, and the plan is updated in place. The label **`plan-review`** has the
Claude GitHub Action review it: it posts `## Independent plan review` with a
verdict, and removes `plan-review` (keeping `review` when it asks for
changes). When the plan is good, the reviewer or the maintainer replaces
`review` with **`ready`**. Only `ready` issues are implemented.

A plan with several parts gets one sub-issue per part, titled
`<what> (#N, part K)`. The `ready` on the main issue covers all parts, and the
main issue closes with the last part.

The order of `ready` issues is chosen during implementation, with a short
reason (dependencies between issues, risk, size). Changes to an approved plan
that come up during the work are noted in the issue before the pull request.

## 4. Pull request

Each issue (or part of a plan) gets its own branch and pull request. The
description says `Closes #N` (and "Part K of #N" for a part), so merging closes
the issue. Labels follow the [label table](./#pull-requests-and-labels). After
an approving review and green required checks, the coordinator merges with
squash; there is no auto-merge.

Risky pull requests (deletes, schema changes, rewriting user data) get a
second review from the Claude GitHub Action: the coordinator adds the label
**`second-review`** to each of them. Pull requests that change
`internal/store/migrations` get it automatically when they are opened,
reopened or marked ready for review, and on the push that brings the first
migration change into a pull request (not on other pushes). The Action only
comments; it never pushes.

## Claude GitHub Action

`.github/workflows/claude.yml` runs
[claude-code-action](https://github.com/anthropics/claude-code-action) on the
repository secret `ANTHROPIC_API_KEY`:

| Trigger | Result |
|---|---|
| `needs-plan` on an issue | `## Plan` comment; `needs-plan` becomes `review`. |
| `plan-review` on an issue | `## Independent plan review` with verdict; approve sets `ready`. |
| `second-review` or `db-migration` on a PR, or a PR with a migration (opened, ready, or first migration push) | One `## Review` comment. |
| `@claude` in a comment or review | An answer; it never edits or pushes (implementation runs in Claude sessions). |

Only users with write access start it (the Action checks the user who added
the label or wrote the comment; `@claude` also needs the author association
owner, member or collaborator). Planner and reviewers run with a read-only
token and read tools only; a separate step posts their comment and sets the
labels. All jobs use the workflow's `GITHUB_TOKEN` with per-job permissions;
no GitHub App is needed. A missing `ANTHROPIC_API_KEY` fails the run with a
clear error.

Labels set with a user's token (the maintainer's, also through Claude
sessions) start the workflow; labels set with `GITHUB_TOKEN` (the labeler
workflow, this workflow) start nothing, so there are no loops. Its comments
come from `github-actions[bot]`, with at-mentions broken so they notify
nobody. A failed run (no result, turn limit, timeout) posts a short comment
and removes its trigger label; adding the label again retries. Issue, pull
request and comment content is data to the Action, never instructions. Its texts end with `— Claude (Action), <role>`.
After changes to the workflow, the coordinator tests each trigger once.

## Labels

| Label | Set by | Meaning |
|---|---|---|
| `plan` | maintainer | Please write an implementation plan (Claude session). |
| `needs-plan` | maintainer | Please write an implementation plan (Claude GitHub Action). |
| `review` | planner | The plan is posted and waits for review. |
| `plan-review` | maintainer or coordinator | Claude GitHub Action reviews the plan. |
| `ready` | plan reviewer or maintainer | The plan is approved; the issue may be implemented. |
| `second-review` | coordinator or maintainer | Claude GitHub Action reviews the pull request as second reviewer. |
| `db-migration` | automatically | Schema change (see Releases). |
| `breaking` | by hand | Manual upgrade steps (see Releases). |
| `feature`, `bug`, `docs`, `chore` | template or by hand | Kind of change. |

Label changes do not notify anyone by themselves: after setting `plan` or
`ready`, say so in the chat with the implementer (for example "plan #18" or
"ready: #18, #19").

## Releases

Versions follow [semantic versioning](https://semver.org/). Before 1.0, the
minor version goes up for new features and breaking changes, the patch
version for fixes.

To release:

1. Check that CI on `main` is green.
2. On GitHub: **Releases → Draft a new release → Choose a tag**, type the new
   version (for example `v0.2.0`) and create it on `main`.
3. Click **Generate release notes**. Pull requests are grouped by label
   (breaking changes, database migrations, features, fixes, documentation,
   dependencies, maintenance; see `.github/release.yml`). Add upgrade notes
   for `breaking` and `db-migration` entries.
4. Publish.

`./ma --version` and `mail-archive --version` show the version, taken from
`git describe` when the image or binary is built: a release tag such as
`v0.2.0`, or `v0.2.0-3-gabc1234` for commits after it.

To run a release instead of the latest `main`:

```sh
git fetch --tags
git checkout v0.2.0
./ma migrate
docker compose up -d web
```
