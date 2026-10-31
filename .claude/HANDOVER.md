# Handover

Current state only. At session end replace these sections; never append. History lives in PRs, issues and git.

## Last release
- v1.2.2 (2026-10-31, main b61f627). **1.3.0 is ready on main 7877466** (code as of bf5b02f; later commits are docs only): every issue of the milestone is closed except the release test #458 (maintainer's device checks). The release dry run (run 41249220196) proposes 1.3.0 and is green. Draft highlights for the notes: the coordinator posts them on the release; they must state #401's AND→OR change for `user`+`actor` and the #459 security fix.

## Open PRs (waiting for the maintainer's OK, all reviewed and green)
- #462: `.claude/` and `CLAUDE.md` (#460 part 4): autonomy, hosted only, target hardware.
- #464: docs hosted-only framing (#460 part 3), with a list of policy statements for the maintainer.
- This PR (handover).

## Open tickets
- 1.3.0: #458 release test (iPhone, Mac Safari/Firefox, VoiceOver; Android has no device, moved to round 3 of #372).
- 1.4.0: #391 OAuth2 (`ready`, #426–#430).
- 1.5.0: #392 encryption (#433–#437), #393 auditor (#439–#442), #394 GoBD export CLI (#443, #444), all `ready`.
- 1.6.0: #395 SMTP AUTH/TLS (#454–#456), #396 start without CLI (#451–#453), #397 operator/quotas/mail alerts (#446–#450), #398 WebKit e2e with #343, all `ready`. #432 (database encryption) is not planned yet.
- #460 docs rework: parts 1 (#461) and 2 (#466) merged, parts 3 (#464) and 4 (#462) wait for the maintainer. Without content changes the docs shrank only 1–3 %; #461 and #466 list 17 cuts that would need the maintainer's OK.
- Follow-ups after #462/#464: `docs/development/process.md:6` (maintainer decides UI/wording), `README.md`, `docs/index.md` still say self-hosted.

## Active decisions
- **Hosted only** (maintainer, 2026-10-31): one instance per customer, run by the operator (the maintainer); customers do not self-host; bundle and Compose are operator tooling; new instances start encrypted (#392 decision 6); GoBD export only by the operator at the CLI (#394); one shared OAuth app (#391).
- **Coordinator autonomy** (maintainer, 2026-10-31): the coordinator decides alone on UI wording, technical trade-offs, small product details inside an approved goal, and tightening security, and records them. It always asks about new goals and milestone scope, releases, and anything that widens access or weakens security or privacy (including `security.md` guarantees). It reports at milestones and blockers only. Until #462 merges, CLAUDE.md still says otherwise; this decision applies already.
- **Target hardware:** plans state memory and CPU cost on 4 GB RAM / 2 cores and stay within it.
- #459 (Safari): the server rewrites links in served HTML mail; navigations to `/api` with `Sec-Fetch-Site` `cross-site`/`same-site` get 403. Stored mail, `/raw`, parts and exports stay byte-identical.
- German UI term for the deletion grace period is "Karenzzeit" (#339). Releases: no backports, only the newest line; old tags and images stay. Patch releases carry only `bug`/`docs` PRs.

## Maintainer actions
- #458 device checks, then the OK for the 1.3.0 release and its notes.
- OK on #462 and #464; decide on the cuts listed in #461 and #466.
- Microsoft 365 journaling (no SMTP AUTH) is out of #395; decide whether it becomes a goal.
