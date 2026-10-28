# Handover

Current state only. At session end replace these sections; never append. History lives in PRs, issues and git.

## Open PRs
- #328 (dependabot oxlint 1.89.0) and #329 (oxlint-tsgolint 7.0.2005): on hold under the 14-day rule; pick up on or after 2026-11-02 and 2026-11-05, with a fresh oxlint run over `web/src` (`make review-gate`).
- Other open dependabot PRs: #318 (squawk-cli 2.70.0), #319 (vitepress alpha.21, docs), #325 (zizmor 1.31.1). Check each release's age against the 14-day rule before picking it up.

## Open tickets
- #282 Carbon app feel: parts #283, #284, #286, #288 done; #285 and #287 open. Closes after its last part.
- #285 tables with detail pages (ready): part 1 #320 done (PR #330), part 2 #321 done (PR #333), part 3 #322 (retention, risky, `second-review`) next. Initial JS on main is about 576 018 of 579 000.
- #287 skeletons and motion (ready): plan Revision 2 (comments 6474163675, 6474579468, 6474635471), approved in 6474648593. Build after #322 merges, as one PR; start with a fresh `t.loading` grep.
- #290 docs screenshots and WebKit e2e guards: point 2 parked.
- Follow-up issues #306-#314 (labels per item).

## Active decisions
- #287: Profile and Log get skeletons; the start-up gate has only a hidden status; folder overlay and conversation panel animate on open only (decisions 6474512360).
- #321: Disable stays usable while a sync runs; only Remove is blocked (6474522407).

## Maintainer actions
- Manual device checks:
  - #283: iPhone.
  - #284: Safari, iOS, notch.
  - #320: screen reader, dark mode.
  - #286: Safari/Firefox focus on click in the date fields, iOS zoom and calendar fit at 390 px, screen reader.
  - #321: screen reader on the account page, dark mode, long name on a phone.
