// Engine facts that some tests' expectations follow (#398). The suite runs
// in Chromium (Playwright's headless shell) and in Playwright's WebKit, the
// Linux port. An assertion about the app holds in both; where an expected
// value is an engine fact, a helper here reads that fact from the browser,
// or names it by engine where only provoking it would show it, instead of a
// skip per browser.
import type { Page } from "@playwright/test";
import { expect, lostCharacter } from "./fixtures";

/**
 * Whether the page's browser has passkeys as the app checks it
 * (web/src/webauthn.ts, passkeysSupported): window.PublicKeyCredential is a
 * function and navigator.credentials exists. Playwright's WebKit stubs
 * PublicKeyCredential as an object in its bootstrap script, so the app
 * offers no passkeys there. In Chromium it must be true, so a check that
 * follows it is never vacuous there.
 */
export async function passkeysSupported(page: Page, browserName: string): Promise<boolean> {
  const supported = await page.evaluate(() => typeof window.PublicKeyCredential === "function" && !!navigator.credentials);
  if (browserName === "chromium") expect(supported, "Chromium has WebAuthn").toBe(true);
  return supported;
}

/**
 * Whether the engine refuses a fetch that a document starts while its main
 * frame has a navigation to a new document in flight (#552). WebKit does:
 * CachedResource::load fails it before any request is sent, and WebKit logs
 * "Fetch API cannot load <url> due to access control checks.", which
 * Playwright reports as a page error. Chromium sends the request. A test
 * that waits for the one or the other times out where this is wrong, so it
 * is never vacuous.
 */
export function refusesFetchDuringNavigation(browserName: string): boolean {
  return browserName === "webkit";
}

/**
 * The first line of the guards' problem for a same-origin fetch that fails
 * an access-control check (route.abort("accessdenied")), as each engine
 * reports it (#552). WebKit: the page error "Fetch API cannot load <url> due
 * to access control checks.", which WebKit logs from the network callback,
 * with no script running, so it has no stack; Playwright dropped the
 * character after the first colon, the one in the URL's "http:", and the
 * guards show lostCharacter for it (fixtures.ts, pageErrorText). Chromium:
 * the console error "Failed to load resource: net::ERR_ACCESS_DENIED" at
 * the URL.
 */
export function accessDeniedFetchProblem(browserName: string, url: string): string {
  if (browserName !== "webkit") return `console error: Failed to load resource: net::ERR_ACCESS_DENIED (${url})`;
  const colon = url.indexOf(":");
  return `page error: Fetch API cannot load ${url.slice(0, colon + 1)}${lostCharacter}${url.slice(colon + 2)} due to access control checks.`;
}
