# Handover

Current state only. At session end replace these sections; never append. History lives in PRs, issues and git.

## Last release
- v1.3.0 (2026-11-01, main 805c2f8, release run 41297250398): attachment search (#390), shared accounts (#389), job queue, Log multi-select, Safari link fix (#459), AND→OR audit filters (#401). All four image tags carry one digest (sha256:f5366854…). Milestone 1.3.0 is closed. The release body still lacks the highlights: the maintainer pastes them (sessions may not edit releases).
- **Pause after 1.3.0** (maintainer): no new development until the maintainer resumes; planning and docs only.

## Open PRs
- None.

## Open tickets
- #458 release test of 1.3.0 (iPhone, Mac Safari/Firefox, VoiceOver), done by the maintainer after the release; a failure goes into 1.3.1. #468 (Log: future Von gives an API error) is a `followup` bug.
- 1.4.0 (customer groups): #470 customer groups (wanted, not planned yet), #476 account sharing leaks user names and crosses customers (interim fix until #470, not planned), #468.
- 1.5.0: #392 encryption (#433–#437), #393 auditor (#439–#442), #394 GoBD export CLI (#443, #444), all `ready`; #393 and #394 get revised for groups once #470 is planned. #392 stays here (maintainer).
- 1.6.0: #395 SMTP AUTH/TLS (#454–#456), #396 start without CLI (#451–#453), #398 WebKit e2e with #343 are `ready`. #397 operator/quotas/mail alerts (#446–#450) is back in `plan` (quota per customer, alerts to admins only). #432 (database encryption) is not planned yet.
- 1.7.0 (Microsoft and Google, moved to the end by the maintainer on 2026-11-01): #391 OAuth2 (`ready`, #426–#430), #475 journaling from Microsoft 365 (after #430, not planned).
- #460 docs rework is done: parts 1–4 (#461, #466, #464, #462) and the approved cuts (#471, #473).

## Active decisions
- **Operating model** (maintainer, 2026-11-01): one shared instance in full-service mode; the operator is its admin and manages the customers' users and accounts; individual instances for single customers on request. Customer groups follow in #470.
- **Coordinator autonomy** (maintainer, 2026-10-31): the coordinator decides alone on UI wording, technical trade-offs, small product details inside an approved goal, and tightening security, and records them. It always asks about new goals and milestone scope, releases, and anything that widens access or weakens security or privacy (including `security.md` guarantees). It reports at milestones and blockers only.
- **Target hardware:** plans state memory and CPU cost on 4 GB RAM / 2 cores and stay within it.
- #459 (Safari): the server rewrites links in served HTML mail; navigations to `/api` with `Sec-Fetch-Site` `cross-site`/`same-site` get 403. Stored mail, `/raw`, parts and exports stay byte-identical.
- Target group: individuals and small businesses. #321: Disable stays usable while a sync runs; only Remove is blocked. German term for the deletion grace period: "Karenzzeit" (#339). Releases: no backports, only the newest line; old tags and images stay. Patch releases carry only `bug`/`docs` PRs.

## Maintainer actions
- #458 device checks; paste the highlights into the v1.3.0 release body.
