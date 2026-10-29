# Handover

Current state only. At session end replace these sections; never append. History lives in PRs, issues and git.

## Open PRs
- #338 (Dependabot, zizmor 1.30.1 to 1.31.0 in `tools/review-gate`): it replaced #325. It probably needs two changes beside the bump (not yet verified). The `zz-target` self-test fixture in `scripts/review-gate-test.sh` expects dangerous-triggers on line 3, but zizmor reports line 4 since its span change (zizmorcore/zizmor#2402; see the #325 hold comment 6497175643). The zizmor row in `docs/development/review-gate.md` needs updating too.
- Dependabot closed #319 (vitepress), #325 (zizmor 1.31.1), #328 (oxlint) and #329 (oxlint-tsgolint) on 2026-10-29, after the `cooldown` from #337. It opens new PRs once a version is 14 days old; security updates are exempt.

## Open tickets
- No ticket is `ready`. #282 (Carbon app feel) is done with all six parts; initial JS on main is 569 913 of 579 000.
- #290 docs screenshots and WebKit e2e guards: point 2 parked.
- Follow-up issues #306-#314 (labels per item).

## Active decisions
- #321: Disable stays usable while a sync runs; only Remove is blocked (6474522407).
- German UI term for the deletion grace period is "Karenzzeit" (#339, 6501382946).

## Maintainer actions
- Manual device checks: the standing checklist in #335. The coordinator appends each merged UI PR's "Open manual checks" there.
