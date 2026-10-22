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
  await page.getByLabel("User name").fill(twoFactorUser.name);
  await passwordField(page).fill(twoFactorUser.password);
  await page.getByRole("button", { name: "Log in" }).click();

  await expect(page.getByRole("heading", { name: "Enter your authentication code" })).toBeVisible();
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
