# Handover

Current state only. At session end replace these sections; never append. History lives in PRs, issues and git.

## Open PRs
- None. Dependabot waits 14 days before it opens a PR for a new version (`cooldown` in `.github/dependabot.yml`); security updates are exempt.

## Open tickets
- No ticket is `ready`.
- Follow-up issues #306-#314 and #343 (WebKit e2e guards, parked until a WebKit project is added).
- Initial JS on main is 569 913 of 579 000.

## Active decisions
- #321: Disable stays usable while a sync runs; only Remove is blocked (6474522407).
- German UI term for the deletion grace period is "Karenzzeit" (#339, 6501382946).
- Within a `ready` ticket, tooling, CI and test-scope trade-offs are the coordinator's call, recorded on the issue. The maintainer gets product, UI and wording questions and secret-scan changes.

## Maintainer actions
- Manual device checks: the standing checklist in #335. The coordinator appends each merged UI PR's "Open manual checks" there.
