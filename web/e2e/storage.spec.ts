import type { Browser, Page } from "@playwright/test";
import { origin, shareOwner } from "./demo";
import { expect, test } from "./fixtures";

// The Storage page and the storage banners (#397, part 4). The demo
// (tools/demo) has an instance quota of 1 GiB, two customer groups, and a
// quota of 512 MiB for the second one; pat is a member of "Default", with
// the demo admin.
const secondGroup = "Lindqvist Design";

/** A page of its own for a member, in a context without the admin's session. */
async function memberPage(browser: Browser) {
  const ctx = await browser.newContext({ baseURL: origin, storageState: { cookies: [], origins: [] }, locale: "en-US", timezoneId: "Europe/Berlin" });
  const page = await ctx.newPage();
  const res = await page.request.post("/api/session", { data: { username: shareOwner.name, password: shareOwner.password }, headers: { Origin: origin } });
  expect(res.ok(), await res.text()).toBe(true);
  return page;
}

/** Sets Default's quota on the Storage page: a size in MiB, or none. */
async function setQuota(page: Page, mib: string | null) {
  const main = page.getByRole("main");
  await main.getByRole("button", { name: "Actions for group Default" }).click();
  await page.getByRole("menuitem", { name: "Edit quota" }).click();
  const dialog = page.getByRole("dialog", { name: "Storage quota of “Default”" });
  const none = dialog.getByRole("checkbox", { name: "No quota (only the instance quota applies)" });
  // Carbon's checkbox input is visually hidden behind its label.
  if ((mib === null) !== (await none.isChecked())) await dialog.getByText("No quota (only the instance quota applies)").click();
  if (mib !== null) {
    await dialog.getByLabel("Unit").selectOption("MiB");
    await dialog.getByRole("textbox", { name: "Quota" }).fill(mib);
  }
  return dialog;
}

test("an admin sets a group quota; the group's member sees that new mail is not archived, and the quota is removed again", async ({ page, browser }) => {
  await page.goto("/?view=storage");
  const main = page.getByRole("main");
  await expect(main.getByRole("heading", { name: "Storage", level: 1 })).toBeVisible();
  await expect(main.getByText(/^Usage: .* of 1\.0 GiB \(\d+ %\)$/)).toBeVisible();
  const groups = main.getByRole("table", { name: "Customer groups" });
  await expect(groups.getByRole("row").filter({ hasText: secondGroup })).toContainText("512 MiB");
  const accounts = main.getByRole("table", { name: "Accounts" });
  await expect(accounts.getByRole("row").filter({ hasText: "team" })).toContainText("pat");

  const member = await memberPage(browser);
  try {
    // A quota below what Default holds: the dialog says it pauses at once.
    const dialog = await setQuota(page, "0.001");
    await expect(dialog.getByText(/Its syncs and imports pause at once/)).toBeVisible();
    await dialog.getByRole("button", { name: "Save quota" }).click();
    await expect(dialog).toHaveCount(0);
    await expect(main.getByRole("status")).toHaveText("The quota of Default is now 1.0 KiB.");
    const row = groups.getByRole("row").filter({ hasText: "Default" });
    // Far over the quota plus the overflow share: journal mail is deferred too.
    await expect(row).toContainText("Full: journal mail deferred");
    await expect(row).toContainText("1.0 KiB");

    // The member: the banner without the cause, and no way to the Storage page.
    await member.goto("/");
    const text = "New mail is not being archived; mail deleted on the server meanwhile is not archived.";
    const banner = member.locator(".health-banner");
    // The line; the live region that announces it holds the same text.
    await expect(banner.locator(".cds--actionable-notification__subtitle")).toHaveText(text);
    await expect(banner.getByRole("button")).toHaveCount(0);
    const usage = (await (await member.request.get("/api/usage")).json()) as Record<string, unknown>;
    expect(Object.keys(usage).sort()).toEqual(["accounts", "state"]);
    expect(usage.state).toBe("overflow");

    // The admin's banner names the group and leads to the page.
    await page.goto("/");
    const adminBanner = page.locator(".health-banner");
    await expect(adminBanner.locator(".cds--actionable-notification__subtitle", { hasText: /^Storage limit reached \(Default\)/ })).toBeVisible();
    await adminBanner.getByRole("button", { name: "Open storage" }).click();
    await expect(main.getByRole("heading", { name: "Storage", level: 1 })).toBeVisible();
  } finally {
    // No quota again: the rest of the suite syncs Default's accounts.
    const dialog = await setQuota(page, null);
    await dialog.getByRole("button", { name: "Save quota" }).click();
    await expect(main.getByRole("status")).toHaveText("Default has no quota now.");
    await member.context().close();
  }
  await expect(groups.getByRole("row").filter({ hasText: "Default" })).toContainText("OK");
  await page.goto("/");
  await expect(page.locator(".cds--actionable-notification__subtitle", { hasText: /^Storage limit reached/ })).toHaveCount(0);
});

test("the Users page shows each group's quota", async ({ page }) => {
  await page.goto("/?view=users");
  const groups = page.getByRole("main").getByRole("table", { name: "Groups" });
  await expect(groups.getByRole("columnheader", { name: "Storage quota" })).toBeVisible();
  await expect(groups.getByRole("row").filter({ hasText: secondGroup })).toContainText("512 MiB");
});
