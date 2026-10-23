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
from a Claude session. The plan is posted as a `## Plan` comment on the issue, and the label
changes to **`review`**. A plan covers:

- the approach and the files or components it touches,
- API, database or behavior changes (a schema change means `db-migration`),
- how it will be tested,
- open questions and decisions to make,
- the expected size (one pull request or several).

## 3. Review and approval

An independent plan-reviewer agent (never the planner) reviews the plan and
the maintainer may read it too; both can ask questions or for changes in the
issue, and the plan is updated in place. When the plan is good, the reviewer or the maintainer replaces
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
squash; there is no auto-merge. The `audit` and `docs-audit` checks
(`pnpm audit`) are not required: a red audit does not block a merge. The
coordinator reads its log; a registry error needs nothing, a real advisory gets
a Dependabot pull request or a follow-up issue.

Every pull request with app code gets a session review. Risky pull requests
(deletes, schema changes, rewriting user data) also get a second, independent
session review: a separate reviewer agent that does not see the first review
beforehand. The coordinator adds the label **`second-review`** to each of them,
including every pull request that changes `internal/store/migrations`; the
label is a marker and triggers nothing. Pull requests without app code (docs,
CI, test-only) need one session review and green required checks.

Pull requests that can weaken the secret scan are merged only after the
maintainer's explicit OK, noted on the pull request. This covers
`.gitleaksignore`, `.gitleaks.toml` and files it extends, a new
`gitleaks:allow` comment, `.github/workflows/secrets.yml`,
`scripts/secret-scan-*` and the gitleaks variables and targets in the
`Makefile`. The reviewer flags any such change as blocking until the OK is
there. The OK names the changed entries, never secret values.

Go toolchain bumps change the `toolchain` line in `go.mod` and the `golang` tag
in the `Dockerfile` together, in one pull request: the image build fails when
they differ (`scripts/check-go-toolchain.sh`), also on a Dependabot bump of
only one of them.

## Labels

| Label | Set by | Meaning |
|---|---|---|
| `plan` | maintainer | Please write an implementation plan (Claude session). |
| `review` | planner | The plan is posted and waits for review. |
| `ready` | plan reviewer or maintainer | The plan is approved; the issue may be implemented. |
| `second-review` | coordinator or maintainer | Risky pull request: needs a second, independent session review (a marker; it triggers nothing). |
| `db-migration` | automatically | Schema change; makes at least a minor release (see Releases). |
| `breaking` | by hand | Manual upgrade steps; makes a major release (see Releases). |
| `feature`, `bug`, `docs`, `chore` | template or by hand | Kind of change; `feature` makes at least a minor release. |

Label changes do not notify anyone by themselves: after setting `plan` or
`ready`, say so in the chat with the implementer (for example "plan #18" or
"ready: #18, #19").

## Releases

Releases are published from `main` by hand: **Actions → Release → Run
workflow** (`.github/workflows/release.yml`), after CI on `main` is green. A
dry run builds everything without pushing an image or creating a release.

The version is semantic, `X.Y.Z` (`scripts/next-version.sh`). The labels of
the pull requests merged into `main` since the last release decide it, not
the labels of their issues:

| Pull requests in the release | Release | Example after `1.4.2` |
|---|---|---|
| Any `breaking` | Major | `2.0.0` |
| Else any `feature` or `db-migration` | Minor | `1.5.0` |
| Else (bug fixes, docs, maintenance, dependencies, no label) | Patch | `1.4.3` |

The first semver release is `1.0.0`, whatever the labels; its range starts at
the last `vYY.MM-N` tag (`v26.10-11`). The old `vYY.MM-N` tags and images stay
and are never renamed. The workflow lists the first-parent commits since the
last release and asks the GitHub API for each commit's pull request. A dry
run shows the proposed version, a table of the pull requests with their
labels and their effect, and the head of the release notes in the run
summary; a pull request without a kind label (`breaking`, `feature`, `bug`,
`chore`, `docs`, `dependencies`) is marked "no kind label", which is a
warning, not a block. The run fails when:

- nothing was merged since the last release, also with the `version` input;
- a commit has no merged pull request (a direct push): check it, then set the
  `version` input;
- the `version` input is not `X.Y.Z` (no `v`, no leading zeros, no suffix) or
  not greater than the last version (for the first release: at least
  `1.0.0`). It may be lower than the proposal (the run then shows a
  warning);
- the tag `vX.Y.Z` or its release exists already;
- it runs on another branch than `main`.

The optional `version` input overrides the proposal. Use it to correct the
bump, for example when a `feature` or `breaking` pull request was reverted
within the range: the reverted pull request still counts.

The workflow builds the image for `linux/amd64` and `linux/arm64`, pushes it
to `ghcr.io/pklnx/mail-archive` with the tags `X.Y.Z`, `X.Y`, `X` and `latest`,
creates the tag `vX.Y.Z` and a GitHub release with `mail-archive.tar.gz` (the
Docker Compose bundle, `scripts/release-bundle.sh`) and `SHA256SUMS`. The notes
start at the same tag as the range and are grouped by label (breaking changes,
database migrations, features, fixes, documentation, dependencies,
maintenance; see `.github/release.yml`). A pull request without a kind label
lands under "Maintenance", so a risky pull request needs a kind label in
addition to `second-review`.

If the run fails in or after "Build and push" (in "Create the release"), the
image tags `X.Y.Z`, `X.Y`, `X` and `latest` may already point to the new
build (a push that failed partway may have moved only some of them), but
there is no release. To finish the release:

1. Fix the cause.
2. Delete the draft release `vX.Y.Z` if the Releases page shows one (the
   check "Tag and release do not exist yet" stops at it).
3. If the tag `vX.Y.Z` exists without a release, delete the tag too (it
   points to this run's commit). Do not follow the run's advice to set the
   `version` input: that ships the next version and leaves the stray tag.
4. Use **Re-run failed jobs** on the same run. It uses the same commit and
   inputs, overwrites the four image tags with a build of the same commit and
   creates the release. It computes the same version only while the labels
   of the pull requests in the range are unchanged (they are read from the
   API again): compare the version in the re-run's summary with the first
   attempt.

To abandon the release instead, point the floating tags back to the previous
release by hand and delete the new package version (package settings on
GitHub, "Manage versions"). Moving a tag needs a `docker login ghcr.io` with
a token that has `write:packages`:

```sh
docker buildx imagetools create -t ghcr.io/pklnx/mail-archive:latest ghcr.io/pklnx/mail-archive:26.10-11
```

For 1.0.0, move `latest` back to `26.10-11`, then delete the package version
that carries `1.0.0`; that removes `1.0.0`, `1.0` and `1`, which did not exist
before. For a later release, also move `X` and `X.Y` back to the previous
release if it has them (abandoning `1.3.0` after `1.2.4`: `latest` and `1` to
`1.2.4`; `1.3` goes with the package version).

The workflow writes an upgrade paragraph into every release's notes
(`scripts/release-notes.sh`). It links the
[Upgrades guide](../guide/operations#upgrades), and the generated notes list
the `breaking` and `db-migration` pull requests first. Add manual upgrade notes
only when a change needs more than these generic steps. The first semver
release (no `vX.Y.Z` tag before it) also gets a paragraph for automatic image
updaters: the tag `26.10` stays on `26.10-11`, and tools should switch to an
exact version, `1.0` or `1`.

`./ma --version` and `mail-archive --version` show the version. The release
image shows `X.Y.Z` (for example `1.0.0`). A build from a clone shows
`git describe`: the tag `v1.0.0`, or `v1.0.0-3-gabc1234` for commits after
it.

To run a release, follow [Upgrades](../guide/operations#upgrades), which uses
the release bundle. From a clone instead of the latest `main`:

```sh
git fetch --tags
git checkout v1.0.0
./ma migrate
docker compose up -d web
```
