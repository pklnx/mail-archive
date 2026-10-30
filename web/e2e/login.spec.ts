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

// The demo's public URL is the test origin, so the login page offers passkeys.
const passkeyButton = (page: Page) => page.getByRole("button", { name: "Sign in with a passkey" });

test("login with password and authentication code", async ({ page }) => {
  await page.goto("/");
  await expect(passkeyButton(page)).toBeVisible();
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
  const user = page.getByRole("button", { name: `User (${twoFactorUser.name})`, exact: true });
  await expect(user).toBeVisible();
  await expect(page.getByRole("searchbox", { name: "Search mail" })).toBeVisible();
  // Log out is in the user panel (#284).
  await user.click();
  await page.getByRole("list", { name: `User (${twoFactorUser.name})` }).getByRole("button", { name: "Log out" }).click();
  await expect(page.getByLabel("User name")).toBeVisible();
  // After Log out the passkey option is back without a reload (#380).
  await expect(passkeyButton(page)).toBeVisible();
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

test("the built assets are served as files only", async ({ request }) => {
  // The built binary, whose embedded build holds the real hashed files
  // (the Go tests run without a build). The request fixture, not a page
  // navigation: a 404 is the expected answer here, and the guards fail a
  // test on the console error a page navigation to it logs (#250).
  const html = await (await request.get("/")).text();
  const script = /\ssrc="(\/assets\/[^"]+\.js)"/.exec(html)?.[1];
  const style = /\shref="(\/assets\/[^"]+\.css)"/.exec(html)?.[1];
  expect(script, "index.html names a script under /assets/").toBeTruthy();
  expect(style, "index.html names a style sheet under /assets/").toBeTruthy();
  const immutable = "public, max-age=31536000, immutable";

  // The host's type table decides the exact JavaScript type string.
  const js = await request.get(script!);
  expect(js.status()).toBe(200);
  expect(js.headers()["content-type"]).toMatch(/^(text|application)\/javascript/);
  expect(js.headers()["cache-control"]).toBe(immutable);
  expect(js.headers()["cross-origin-resource-policy"]).toBe("same-origin");
  expect((await js.body()).length).toBeGreaterThan(0);
  const css = await request.get(style!);
  expect(css.status()).toBe(200);
  expect(css.headers()["content-type"]).toMatch(/^text\/css/);
  expect(css.headers()["cache-control"]).toBe(immutable);

  // No listing of the folder, and no redirect: a plain, uncached 404.
  const name = script!.slice("/assets/".length);
  for (const path of ["/assets/", "/assets/x/index.html", "/assets/nope.js"]) {
    const resp = await request.get(path, { maxRedirects: 0 });
    expect(resp.status(), path).toBe(404);
    expect(resp.headers()["cache-control"], path).toBe("no-cache");
    expect(resp.headers()["cross-origin-resource-policy"], path).toBe("same-origin");
    expect(await resp.text(), path).not.toContain(name);
  }

  // The mux routes on the decoded path: the folder under another spelling
  // is the same file.
  const encoded = await request.get(script!.replace("/assets/", "/%61ssets/"));
  expect(encoded.status()).toBe(200);
  expect(encoded.headers()["cache-control"]).toBe(immutable);
});
