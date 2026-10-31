---
paths:
  - "web/e2e/**"
  - "web/playwright.config.ts"
---
# e2e
- No Playwright `evaluate` or locator inside the mail frame; read its document through `iframe.contentDocument` from the parent page and click at coordinates (HANDOVER.md:36 at 26b7a90; `web/e2e/mailFrame.ts:9-15`). The older calls in `web/e2e/mail.spec.ts:33-40,47` predate the rule; do not copy them.
- The demo uses fixed ports 18181/18143 (`web/e2e/demo.ts:5-6`); the redirect check in `frame-links.spec.ts` listens on 127.0.0.2:18182 (`redirectOrigin`). Parallel agents change them only in their own worktree and never commit that. On port contention, wait; never kill.
- The suite needs `DEMO_DATABASE_URL`.
- Traces record no DOM snapshots and retries stay 0 (`web/playwright.config.ts:15-17,29-33`).
- `E2E_BROWSER` (`chromium` default, `webkit`) picks the project; the `webkit` project runs only `frame-links.spec.ts` until #398, with traces without screenshots (#459).
