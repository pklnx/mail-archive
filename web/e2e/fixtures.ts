// The test fixture every spec uses. Its guards fail a test on:
// - an uncaught error in a page,
// - a console error,
// - a Content Security Policy violation,
// - a request to any host but the demo server (the request is aborted).
// They keep the suite off the network and catch CDN fonts, injected styles
// and similar regressions while the UI changes.
import { expect, test as base, type Locator, type Page } from "@playwright/test";
import { origin } from "./demo";

export { expect };

// Console errors that come from the mail's own HTML in the sandboxed message
// frame (/api/messages/<id>/html), not from the app. The browser logs each
// thing the frame's sandbox and CSP block, and blocking is their job.
const message = `${origin.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}/api/messages/[0-9a-f]{64}`;
const mailFrameErrors: { text: RegExp; url: RegExp }[] = [
  // A remote image or style the frame's CSP refused (the "Load remote
  // images" gate).
  { text: /^Refused to load .* because it violates the following Content Security Policy directive/, url: new RegExp(`^${message}/html`) },
  // This suite's own init script (the CSP listener below): the frame has no
  // allow-scripts, so it never runs there.
  {
    text: new RegExp(`^Blocked script execution in '${message}/html[^']*' because the document's frame is sandboxed and the 'allow-scripts' permission is not set\\.$`),
    url: /.*/, // reported with or without the frame's URL
  },
  // An inline (cid:) image of the mail: its part URL answers with
  // Cross-Origin-Resource-Policy: same-origin, and the sandboxed frame has
  // an opaque origin, so the image does not show. A known app issue, left
  // for a follow-up; this suite does not change app behaviour.
  { text: /^Failed to load resource: net::ERR_BLOCKED_BY_RESPONSE\.NotSameOrigin$/, url: new RegExp(`^${message}/parts/\\d+$`) },
];

function fromMailFrame(url: string, text: string): boolean {
  return mailFrameErrors.some((e) => e.text.test(text) && e.url.test(url));
}

/** Lets a test declare the error answers it provokes on purpose. */
export interface Guards {
  /** An answer with this status to a request for this path is expected. */
  allowHTTPError(path: string, status: number): void;
}

export const test = base.extend<{ guards: Guards }>({
  guards: [
    async ({ context }, use) => {
      const problems: string[] = [];
      const allowed: { path: string; status: number }[] = [];
      // The browser logs every 4xx/5xx answer as a console error.
      const expectedHTTPError = (url: string, text: string) =>
        allowed.some((a) => {
          try {
            const u = new URL(url);
            return u.origin === origin && u.pathname === a.path && text.includes(`status of ${a.status} `);
          } catch {
            return false;
          }
        });
      await context.route(
        (url) => url.origin !== origin,
        (route) => {
          problems.push(`request to another host: ${route.request().method()} ${route.request().url()}`);
          return route.abort("blockedbyclient");
        },
      );
      // The browser logs CSP violations too; the listener also catches the
      // ones of report-only policies and names the directive.
      await context.addInitScript(() => {
        document.addEventListener("securitypolicyviolation", (e) => {
          console.error(`CSP violation: ${e.violatedDirective} blocked ${e.blockedURI || "inline"} in ${e.sourceFile || e.documentURI}`);
        });
      });
      context.on("console", (msg) => {
        if (msg.type() !== "error") return;
        const { url } = msg.location();
        if (fromMailFrame(url, msg.text()) || expectedHTTPError(url, msg.text())) return;
        problems.push(`console error: ${msg.text()} (${url})`);
      });
      context.on("weberror", (err) => problems.push(`page error: ${err.error().stack ?? err.error().message}`));
      await use({ allowHTTPError: (path, status) => allowed.push({ path, status }) });
      expect(problems, "guards: errors, CSP violations or foreign requests").toEqual([]);
    },
    { auto: true },
  ],
});

/**
 * The card of an account on the Accounts page: the innermost block with the
 * account's name and the button (Edit for an active account). Cards have no
 * role yet; the Carbon conversion of the page (#171) can give them one.
 */
export function accountCard(page: Page, name: string, button = "Edit"): Locator {
  return page
    .getByRole("main")
    .locator("div")
    .filter({ has: page.getByText(name, { exact: true }) })
    .filter({ has: page.getByRole("button", { name: button }) })
    .last();
}

/** Accepts the next window.confirm or window.prompt, optionally with text. */
export function acceptDialog(page: Page, text?: string): void {
  page.once("dialog", (d) => void d.accept(text));
}
