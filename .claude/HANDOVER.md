# Handover

Current state only. At session end replace these sections; never append. History lives in PRs, issues and git.

## Last release
- v1.2.2 (2026-10-31, main b61f627): #385 (date range fields always show the applied range; shared `DateRange`, so search filters and Log). All four image tags carry one digest (sha256:e273e825…), checked in the registry. Milestone 1.2.2 is closed.

## Open PRs
- #400's PR (Log multi-select UI, part 2 of #376) is being implemented. Dependabot waits 14 days before it opens a PR for a new version (`cooldown` in `.github/dependabot.yml`); security updates are exempt.

## Open tickets
- Milestone 1.3.0 (open on GitHub, see Milestones): #376 (part 1 #399 merged via #401; part 2 #400 in progress, `ready`), #389 shared mailboxes, #390 attachment search, #404 search `after:`/`before:` count UTC days, #406 `retentionDay` test fails in Asia/Tokyo. #402 is done (#405). The 1.3.0 release notes must state #401's AND→OR change for `user`+`actor` together.
- Later milestones: 1.4.0 #391 OAuth2; 1.5.0 #392 encryption at rest, #393 auditor access, #394 GoBD export; 1.6.0 #395 SMTP auth/TLS, #396 start without CLI, #397 operator/quotas, #398 WebKit e2e with #343.
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
