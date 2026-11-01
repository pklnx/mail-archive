# Handover

Current state only. At session end replace these sections; never append. History lives in PRs, issues and git.

## Last release
- v1.3.1 (2026-11-01, main 16cd143, release run 41334688736): patch with #468 (future Von gives an empty page, #481), #479 (long attachment name keeps the snippet visible, #482), #478 (screen readers read each Log filter value once, #483). All four image tags carry one digest (sha256:e6d8b531…). Milestone 1.3.1 is closed. Release test #484 (iPhone, VoiceOver) runs after the release; a failure goes into 1.3.2.
- v1.3.0 (main 805c2f8): attachment search, shared accounts, job queue, Log multi-select, Safari link fix (#459), AND→OR audit filters.
- After 1.3.1 the maintainer decides what comes next; no new development until then, planning and docs only.

## Open PRs
- None.

## Open tickets
- 1.4.0 (customer groups): #470 customer groups (wanted, not planned yet) and #476 account sharing (user-name probing, sharing across customers), rethought with #470 (maintainer 2026-11-01): within groups owners share again themselves, limited to their group. The "admins share any account" plan on #476 was dropped, because every admin could then read any account through a share with a user they control; it stays as input for #470.
- 1.5.0: #392 encryption (#433–#437), #393 auditor (#439–#442), #394 GoBD export CLI (#443, #444), all `ready`; #393 and #394 get revised for groups once #470 is planned. #392 stays here (maintainer).
- 1.6.0: #395 SMTP AUTH/TLS (#454–#456), #396 start without CLI (#451–#453), #398 WebKit e2e with #343 are `ready`. #397 operator/quotas/mail alerts (#446–#450) is back in `plan` (quota per customer, alerts to admins only). #432 (database encryption) is not planned yet.
- 1.7.0 (Microsoft and Google, moved to the end by the maintainer on 2026-11-01): #391 OAuth2 (`ready`, #426–#430), #475 journaling from Microsoft 365 (after #430, not planned).
- #484 release test 1.3.1 (maintainer). #372 device checks round 2: Android parts open.

## Active decisions
- **Operating model** (maintainer, 2026-11-01): one shared instance in full-service mode; the operator is its admin and manages the customers' users and accounts; individual instances for single customers on request. Customer groups follow in #470.
- **Coordinator autonomy** (maintainer, 2026-10-31): the coordinator decides alone on UI wording, technical trade-offs, small product details inside an approved goal, and tightening security, and records them. It always asks about new goals and milestone scope, releases, and anything that widens access or weakens security or privacy (including `security.md` guarantees). It reports at milestones and blockers only.
- **Target hardware:** plans state memory and CPU cost on 4 GB RAM / 2 cores and stay within it.
- #459 (Safari): the server rewrites links in served HTML mail; navigations to `/api` with `Sec-Fetch-Site` `cross-site`/`same-site` get 403. Stored mail, `/raw`, parts and exports stay byte-identical.
- Target group: individuals and small businesses. #321: Disable stays usable while a sync runs; only Remove is blocked. German term for the deletion grace period: "Karenzzeit" (#339). Releases: no backports, only the newest line; old tags and images stay. Patch releases carry only `bug`/`docs` PRs. Avoid "fix #N" and other closing keywords in PR bodies for issues that stay open (#477 closed #476 by accident).

## Maintainer actions
- #484 device checks for 1.3.1 (setup with the fixed Caddyfile is in the issue).
- Optional: release highlights for v1.3.1 (sessions may not edit releases); the generated notes already list the three fixes.
