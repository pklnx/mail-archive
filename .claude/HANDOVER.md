# Handover

Current state only. At session end replace these sections; never append. History lives in PRs, issues and git.

## Last release
- v1.2.1 (2026-10-30, main ee304bb): the five bugs the maintainer found in v1.2.0 (#375, #377-#380). All four image tags carry one digest, checked in the registry.

## Open PRs
- None. Dependabot waits 14 days before it opens a PR for a new version (`cooldown` in `.github/dependabot.yml`); security updates are exempt.

## Open tickets
- No ticket is `ready`. Next in the backlog:
  - #376: multi-select for Aktion/Wer on the Log. It is a feature, so it goes into 1.3.0, by maintainer decision. It builds on the shared `DateRange` from #377.
  - #385: date range fields and the applied filter can disagree after a field is emptied, and Enter in an emptied field throws. The bug is in the shared component, so the fix changes the search filters too. One more case (Tab with the calendar open) is in the #386 body.
  - #343: WebKit e2e guards, parked until a WebKit project is added. It also carries #312 items 5 and 6.
- Initial JS on main is about 569 900 of 579 000.

## Active decisions
- #321: Disable stays usable while a sync runs; only Remove is blocked (6474522407).
- German UI term for the deletion grace period is "Karenzzeit" (#339, 6501382946).
- The coordinator picks the backlog order and sets `plan` itself (CLAUDE.md, process.md). Within a ticket, tooling, CI and test-scope trade-offs are the coordinator's call, recorded on the issue. The maintainer gets product, UI and wording questions and secret-scan changes.
- Releases are planned in milestones `X.Y.Z` (process.md, "Milestones"): the coordinator proposes scope and order, the maintainer confirms.
- Product direction: a SaaS offering with one instance per customer; the self-hosted open source version stays. Target group: individuals and small businesses.
- Releases in patch versions carry only `bug`/`docs` PRs; a `feature` PR makes the next release minor (`next-version.sh`). Releases: no backports; only the newest line gets releases (#314, 6518415969). Old releases, tags and images stay, including `26.10-*`, as `docs/guide/operations.md` promises.

## Maintainer actions
- Manual device checks run in rounds (maintainer decision, 2026-10-30; process.md, "4. Pull request"). Round 2 is #372 (label `device-checks`); the coordinator appends each merged UI PR's "Open manual checks" there and closes the round once the maintainer has gone through it.
