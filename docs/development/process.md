# Issues, plans and releases

Work goes through four steps: **issue → plan → approval → pull request**.
Nothing is implemented before its plan is approved. The coordinator picks
what gets planned next; an approved plan decides what gets built. The
maintainer decides new goals, milestone scope, releases and anything that
widens access or weakens security or privacy; UI wording, technical
trade-offs and small product details inside an approved goal are the
coordinator's ([`CLAUDE.md`](https://github.com/pklnx/mail-archive/blob/main/CLAUDE.md),
"Coordinator autonomy"). Labels carry these decisions.

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

The coordinator (or the maintainer) labels an issue **`plan`** when it picks
it next from the backlog. A Claude session writes the plan as a `## Plan`
comment on the issue, and the label changes to **`review`**. A plan covers:

- the approach and the files or components it touches,
- API, database or behavior changes (a schema change means `db-migration`),
- how it will be tested,
- open questions and decisions to make,
- the expected size (one pull request or several).

## 3. Review and approval

An independent plan-reviewer agent (never the planner) reviews the plan; the
maintainer may read it too. Both ask questions or for changes in the issue,
and the plan is updated in place. When it is good, the reviewer or the
maintainer replaces `review` with **`ready`**. Only `ready` issues are
implemented.

A plan with several parts gets one sub-issue per part, titled
`<what> (#N, part K)`, created by the planner or the coordinator when the plan
is posted. The `ready` on the main issue covers all parts, and the
main issue closes with the last part.

The order of `ready` issues is chosen during implementation, with a short
reason (dependencies between issues, risk, size). Changes to an approved plan
that come up during the work are noted in the issue before the pull request.

## 4. Pull request

Each issue (or part of a plan) gets its own branch and pull request. The
description says `Closes #N` (and "Part K of #N" for a part), so merging closes
the issue. Labels follow the [label table](#labels). After an approving
review and green required checks, the coordinator merges with squash; there
is no auto-merge. The `audit` and `docs-audit` checks are not required; what
a red one means is in [Dependencies](./#dependencies).

Every pull request with app code gets a session review. Risky pull requests
(deletes, schema changes, rewriting user data, changes to the serving layer:
static file serving, response headers, CSP, routing) also get a second, independent
session review by a separate reviewer agent that does not see the first review
beforehand. The coordinator labels each of them **`second-review`**, every
pull request that changes `internal/store/migrations` included; the label is
a marker and triggers nothing. Pull requests without app code (docs,
CI, test-only) need one session review and green required checks.

Pull requests that can weaken the secret scan are merged only after the
maintainer's explicit OK, noted on the pull request. This covers
`.gitleaksignore`, `.gitleaks.toml` and files it extends, a new
`gitleaks:allow` comment, `.github/workflows/secrets.yml`,
`scripts/secret-scan-*` and the gitleaks variables and targets in the
`Makefile`. The reviewer flags any such change as blocking until the OK is
there. The OK names the changed entries, never secret values.

A non-blocking review finding becomes a follow-up issue, labelled
**`followup`**, only when the reviewer names a realistic case likely to occur
soon. The coordinator creates it after the merge, and its body names the pull
request or review it comes from. Every other non-blocking remark stays in the
review and is done with it.

What CI cannot check (a real device, Safari, Firefox, a screen reader) the
pull request lists under "Open manual checks". After the merge the coordinator
appends these items to the open round of manual device checks: an issue titled
"Manual device checks, round N" and labelled **`device-checks`**, one open at a
time. The maintainer ticks what passed; a failed check gets a bug issue,
linked on its item. When the maintainer has gone through a round, the
coordinator closes it and opens the next one with every item still open.

Go toolchain bumps change the `toolchain` line in `go.mod` and the `golang` tag
in the `Dockerfile` together, in one pull request: the image build fails when
they differ (`scripts/check-go-toolchain.sh`), also on a Dependabot bump of
only one of them.

## Agents, rules and handover

The Claude roles are defined in `.claude/agents/`: `implementer.md`,
`reviewer.md` and `plan-reviewer.md`. The coordinator starts them as agents,
or, where a session does not offer them, as general agents told to act as the
role in that file. Lessons that apply to certain files live in
`.claude/rules/*.md`; each rule names its files in `paths:` and loads when a
matching file is read or edited, and the agents read the matching rules
themselves before they start. The GitHub commands per operation are in
`.claude/github-operations.md`.

`.claude/HANDOVER.md` holds only the current state: open pull requests, open
tickets, active decisions and maintainer actions. A session reads it at start
and replaces it at the end, never appending; history lives in pull requests,
issues and git. Follow-ups are open issues labelled `followup`, queried when
needed and not read at start.

## Labels

Labels are defined in `.github/labels.json` and synced to GitHub when that
file changes on `main`.

| Label | Set by | Meaning |
|---|---|---|
| `plan` | coordinator or maintainer | Please write an implementation plan (Claude session). |
| `review` | planner | The plan is posted and waits for review. |
| `ready` | plan reviewer or maintainer | The plan is approved; the issue may be implemented. |
| `second-review` | coordinator or maintainer | Risky pull request: needs a second, independent session review (a marker; it triggers nothing). |
| `db-migration` | automatically | Schema change; back up before upgrading. Makes at least a minor release (see Releases). |
| `breaking` | by hand | Manual upgrade steps; makes a major release (see Releases). |
| `feature`, `bug`, `docs`, `chore` | template or by hand | Kind of change; `feature` makes at least a minor release. |
| `security` | by hand | Security hardening (never an undisclosed vulnerability). Kind label; does not change the version (a patch release unless other labels raise it). |
| `dependencies` | Dependabot | Dependency update. |
| `followup` | coordinator | Non-blocking finding from a review, CI or the day's work; its body names the source. |
| `device-checks` | coordinator | The open round of manual device checks (one at a time; see Pull request). |

Label changes do not notify anyone by themselves: after setting `plan` or
`ready`, say so in the chat with the implementer (for example "plan #18" or
"ready: #18, #19").

## Milestones

Releases are planned with GitHub milestones: one milestone per planned
release, titled `X.Y.Z` (no `v`), with a one-sentence goal as description.

- The coordinator proposes the scope and order of a milestone; the
  maintainer confirms. Then the coordinator assigns the issues (the main
  issue and its sub-issues) and keeps the milestone current. Adding or
  removing an issue after the confirmation needs the maintainer's OK, except
  for bugs in the milestone's own work. A pull request gets the milestone of
  its issue.
- A release is made when every issue of its milestone is closed and CI on
  `main` is green.
- Version check: the dry run's proposed version (`scripts/next-version.sh`,
  label based) must equal the milestone title. If it differs (for example a
  `breaking` pull request slipped in), the coordinator renames the milestone
  to the proposal and tells the maintainer. The one exception: when only
  reverted pull requests raise the proposal, the `version` input lowers it
  to the milestone title, and the coordinator says so on the release.
  Otherwise never use the `version` input to go below the proposal.
- Patch releases: a bug goes into the next patch milestone (`X.Y.Z+1`).
  Releases come from `main`, without release branches and without backports,
  so once the first pull request of the next minor or major milestone is
  merged, every later release is at least that version; open bug fixes then
  move into that milestone. The coordinator releases pending patch
  milestones before merging the first pull request of the next minor when
  that is reasonable.
- After the release the coordinator closes the milestone. An issue closed as
  not planned is removed from its milestone.

Commands: `.claude/github-operations.md`.

## Releases

Releases are published from `main` by hand: **Actions → Release → Run
workflow** (`.github/workflows/release.yml`), after CI on `main` is green. A
dry run builds everything without pushing an image or creating a release.
A release is made when its milestone is done, and the dry run's version must
match the milestone title ([Milestones](#milestones)).

The version is semantic, `X.Y.Z` (`scripts/next-version.sh`). The labels of
the pull requests merged into `main` since the last release decide it, not
the labels of their issues:

| Pull requests in the release | Release | Example after `1.4.2` |
|---|---|---|
| Any `breaking` | Major | `2.0.0` |
| Else any `feature` or `db-migration` | Minor | `1.5.0` |
| Else (bug fixes, security hardening, docs, maintenance, dependencies, no label) | Patch | `1.4.3` |

The first semver release is `1.0.0`, whatever the labels; its range starts at
the last `vYY.MM-N` tag (`v26.10-11`). The old `vYY.MM-N` tags and images stay
and are never renamed. The workflow lists the first-parent commits since the
last release and asks the GitHub API for each commit's pull request. A dry
run shows the proposed version, a table of the pull requests with their
labels and their effect, and the head of the release notes in the run
summary; a pull request without a kind label (`breaking`, `feature`, `bug`,
`chore`, `docs`, `dependencies`, `security`) is marked "no kind label", which
is a warning, not a block. The run fails when:

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
within the range: the reverted pull request still counts. Never set it
below the proposal for any other reason ([Milestones](#milestones)).

The workflow has two jobs. The job `release` builds the image for
`linux/amd64` and `linux/arm64`, pushes it to `ghcr.io/pklnx/mail-archive`
with the exact tag `X.Y.Z` only, and creates the tag `vX.Y.Z` and a GitHub
release with `mail-archive.tar.gz` (the Docker Compose bundle,
`scripts/release-bundle.sh`) and `SHA256SUMS`. Then the job `floating-tags`
checks that the release is published (no draft, no pre-release, its tag on
the run's commit) and that `X.Y.Z` is the highest release, moves `X.Y`, `X`
and `latest` to the digest the build pushed and checks that all four tags
have that digest (a table in its run summary). So the floating tags never
point to a build without a release; for a minute after the release `latest`
still shows the previous one. A dry run skips `floating-tags`; the summary of
`release` lists the tags either way. The notes
start at the same tag as the range and are grouped by label (breaking changes,
database migrations, security, features, fixes, documentation, dependencies,
maintenance; see `.github/release.yml`). A pull request without a kind label
lands under "Maintenance", so a risky pull request needs a kind label in
addition to `second-review`.

If the job `release` or the job `floating-tags` fails, or a release has to
be abandoned, follow the runbook comment at the top of
`.github/workflows/release.yml` (it covers the stray tag `vX.Y.Z` and image
tag `X.Y.Z` of a failed `release`, **Re-run failed jobs** for both jobs,
and moving the floating tags by hand with `docker buildx imagetools create`).

The workflow writes an upgrade paragraph into every release's notes
(`scripts/release-notes.sh`). It links the
[Upgrades guide](../guide/operations#upgrades), and the generated notes list
the `breaking` and `db-migration` pull requests first. Add manual upgrade notes
only when a change needs more than these generic steps. The first semver
release (no `vX.Y.Z` tag before it) also got a paragraph for automatic image
updaters (the tag `26.10` stays on `26.10-11`); it was published with 1.0.0,
and once a `vX.Y.Z` tag exists no release prints it again. The current advice
for updaters is in [Versions](../guide/operations#versions): an exact version
or `X.Y`, moved to the newest `X.Y` tag after reading that release's notes.

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
