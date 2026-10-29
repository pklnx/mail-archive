# Handover

Current state only. At session end replace these sections; never append. History lives in PRs, issues and git.

## Open PRs
- Dependabot, on hold under the 14-day rule:
  - #325 (zizmor 1.31.1): from 2026-10-30. It needs the self-test fixture fix in `scripts/review-gate-test.sh` (zz-target expects dangerous-triggers on line 3; zizmor 1.31.1 reports line 4) and the zizmor row in `docs/development/review-gate.md` (6497175643).
  - #328 (oxlint 1.89.0): from 2026-11-02.
  - #329 (oxlint-tsgolint 7.0.2005): from 2026-11-05. For #328 and #329, run oxlint fresh over `web/src` (`make review-gate`).
  - #319 (vitepress alpha.21, docs): from 2026-11-08 (6497168656).
- Dependabot waits 14 days for new versions on every ecosystem (`cooldown` in `.github/dependabot.yml`, #337); security updates are exempt.

## Open tickets
- No ticket is `ready`. #282 (Carbon app feel) is done with all six parts; initial JS on main is 569 913 of 579 000.
- #290 docs screenshots and WebKit e2e guards: point 2 parked.
- Follow-up issues #306-#314 (labels per item).

## Active decisions
- #321: Disable stays usable while a sync runs; only Remove is blocked (6474522407).
- German UI term for the deletion grace period is "Karenzzeit" (#339, 6501382946).

## Maintainer actions
- Manual device checks: the standing checklist in #335. The coordinator appends each merged UI PR's "Open manual checks" there.
