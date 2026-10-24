import { readdirSync, readFileSync } from "node:fs";
import { lockedUser, timeStep, totp, twoFactorUser } from "./demo";
import type { Page } from "@playwright/test";
import { expect, test } from "./fixtures";

// No session: these tests use the login page.
test.use({ storageState: { cookies: [], origins: [] } });

// Without a session, GET /api/session answers 401 by design (on load and
// after logging out); the browser logs that as a console error.
test.beforeEach(({ guards }) => guards.allowHTTPError("/api/session", 401));

// The password field. Exact: the field's "Show password" button names it too.
const passwordField = (page: Page) => page.getByLabel("Password", { exact: true });
// The login error. Carbon's text field has an empty alert region of its own
// (for its character counter), so the one alert with text is the error.
const loginError = (page: Page) => page.getByRole("alert").filter({ hasText: /\S/ });

test("login with password and authentication code", async ({ page }) => {
  await page.goto("/");
  // Carbon's type scale applies: heading-04 for the app name (28px), not
  // body text. Its tokens are scoped inside Carbon's CSS, so a class that
  // relies on them silently falls back to 16px with line-height 1.
  await expect(page.getByRole("heading", { name: "Mail Archive", level: 1 })).toHaveCSS("font-size", "28px");
  await page.getByLabel("User name").fill(twoFactorUser.name);
  await passwordField(page).fill(twoFactorUser.password);
  await page.getByRole("button", { name: "Log in" }).click();

  const codeHeading = page.getByRole("heading", { name: "Enter your authentication code" });
  await expect(codeHeading).toBeVisible();
  // heading-03: 20px on a 28px line.
  await expect(codeHeading).toHaveCSS("font-size", "20px");
  await expect(codeHeading).toHaveCSS("line-height", "28px");
  // body-01 for the text below it: 14px.
  await expect(page.getByText("Enter the six-digit code from your authenticator app, or a recovery code.")).toHaveCSS("font-size", "14px");
  // The password is not carried over into the code field.
  await expect(page.getByLabel("Authentication code")).toHaveValue("");
  // The demo confirmed this user's 2FA with an old code, so the current
  // step is unused. It runs once (retries: 0), so the step is never reused.
  await page.getByLabel("Authentication code").fill(totp(twoFactorUser.totpSecret, timeStep()));
  await page.getByRole("button", { name: "Log in" }).click();

  // kim has no accounts: the mail view opens empty, with kim logged in.
  await expect(page.getByRole("button", { name: twoFactorUser.name })).toBeVisible();
  await expect(page.getByRole("searchbox", { name: "Search mail" })).toBeVisible();
  await page.getByRole("button", { name: "Log out" }).click();
  await expect(page.getByLabel("User name")).toBeVisible();
});

test("login errors: wrong password and locked user", async ({ page, guards }) => {
  // The locked user's login answers 403.
  guards.allowHTTPError("/api/session", 403);
  await page.goto("/");
  await page.getByLabel("User name").fill(twoFactorUser.name);
  await passwordField(page).fill("not-the-password");
  const wrong = page.waitForResponse((r) => r.url().endsWith("/api/session") && r.request().method() === "POST");
  await page.getByRole("button", { name: "Log in" }).click();
  expect((await wrong).status()).toBe(401);
  // The notification's text: its status icon's label, then the message.
  await expect(loginError(page)).toHaveText("ErrorWrong user name or password.");
  // The password field is cleared after a failed login.
  await expect(passwordField(page)).toHaveValue("");

  await page.getByLabel("User name").fill(lockedUser.name);
  await passwordField(page).fill(lockedUser.password);
  const locked = page.waitForResponse((r) => r.url().endsWith("/api/session") && r.request().method() === "POST");
  await page.getByRole("button", { name: "Log in" }).click();
  expect((await locked).status()).toBe(403);
  await expect(loginError(page)).toHaveText("ErrorThis user is locked. Ask an admin to unlock it.");
  await expect(page.getByRole("heading", { name: "Enter your authentication code" })).toHaveCount(0);
});

test("the login page shows the product mark for the theme", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "light" });
  await page.goto("/");
  const brand = page.locator(".auth-page__brand img");
  await expect(brand).toHaveClass(/(^|\s)app-mark--light(\s|$)/);
  // Decorative: the heading next to it is the name.
  await expect(brand).toHaveAttribute("alt", "");
  expect(await brand.evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth > 0)).toBe(true);

  // theme.ts follows the system: the mark follows with it.
  await page.emulateMedia({ colorScheme: "dark" });
  await expect(brand).toHaveClass(/(^|\s)app-mark--dark(\s|$)/);
  await expect.poll(() => brand.evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth > 0)).toBe(true);
});

test("the web app manifest and its icons are served without a session", async ({ page }) => {
  // The built binary: Vite copies web/public into the embedded build, and
  // the real route table lets these paths through without a login (#242).
  const files: Record<string, string> = {
    "/manifest.webmanifest": "application/manifest+json",
    "/favicon-32.png": "image/png",
    "/apple-touch-icon.png": "image/png",
    "/icon-192.png": "image/png",
    "/icon-512.png": "image/png",
    "/icon-maskable-512.png": "image/png",
  };
  // A new file in web/public needs a row here, and a route in the server.
  const pub = readdirSync(new URL("../public", import.meta.url)).map((n) => `/${n}`);
  expect(Object.keys(files).sort()).toEqual(pub.sort());
  for (const [path, type] of Object.entries(files)) {
    const resp = await page.request.get(path);
    expect(resp.status(), path).toBe(200);
    expect(resp.headers()["content-type"], path).toBe(type);
    expect(resp.headers()["cache-control"], path).toBe("no-cache");
    expect(resp.headers()["content-security-policy"], path).toContain("default-src 'self'");
    expect(Buffer.compare(await resp.body(), readFileSync(new URL(`../public${path}`, import.meta.url))), path).toBe(0);
  }
});
