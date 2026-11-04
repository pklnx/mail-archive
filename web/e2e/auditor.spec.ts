// Auditor access (#393 part 3) against the demo server: an owner grants
// access on the Auditor access page, and a fresh auditor with two grants
// goes through the first sign-in (own password, 2FA, the choice of a case)
// without a single refused request: until all three are done the server
// refuses everything else and records each refusal as auditor.denied
// (#441, coordinator note), so the UI must send nothing else.
import type { APIRequestContext, Browser, Page } from "@playwright/test";
import { origin, shareOwner, timeStep, totp, unique } from "./demo";
import { expect, test } from "./fixtures";

const headers = { Origin: origin };
const context = { baseURL: origin, storageState: { cookies: [], origins: [] }, locale: "en-US", timezoneId: "Europe/Berlin" };

/** The admin's API (the suite's stored session): a new auditor login in the owner's group, with its generated password. */
async function newAuditor(admin: APIRequestContext): Promise<{ name: string; password: string }> {
  const groups = (await (await admin.get("/api/groups")).json()) as { groups: { id: number; name: string }[] };
  const group = groups.groups.find((g) => g.name === "Default")!;
  const res = await admin.post("/api/users", { data: { name: unique("stb"), auditor: true, group: group.id }, headers });
  expect(res.status(), await res.text()).toBe(201);
  return (await res.json()) as { name: string; password: string };
}

/** pat's API: pat owns team and has no 2FA. */
async function asOwner(browser: Browser): Promise<APIRequestContext> {
  const ctx = await browser.newContext(context);
  const res = await ctx.request.post("/api/session", { data: { username: shareOwner.name, password: shareOwner.password }, headers });
  expect(res.ok(), await res.text()).toBe(true);
  return ctx.request;
}

async function grant(owner: APIRequestContext, auditor: string, label: string) {
  const endsAt = new Date(Date.now() + 7 * 86_400_000).toISOString();
  const res = await owner.post("/api/auditor-grants", {
    data: { auditor, label, accounts: [{ name: "team", folders: null }], endsAt, currentPassword: shareOwner.password, code: "" },
    headers,
  });
  expect(res.status(), await res.text()).toBe(201);
}

/** The auditor's own entries, as the admin reads them. */
async function entriesOf(admin: APIRequestContext, user: string): Promise<{ action: string; details: Record<string, unknown> }[]> {
  const res = await admin.get(`/api/audit?user=${encodeURIComponent(user)}&since=2000-01-01T00:00:00Z`);
  expect(res.ok(), await res.text()).toBe(true);
  return ((await res.json()) as { entries: { action: string; details: Record<string, unknown> }[] }).entries;
}

test("a fresh auditor with two grants sets up the login and chooses a case without one refused request", async ({ page, browser }) => {
  const admin = page.context().request;
  const auditor = await newAuditor(admin);
  const owner = await asOwner(browser);
  await grant(owner, auditor.name, "Betriebsprüfung 2021–2023");
  await grant(owner, auditor.name, "Umsatzsteuer 2024");

  const ctx = await browser.newContext(context);
  const a = await ctx.newPage();
  // The fixture's guards watch the test's own context only; the auditor's
  // context records every refused answer here (the login page's 401 for
  // GET /api/session is by design).
  const refused: string[] = [];
  a.on("response", (r) => {
    if (r.url().includes("/api/") && r.status() >= 400 && !(r.url().endsWith("/api/session") && r.status() === 401)) refused.push(`${r.status()} ${r.url()}`);
  });
  try {
    await a.goto("/");
    await a.getByLabel("User name").fill(auditor.name);
    await a.getByLabel("Password", { exact: true }).fill(auditor.password);
    await a.getByRole("button", { name: "Log in" }).click();

    // The own password.
    await expect(a.getByRole("heading", { name: "Choose your own password" })).toBeVisible();
    await a.getByLabel("Current password", { exact: true }).fill(auditor.password);
    await a.getByLabel("New password", { exact: true }).fill("a-new-auditor-password-1");
    await a.getByLabel("Repeat new password", { exact: true }).fill("a-new-auditor-password-1");
    await a.getByRole("button", { name: "Change password" }).click();

    // The mandatory 2FA: the secret from the page, a code of the current step.
    await a.getByRole("button", { name: "Set up authenticator app" }).click();
    const secret = (await a.locator(".two-factor__key code").textContent())!.trim();
    await a.getByLabel("Authentication code").fill(totp(secret, timeStep()));
    await a.getByRole("button", { name: "Enable two-factor authentication" }).click();
    await a.getByRole("button", { name: "I have saved the codes" }).click();

    // Two grants: the auditor picks one.
    await expect(a.getByRole("heading", { name: "Choose an audit case" })).toBeVisible();
    await a.getByText("Umsatzsteuer 2024 (pat)").click();
    await a.getByRole("button", { name: "Open" }).click();
    await expect(a.getByText(/Read-only access to “Umsatzsteuer 2024” of pat/)).toBeVisible();
    // The case's mail: team, as pat/team.
    await expect(a.getByRole("treeitem", { name: "team · pat" })).toBeVisible();
    await expect(a.getByRole("list", { name: "Messages" }).getByRole("button").first()).toBeVisible();
    const nav = a.getByRole("navigation", { name: "Main navigation" });
    await expect(nav.getByRole("link")).toHaveText(["All mail", "Log", "Profile"]);
  } finally {
    await ctx.close();
  }

  const entries = await entriesOf(admin, auditor.name);
  // The setup reached the server (the choice is recorded), and nothing was refused.
  expect(entries.map((e) => e.action)).toContain("auditor.select");
  expect(entries.filter((e) => e.action === "auditor.denied")).toEqual([]);
  expect(refused).toEqual([]);
});

/** pat's page, logged in through the API. */
async function ownerPage(browser: Browser): Promise<Page> {
  const ctx = await browser.newContext(context);
  const page = await ctx.newPage();
  const res = await page.request.post("/api/session", { data: { username: shareOwner.name, password: shareOwner.password }, headers });
  expect(res.ok(), await res.text()).toBe(true);
  return page;
}

test("the owner grants access on the Auditor access page, is told about a name outside the group, and revokes", async ({ page, browser }) => {
  const auditor = await newAuditor(page.context().request);
  const p = await ownerPage(browser);
  // The uniform answer for a name that is no auditor of pat's group (kim is in another group).
  const notFound = p.waitForResponse((r) => r.url().endsWith("/api/auditor-grants") && r.request().method() === "POST");
  try {
    await p.goto("/");
    await p.getByRole("navigation", { name: "Main navigation" }).getByRole("link", { name: "Auditor access" }).click();
    const main = p.getByRole("main");
    await expect(main.getByRole("heading", { name: "Auditor access", level: 1 })).toBeVisible();
    await main.getByRole("button", { name: "Grant access" }).click();
    const dialog = p.getByRole("dialog");
    await dialog.getByLabel("Auditor's login name").fill("kim");
    await dialog.getByLabel("Case", { exact: true }).fill("Jahresabschluss 2025");
    await dialog.getByText("team", { exact: true }).click();
    await dialog.getByLabel("Current password", { exact: true }).fill(shareOwner.password);
    await dialog.getByRole("button", { name: "Grant access" }).click();
    expect((await notFound).status()).toBe(404);
    await expect(dialog.getByText("There is no auditor with this name in your customer group.")).toBeVisible();

    await dialog.getByLabel("Auditor's login name").fill(auditor.name);
    await dialog.getByLabel("Current password", { exact: true }).fill(shareOwner.password);
    await dialog.getByRole("button", { name: "Grant access" }).click();
    await expect(dialog).toBeHidden();
    await expect(main.getByText(`${auditor.name} can now read “Jahresabschluss 2025”.`)).toBeVisible();
    const row = main.getByRole("row").filter({ hasText: "Jahresabschluss 2025" });
    await expect(row.getByText("Active")).toBeVisible();
    await expect(row.getByText("No access yet")).toBeVisible();

    await row.getByRole("button", { name: "Actions for Jahresabschluss 2025" }).click();
    await p.getByRole("menuitem", { name: "Revoke" }).click();
    await p.getByRole("dialog").getByRole("button", { name: "Revoke" }).click();
    await expect(main.getByText("“Jahresabschluss 2025” is revoked.")).toBeVisible();
    await expect(row.getByText("Ended")).toBeVisible();
    await expect(row.getByText("Revoked by you.")).toBeVisible();
  } finally {
    await p.context().close();
  }
});
