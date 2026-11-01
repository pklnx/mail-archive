# Handover

Current state only. At session end replace these sections; never append. History lives in PRs, issues and git.

## Last release
- v1.2.2 (2026-10-31, main b61f627). **1.3.0 is ready on main 7877466** (app code as of bf5b02f; later commits are docs and test-only fixes): every issue of the milestone is closed except the release test #458 (maintainer's device checks). The release dry run (run 41249220196) proposes 1.3.0 and is green. Draft highlights for the notes: the coordinator posts them on the release; they must state #401's AND→OR change for `user`+`actor` and the #459 security fix.

## Open PRs (waiting for the maintainer's OK, all reviewed and green)
- #464: docs for the operator (also admin) and users (#460 part 3), being reworked for the full-service model; needs the maintainer's OK.

## Open tickets
- 1.3.0: #458 release test (iPhone, Mac Safari/Firefox, VoiceOver; Android has no device, moved to round 3 of #372).
- 1.4.0: #391 OAuth2 (`ready`, #426–#430).
- 1.5.0: #392 encryption (#433–#437), #393 auditor (#439–#442), #394 GoBD export CLI (#443, #444), all `ready`.
- 1.6.0: #395 SMTP AUTH/TLS (#454–#456), #396 start without CLI (#451–#453), #398 WebKit e2e with #343 are `ready`. #397 operator/quotas/mail alerts (#446–#450) is back in `plan` (quota per customer, alerts to admins only). #432 (database encryption) is not planned yet.
- #470 customer groups (tenants) in one shared instance: wanted, no milestone yet, not planned.
- #460 docs rework: parts 1 (#461) and 2 (#466) merged, part 4 (#462) merged, part 3 (#464) waits for the maintainer. Without content changes the docs shrank only 1–3 %; #461 and #466 list 17 cuts that would need the maintainer's OK.
- Follow-up after #462: `docs/development/process.md:6` (maintainer decides UI/wording).

## Active decisions
- **Operating model** (maintainer, 2026-11-01): one shared instance in full-service mode; the operator is its admin and manages the customers' users and accounts; individual instances for single customers on request. Customer groups follow in #470.
- **Coordinator autonomy** (maintainer, 2026-10-31): the coordinator decides alone on UI wording, technical trade-offs, small product details inside an approved goal, and tightening security, and records them. It always asks about new goals and milestone scope, releases, and anything that widens access or weakens security or privacy (including `security.md` guarantees). It reports at milestones and blockers only.
- **Target hardware:** plans state memory and CPU cost on 4 GB RAM / 2 cores and stay within it.
- #459 (Safari): the server rewrites links in served HTML mail; navigations to `/api` with `Sec-Fetch-Site` `cross-site`/`same-site` get 403. Stored mail, `/raw`, parts and exports stay byte-identical.
- Target group: individuals and small businesses. #321: Disable stays usable while a sync runs; only Remove is blocked. German term for the deletion grace period: "Karenzzeit" (#339). Releases: no backports, only the newest line; old tags and images stay. Patch releases carry only `bug`/`docs` PRs.

## Maintainer actions
- #458 device checks, then the OK for the 1.3.0 release and its notes.
- OK on #464; milestone for #470; whether Microsoft 365 journaling (no SMTP AUTH, out of #395) becomes a goal. Approved docs cuts (#461 items 3, 4, 5, 7; #466 items 1, 4, 5, 6, 7, 8) are open work.
