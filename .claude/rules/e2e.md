---
paths:
  - "web/e2e/**"
  - "web/playwright.config.ts"
---
# e2e
- No Playwright `evaluate` or locator inside the mail frame; read its document through `iframe.contentDocument` from the parent page and click at coordinates (HANDOVER.md:36 at 26b7a90; `web/e2e/mailFrame.ts:9-15`). The older calls in `web/e2e/mail.spec.ts:33-40,47` predate the rule; do not copy them.
- The demo uses fixed ports 18181/18143 (`web/e2e/demo.ts:5-6`); the redirect check in `frame-links.spec.ts` listens on 127.0.0.2:18182 (`redirectOrigin`), `referrer.spec.ts` and `css-images.spec.ts` on 127.0.0.2:18183 (`referrerOrigin`, `recorder.ts`), `hints.spec.ts` on 18184-18187 of 127.0.0.1 and [::1] (`hintHTTPPort`, `hintConnectPort`, `controlHTTPPort`, `controlConnectPort`). Parallel agents change them only in their own worktree and never commit that. On port contention, wait; never kill.
- The suite needs `DEMO_DATABASE_URL`.
- Traces record no DOM snapshots and retries stay 0 (`web/playwright.config.ts:22-24,36-40`); `webkit` traces record no screenshots either (#343).
- One engine per run: `E2E_BROWSER` (`chromium` default, `webkit`) picks the single project; never two projects against one demo (its state, e.g. `retention.spec.ts`'s folder). `pnpm e2e:webkit` runs the suite in Playwright's WebKit, the Linux port, not Safari (#398).
- Engine scoping is by tag, never `test.skip` per browser or `test.fixme`; the `webkit` project's `grepInvert` excludes the tags. `@cdp`: the test uses the DevTools protocol (`newCDPSession`). `@chromium-layer`: only for a test of the web UI's own frame blocker or `inert` gate, which WebKit does not run (#459). `@chromium-launch`: the test launches its own Chromium. Split a test so only the engine-bound part is tagged.
- An assertion about the app holds in both engines; a WebKit failure there is an app bug (`bug` issue). An expected value that is an engine fact is engine-aware and names the fact (`web/e2e/engine.ts`).
- The guards make no WebKit exception (#343). WebKit's remote-image refusal in the mail frame is matched by its wording only (no location); the guards' `securitypolicyviolation` listener still fails it on an app page (`guards.spec.ts`).
