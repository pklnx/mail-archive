---
paths:
  - "**/*_test.go"
---
# Go tests
- Bound every hang guard by `testwait.Limit` (`internal/testwait/testwait.go:55`).
- Never check "still waits after N ms"; wait for the lock wait itself, as `waitForRow` does (`internal/store/pgwait_test.go:17`).
