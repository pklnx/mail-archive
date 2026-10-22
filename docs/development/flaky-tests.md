# Flaky tests

Every rerun of a CI job because of a flaky test gets a row here. A test listed twice is fixed right away.

| Date | Test | CI run | Cause | Status |
|---|---|---|---|---|
| 2026-10-22 | `internal/web` `TestReconcileButton` | [PR #184, job 120140810421](https://github.com/pklnx/mail-archive/actions/runs/39869721886/job/120140810421) | `waitNewRun(f, "private", "")` accepted the previous run: the runner takes an account off the queue before the new run holds the sync lock, and in between `/api/accounts` shows it idle with the last run. Locally 3 of 40 on main. | Fixed: `waitNewRun` waits for a finished run other than the given one. |
