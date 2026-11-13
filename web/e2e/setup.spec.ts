import { setupOrigin, setupToken, timeStep, totp } from "./demo";
import { expect, test } from "./fixtures";

// The setup link of a new instance (#396): the demo's second instance has no
// user and a setup token. A fresh browser without a session.
test.use({ storageState: { cookies: [], origins: [] } });

// The test's pages are the new instance's. Without a session, GET
// /api/session answers 401 by design.
test.beforeEach(({ guards }) => {
  guards.allowSetupInstance();
  guards.allowHTTPError("/api/session", 401);
});

test("the setup link creates the first admin, who sets up 2FA next", async ({ page }) => {
  await page.goto(`${setupOrigin}/#setup=${setupToken}`);
  await expect(page.getByRole("heading", { name: "Create the first admin" })).toBeVisible();
  // The token left the address bar at once.
  expect(new URL(page.url()).hash).toBe("");
  expect(page.url()).not.toContain(setupToken);

  await page.getByLabel("User name").fill("first-admin");
  await page.getByLabel("Password", { exact: true }).fill("correct horse battery staple");
  await page.getByLabel("Repeat password", { exact: true }).fill("correct horse battery staple");
  await page.getByRole("button", { name: "Create admin" }).click();

  // Logged in as the new admin, held at the 2FA setup like every admin.
  await expect(page.getByRole("heading", { name: "Set up authenticator app", level: 2 })).toBeVisible();
  // The 2FA: the secret from the page, a code of the current step.
  await page.getByRole("button", { name: "Set up authenticator app" }).click();
  const secret = (await page.locator(".two-factor__key code").textContent())!.trim();
  await page.getByLabel("Authentication code").fill(totp(secret, timeStep()));
  await page.getByRole("button", { name: "Enable two-factor authentication" }).click();
  await page.getByRole("button", { name: "I have saved the codes" }).click();
  // The new admin's empty archive.
  await expect(page.getByRole("searchbox", { name: "Search mail" })).toBeVisible();

  // The link is used: opened again without the session, it says so. A full
  // load: from the app's own URL only the fragment would change.
  await page.context().clearCookies();
  await page.goto("about:blank");
  await page.goto(`${setupOrigin}/#setup=${setupToken}`);
  await expect(page.getByText("This instance is already set up", { exact: false })).toBeVisible();
  await expect(page.getByRole("button", { name: "Create admin" })).toHaveCount(0);
  await page.getByRole("button", { name: "Go to login" }).click();
  await expect(page.getByLabel("User name")).toBeVisible();
});
