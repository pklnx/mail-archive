// SMTP secrets in the web UI (#395 part 3) against the demo server: a fresh
// admin adds an smtp account, creates its secret with the password and a
// code, sees it once (copy, then gone after Done and after a reload),
// finds it in the admins' list on the Users page and revokes it.
import { randomUUID } from "node:crypto";
import type { APIRequestContext } from "@playwright/test";
import { origin, timeStep, totp, unique } from "./demo";
import { expect, test } from "./fixtures";
import { adminState } from "./global-setup";

const headers = { Origin: origin };

// The test's own page signs in as the new admin; the demo admin acts
// through a context of its own.
test.use({ storageState: { cookies: [], origins: [] } });

/** A new admin in the group Default, with its generated password. */
async function newAdmin(admin: APIRequestContext): Promise<{ name: string; password: string }> {
  const groups = (await (await admin.get("/api/groups")).json()) as { groups: { id: number; name: string }[] };
  const group = groups.groups.find((g) => g.name === "Default")!;
  const res = await admin.post("/api/users", { data: { name: unique("smtpadm"), admin: true, group: group.id }, headers });
  expect(res.status(), await res.text()).toBe(201);
  return (await res.json()) as { name: string; password: string };
}

test("an admin creates an SMTP secret, sees it once, and revokes it", async ({ page, browser, guards }) => {
  // The login page asks for a session first: 401 while signed out.
  guards.allowHTTPError("/api/session", 401);
  const adminCtx = await browser.newContext({ baseURL: origin, storageState: adminState, locale: "en-US", timezoneId: "Europe/Berlin" });
  try {
    const user = await newAdmin(adminCtx.request);
    // Made at run time, so that no literal in the repository is a password.
    const password = `pw-${randomUUID()}`;
    const account = unique("journal");
    const address = `archive@${account}.archive.local`;

    // The first sign-in: an own password and the mandatory 2FA.
    await page.goto("/");
    await page.getByLabel("User name").fill(user.name);
    await page.getByLabel("Password", { exact: true }).fill(user.password);
    await page.getByRole("button", { name: "Log in" }).click();
    await expect(page.getByRole("heading", { name: "Choose your own password" })).toBeVisible();
    await page.getByLabel("Current password", { exact: true }).fill(user.password);
    await page.getByLabel("New password", { exact: true }).fill(password);
    await page.getByLabel("Repeat new password", { exact: true }).fill(password);
    await page.getByRole("button", { name: "Change password" }).click();
    await page.getByRole("button", { name: "Set up authenticator app" }).click();
    const totpSecret = (await page.locator(".two-factor__key code").textContent())!.trim();
    const setupStep = timeStep();
    await page.getByLabel("Authentication code").fill(totp(totpSecret, setupStep));
    await page.getByRole("button", { name: "Enable two-factor authentication" }).click();
    await page.getByRole("button", { name: "I have saved the codes" }).click();

    // A new smtp account: its setup logs in as its user name.
    await page.goto("/?view=accounts&add=1");
    const main = page.getByRole("main");
    // Carbon's radio input is hidden behind its label.
    await main.getByText("SMTP journaling: complete").click();
    await expect(main.getByRole("radio", { name: /^SMTP journaling/ })).toBeChecked();
    await main.getByLabel("Name", { exact: true }).fill(account);
    await main.getByLabel("Addresses 1").fill(address);
    await main.getByRole("button", { name: "Save" }).click();
    const lines = main.getByRole("region", { name: "Mail server setup" });
    const mapLine = lines.getByText(/\[archive\.customer\.example\]:587\s+smtp-\d+:mas_…/);
    await expect(mapLine).toBeVisible();
    const username = /(smtp-\d+):mas_…/.exec((await mapLine.textContent())!)![1]!;
    await main.getByRole("button", { name: "Done" }).click();

    // The account's page: no secret yet.
    await page.goto(`/?view=accounts&id=${account}`);
    const section = main.getByRole("region", { name: "SMTP secrets" });
    await expect(section.getByText("No secret yet: a mail server cannot log in for this account.")).toBeVisible();
    await expect(section.getByText(username, { exact: true })).toBeVisible();

    // New secret: the password and a code of a later time step than the setup's.
    await section.getByRole("button", { name: "New secret" }).click();
    const dialog = page.getByRole("dialog", { name: `New SMTP secret for “${account}”` });
    await dialog.getByLabel("Current password", { exact: true }).fill(password);
    await dialog.getByLabel("Authentication code").fill(totp(totpSecret, Math.max(setupStep + 1, timeStep())));
    await dialog.getByRole("button", { name: "Create secret" }).click();
    await expect(dialog.getByText(/^Copy the secret now: it is shown only once\./)).toBeVisible();
    const secretBox = dialog.getByLabel("Secret", { exact: true });
    const secret = (await secretBox.locator("code").textContent())!.trim();
    expect(secret).toMatch(/^mas_[a-z2-7]{52}$/);
    await expect(dialog.getByText(`${username}:${secret}`)).toBeVisible();
    await secretBox.getByRole("button", { name: "Copy to clipboard" }).click();
    await expect(secretBox.getByText("Copied", { exact: true })).toBeVisible();
    // Behind the dialog the list has it, by its last four characters only.
    await expect(section.getByRole("cell", { name: `mas_…${secret.slice(-4)}`, exact: true })).toBeAttached();

    await dialog.getByRole("button", { name: "Done" }).click();
    await expect(dialog).toBeHidden();
    const noSecret = async () => {
      expect(await page.content()).not.toContain(secret);
      expect(page.url()).not.toContain(secret);
      const stored = await page.evaluate(() =>
        [localStorage, sessionStorage].flatMap((s) => Object.keys(s).map((k) => `${k}=${s.getItem(k) ?? ""}`)).join("\n"),
      );
      expect(stored).not.toContain(secret);
    };
    const row = section.getByRole("row").filter({ hasText: `mas_…${secret.slice(-4)}` });
    await expect(row.getByText("active", { exact: true })).toBeVisible();
    await noSecret();
    await page.reload();
    await expect(row.getByText("active", { exact: true })).toBeVisible();
    await noSecret();

    // The demo admin's list on the Users page has it, with its account and owner.
    const users = await adminCtx.newPage();
    await users.goto("/?view=users");
    const all = users.getByRole("main").getByRole("region", { name: "SMTP secrets" });
    const listed = all.getByRole("row").filter({ hasText: account });
    await expect(listed.getByRole("cell", { name: user.name, exact: true })).toBeVisible();
    await expect(listed.getByRole("cell", { name: username, exact: true })).toBeVisible();

    // Revoke on the account's page, after asking.
    await row.getByRole("button", { name: `Revoke mas_…${secret.slice(-4)}` }).click();
    const ask = page.getByRole("dialog", { name: `Revoke the SMTP secret mas_…${secret.slice(-4)}?` });
    await ask.getByRole("button", { name: "Revoke" }).click();
    await expect(section.getByText(`The SMTP secret mas_…${secret.slice(-4)} is revoked.`)).toBeVisible();
    await expect(row.getByText("revoked", { exact: true })).toBeVisible();
    await expect(section.getByRole("heading", { name: "SMTP secrets", level: 2 })).toBeFocused();
    await users.reload();
    await expect(listed.getByText("revoked", { exact: true })).toBeVisible();
    await expect(listed.getByRole("button", { name: /^Revoke/ })).toHaveCount(0);
  } finally {
    await adminCtx.close();
  }
});
