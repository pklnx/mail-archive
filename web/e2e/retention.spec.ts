import type { Page } from "@playwright/test";
import { unique } from "./demo";
import { expect, test } from "./fixtures";

const noSideScroll = (page: Page) => page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth);

// The demo's personal / Archive/2014 holds three twelve-year-old messages.
// No other test uses that folder, so deleting from it changes nothing else.
// Each tab is a table; rules and holds are added on pages of their own, and
// a run is reviewed and confirmed on its page (#285 part 3).
test("retention rule, legal hold, deletion run confirmed by count", async ({ page }) => {
  const note = unique("e2e rule");
  const hold = unique("e2e hold");
  const what = "demo / personal / Archive/2014";
  await page.goto("/?view=retention");
  const main = page.getByRole("main");
  await expect(main.getByRole("heading", { name: "Retention", level: 1 })).toBeVisible();
  await expect(main.getByRole("tab", { name: "Rules" })).toHaveAttribute("aria-selected", "true");

  // A ten-year rule for the old folder, on the Add rule page.
  await main.getByRole("button", { name: "Add rule" }).click();
  await expect(main.getByRole("heading", { name: "Add rule", level: 1 })).toBeVisible();
  await main.getByRole("combobox", { name: "Account", exact: true }).selectOption({ label: "demo / personal" });
  await main.getByRole("combobox", { name: "Folder", exact: true }).selectOption("Archive/2014");
  await main.getByLabel("Years").fill("10");
  await main.getByLabel("Note").fill(note);
  await main.getByRole("button", { name: "Add rule" }).click();
  await expect(page).toHaveURL(/\?view=retention$/);
  const rule = main.getByRole("row").filter({ hasText: note });
  await expect(rule.getByRole("rowheader")).toHaveText(what);
  await expect(rule).toContainText("10 years");
  // Its page.
  await rule.getByRole("link", { name: what }).click();
  await expect(main.getByRole("heading", { name: what, level: 1 })).toBeFocused();
  await expect(main.locator(".app-summary__tile").filter({ hasText: "Period" })).toContainText("10 years");
  await main.getByRole("navigation", { name: "Breadcrumb" }).getByRole("link", { name: "Retention" }).click();

  // A hold for the court mail in it, on the Add hold page.
  await main.getByRole("tab", { name: "Legal holds" }).click();
  await expect(page).toHaveURL(/tab=holds/);
  await main.getByRole("button", { name: "Add hold" }).click();
  await main.getByLabel("Name", { exact: true }).fill(hold);
  await main.getByLabel("Reason").fill("Case 4711");
  await main.getByRole("combobox", { name: "Account", exact: true }).selectOption({ label: "demo / personal" });
  await main.getByLabel("Search filter").fill("from:registry@court.example");
  await main.getByRole("button", { name: "Add hold" }).click();
  await expect(main.getByText("Hold added; the filter matched 1 message(s).")).toBeVisible();
  await expect(main.getByRole("tab", { name: "Legal holds" })).toHaveAttribute("aria-selected", "true");
  const held = main.getByRole("row").filter({ hasText: hold });
  await expect(held).toContainText("account personal (demo)");
  await expect(held).toContainText("from:registry@court.example (1 matched)");
  await expect(held).toContainText("active");

  // The run lists the two other messages, not the held one. It opens on
  // its page.
  await main.getByRole("tab", { name: "Deletion runs" }).click();
  await main.getByRole("button", { name: "New deletion run" }).click();
  const title = main.getByRole("heading", { name: /^Run \d+: 2 message\(s\)/, level: 1 });
  await expect(title).toBeVisible();
  await expect(page).toHaveURL(/tab=runs&id=\d+/);
  const groups = main.getByRole("region", { name: "Groups" });
  await expect(groups.getByRole("cell", { name: "demo / personal / Archive/2014" })).toBeVisible();

  // On a phone: no sideways scroll, the groups table fits, the tiles wrap.
  await page.setViewportSize({ width: 375, height: 800 });
  expect(await noSideScroll(page)).toBe(true);
  const box = groups.locator(".cds--data-table-content");
  expect(await box.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
  const tiles = await main.locator(".app-summary__tile").evaluateAll((els) => els.map((el) => el.getBoundingClientRect()).map((r) => ({ top: r.top, left: r.left, right: r.right })));
  expect(tiles).toHaveLength(5);
  expect(new Set(tiles.map((r) => r.top)).size).toBeGreaterThan(1);
  for (const r of tiles) expect(r.left >= 0 && r.right <= 375).toBe(true);
  await page.setViewportSize({ width: 1280, height: 720 });

  const open = main.getByRole("button", { name: "Delete 2 messages" });
  await open.click();
  const dialog = page.getByRole("dialog");
  const confirm = dialog.getByLabel("To delete, type the number of messages (2):");
  await expect(confirm).toBeFocused();
  const remove = dialog.getByRole("button", { name: "Delete", exact: true });
  await expect(remove).toBeDisabled();
  // Only the exact count enables the button.
  await confirm.fill("3");
  await expect(remove).toBeDisabled();
  await confirm.fill("2");
  await expect(remove).toBeEnabled();
  await remove.click();
  await expect(dialog).toHaveCount(0);
  await expect(title).toBeFocused();

  // The retention job deletes in the background; the page polls.
  await expect(main.locator(".app-detail-title__tags")).toHaveText("done", { timeout: 20_000 });
  await expect(main.locator(".app-summary__tile").filter({ hasText: "Deleted" })).toContainText("2");
  await main.getByRole("navigation", { name: "Breadcrumb" }).getByRole("link", { name: "Retention" }).click();
  const run = main.getByRole("row").filter({ hasText: /^Run \d+: 2 message\(s\)/ });
  await expect(run).toContainText("done");
  await expect(run).toContainText("2 deleted, 0 kept");
  await expect(main.getByRole("button", { name: "New deletion run" })).toBeVisible();

  // The held message is still in the archive, the others are gone.
  await page.goto("/?q=" + encodeURIComponent("hearing date"));
  await expect(page.getByRole("list", { name: "Messages" }).getByRole("button", { name: /Case 4711: hearing date/ })).toBeVisible();
  await page.goto("/?q=" + encodeURIComponent("tax assessment"));
  await expect(page.getByText("No messages match your search.")).toBeVisible();

  // Release the hold on its page: it stays, released.
  await page.goto("/?view=retention&tab=holds");
  await main.getByRole("link", { name: hold }).click();
  const holdTitle = main.getByRole("heading", { name: hold, level: 1 });
  await expect(holdTitle).toBeFocused();
  await main.getByRole("button", { name: `Actions for ${hold}` }).click();
  await page.getByRole("menuitem", { name: "Release" }).click();
  const release = page.getByRole("dialog", { name: `Release the hold “${hold}”?` });
  await release.getByLabel("Why is it released?").fill("e2e done");
  await release.getByRole("button", { name: "Release" }).click();
  await expect(release).toHaveCount(0);
  await expect(main.locator(".app-detail-title__tags")).toHaveText("released");
  await expect(holdTitle).toBeFocused();

  // Remove the rule from its row: the row goes, the focus is on the Rules tab.
  await page.goto("/?view=retention");
  await main.getByRole("button", { name: `Actions for ${what}` }).click();
  await page.getByRole("menuitem", { name: "Remove" }).click();
  const removeRule = page.getByRole("dialog", { name: `Remove the rule for ${what}?` });
  await removeRule.getByRole("button", { name: "Remove" }).click();
  await expect(main.getByRole("row").filter({ hasText: note })).toHaveCount(0);
  await expect(main.getByRole("tab", { name: "Rules" })).toBeFocused();
});

test("on a phone the retention tables fit the width and their menus are on screen", async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 800 });
  await page.goto("/?view=retention");
  const main = page.getByRole("main");
  const row = main.getByRole("row").filter({ hasText: "§147 AO" });
  await expect(row.getByRole("rowheader")).toBeVisible();
  expect(await noSideScroll(page)).toBe(true);
  // The note and the creation give way to the second line under the rule.
  await expect(main.getByRole("columnheader", { name: "Note" })).toBeHidden();
  await expect(row.locator(".retention-table__more")).toBeVisible();
  const menu = row.getByRole("button", { name: /^Actions for / });
  await expect(menu).toBeInViewport({ ratio: 1 });
  await menu.click();
  await expect(page.getByRole("menuitem", { name: "Remove" })).toBeInViewport();
  await page.keyboard.press("Escape");
  for (const tab of ["Legal holds", "Deletion runs"]) {
    await main.getByRole("tab", { name: tab }).click();
    await expect(main.getByRole("tab", { name: tab })).toHaveAttribute("aria-selected", "true");
    await expect(main.getByRole("tabpanel").getByRole("button", { name: tab === "Legal holds" ? "Add hold" : "New deletion run" })).toBeInViewport();
    expect(await noSideScroll(page)).toBe(true);
  }
});
