# Issues, plans and releases

Work goes through four steps: **issue → plan → approval → pull request**.
Nothing is implemented before its plan is approved.

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

Before any code, the implementation plan is posted as a comment on the issue
and the issue gets the label `plan-review`. A plan covers:

- the approach and the files or components it touches,
- API, database or behavior changes (a schema change means `db-migration`),
- how it will be tested,
- open questions and decisions to make,
- the expected size (one pull request or several).

## 3. Review and approval

The maintainer reads the plan, asks questions or asks for changes in the
issue. When the plan is good, the maintainer **replaces `plan-review` with
`ready`**. Only `ready` issues are implemented.

The order of `ready` issues is chosen during implementation, with a short
reason (dependencies between issues, risk, size). Changes to an approved plan
that come up during the work are noted in the issue before the pull request.

## 4. Pull request

Each issue gets its own branch and pull request. The description says
`Closes #N`, so merging closes the issue. Labels follow the
[label table](./#pull-requests-and-labels). CI must be green; the maintainer
reviews and enables auto-merge.

## Labels

| Label | Set by | Meaning |
|---|---|---|
| `plan-review` | implementer | A plan is posted and waits for review. |
| `ready` | maintainer | The plan is approved; the issue may be implemented. |
| `feature`, `bug`, `docs`, `chore` | template or by hand | Kind of change. |

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
