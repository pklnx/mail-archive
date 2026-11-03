# Handover

Current state only. At session end replace these sections; never append. History lives in PRs, issues and git.

## Last release
- v1.4.0 (2026-11-03, main 774a2a0, release run 41624111137, digest sha256:4868f415…): customer groups (#470 parts #500, #502, #501, #503; closes #476), full entries in the Log filter menus (#486, #494), header icons (#488, #495). Migrations 00034 and 00035. Milestone 1.4.0 is closed. Release test #504 (Mac Safari, iPhone, iPad, VoiceOver) after the release; a failure goes into 1.4.1.
- v1.3.2 (main 1e3cc72): #487. v1.3.1 (main 16cd143): #468, #479, #478.

## Open PRs
- None.

## Open tickets
- 1.5.0: #392 encryption (#433–#437), #393 auditor (#439–#442), #394 GoBD export CLI (#443, #444), all `ready`, but #393 and #394 must first be revised for customer groups (maintainer 2026-11-02: an auditor belongs to a group and grants stay inside it; exports never span groups). #392 stays here (maintainer).
- 1.6.0: #395 SMTP AUTH/TLS (#454–#456), #396 start without CLI (#451–#453), #398 WebKit e2e with #343 are `ready`. #397 operator/quotas/mail alerts (#446–#450) is in `plan`: quota per customer group (`customer_groups.quota_bytes`, instance quota as upper bound), alerts to admins only. #432 (database encryption) is not planned yet.
- 1.7.0 (Microsoft and Google): #391 OAuth2 (`ready`, #426–#430), #475 journaling from Microsoft 365 (after #430, not planned).
- #372 device checks round 2: Android parts open.

## Active decisions
- **Operating model** (maintainer, 2026-11-01): one shared instance in full-service mode; the operator is its admin and manages the customers' users and accounts; individual instances for single customers on request. Customer groups follow in #470.
- **Coordinator autonomy** (maintainer, 2026-10-31): the coordinator decides alone on UI wording, technical trade-offs, small product details inside an approved goal, and tightening security, and records them. It always asks about new goals and milestone scope, releases, and anything that widens access or weakens security or privacy (including `security.md` guarantees). It reports at milestones and blockers only.
- **Customer groups** (maintainer 2026-11-02/03): one shared instance, groups separate customers logically; no group admin; sharing by exact name inside the group; group UI hidden with one group, the first extra group is created on the CLI; older Log entries keep the names and groups they had (security.md).
- **GitHub operations without an MCP tool** (maintainer, 2026-11-01): agents do them themselves (milestones via the REST fallback in `.claude/github-operations.md`), not the maintainer.
- **Target hardware:** plans state memory and CPU cost on 4 GB RAM / 2 cores and stay within it.
- #459 (Safari): the server rewrites links in served HTML mail; navigations to `/api` with `Sec-Fetch-Site` `cross-site`/`same-site` get 403. Stored mail, `/raw`, parts and exports stay byte-identical.
- Target group: individuals and small businesses. #321: Disable stays usable while a sync runs; only Remove is blocked. German term for the deletion grace period: "Karenzzeit" (#339). Releases: no backports, only the newest line; old tags and images stay. Patch releases carry only `bug`/`docs` PRs. Avoid "fix #N" and other closing keywords in PR bodies for issues that stay open (#477 closed #476 by accident).

## Maintainer actions
- #504 release test of 1.4.0 (setup with the fixed Caddyfile is in the issue).
- On the shared instance: back up, `./ma migrate`, then one `./ma group add NAME` per customer and `./ma user set-group`; after the first extra group everything works on the Users page.
