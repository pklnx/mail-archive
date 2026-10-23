# Flaky tests

Every rerun of a CI job because of a flaky test gets a row here. A test listed twice is fixed right away.

| Date | Test | CI run | Cause | Status |
|---|---|---|---|---|
| 2026-10-22 | `internal/web` `TestReconcileButton` | [PR #184, job 120140810421](https://github.com/pklnx/mail-archive/actions/runs/39869721886/job/120140810421) | `waitNewRun(f, "private", "")` accepted the previous run: the runner takes an account off the queue before the new run holds the sync lock, and in between `/api/accounts` shows it idle with the last run. Locally 3 of 40 on main. #187 measured 169b3bf (before #183) against d1c158c (#183): 0 of 80 each on an idle machine, 5 of 75 and 9 of 75 under CPU load, and the same dequeue-to-lock window (p50 about 0.7 ms, p99 about 8 ms). The window depends on load and is older than #183; #183 widens it only while the blob lock is held exclusively, which this test never does. | Fixed: `waitNewRun` waits for a finished run other than the given one. The window is closed by #187: the runner claims the account until its sync returned, so it shows `queued` instead of `idle` (`TestSyncStateNeverIdle`). |
