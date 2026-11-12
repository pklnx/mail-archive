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
- The guards' `securitypolicyviolation` listener is the guard of record in WebKit; its only WebKit exceptions are narrow and in `fixtures.ts` (#343, #398): WebKit's own `<select>` style report (a `style-src-attr` report without sample whose target is a select without a style attribute, or the document right after script created a select; at most one per select); WebKit's own console lines for a refused style and for a remote image in the mail frame, matched by their words (the listener still reports them on an app page); and the ResizeObserver loop page error by its exact text, until the frame fit defers its work. Never widen them; `guards.spec.ts` keeps a test that each still fails what it must.
