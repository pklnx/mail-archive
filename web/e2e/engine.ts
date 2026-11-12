// Engine facts that some tests' expectations follow (#398). The suite runs
// in Chromium (Playwright's headless shell) and in Playwright's WebKit, the
// Linux port. An assertion about the app holds in both; where an expected
// value is an engine fact, a helper here reads that fact from the browser
// and names it, instead of a skip per browser.
import type { Page } from "@playwright/test";
import { expect } from "./fixtures";

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
