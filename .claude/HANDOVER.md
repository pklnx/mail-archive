# Handover

Current state only. At session end replace these sections; never append. History lives in PRs, issues and git.

## Last release
- v1.2.0 (2026-10-30, main 8047a72). The `floating-tags` job passed its live test there (#365 comment 6522504898).

## Open PRs
- None. Dependabot waits 14 days before it opens a PR for a new version (`cooldown` in `.github/dependabot.yml`); security updates are exempt.

## Open tickets
- No ticket is `ready`, and there is no open follow-up besides #343 (WebKit e2e guards, parked until a WebKit project is added; it also carries #312 items 5 and 6 for that moment).
- Initial JS on main is 569 940 of 579 000.

## Active decisions
- #321: Disable stays usable while a sync runs; only Remove is blocked (6474522407).
- German UI term for the deletion grace period is "Karenzzeit" (#339, 6501382946).
- The coordinator picks the backlog order and sets `plan` itself (CLAUDE.md, process.md). Within a ticket, tooling, CI and test-scope trade-offs are the coordinator's call, recorded on the issue. The maintainer gets product, UI and wording questions and secret-scan changes.
- Releases: no backports; only the newest line gets releases (#314, 6518415969). Old releases, tags and images stay, including `26.10-*`, as `docs/guide/operations.md` promises.

## Maintainer actions
- Manual device checks run in rounds (maintainer decision, 2026-10-30; process.md, "4. Pull request"). Round 2 is #372 (label `device-checks`); the coordinator appends each merged UI PR's "Open manual checks" there and closes the round once the maintainer has gone through it.
