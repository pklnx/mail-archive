import { origin, unique } from "./demo";
import { expect, test } from "./fixtures";
import { menuAtRowEnd } from "./layout";

test("add a user, copy the generated password, lock and unlock", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin });
  const name = unique("e2e-user");
  await page.goto("/?view=users");
  const main = page.getByRole("main");
  await expect(main.getByRole("heading", { name: "Users", level: 1 })).toBeVisible();

  // Add user opens a Modal with the name and the admin choice.
  await main.getByRole("button", { name: "Add user" }).click();
  const add = page.getByRole("dialog", { name: "Add user" });
  await add.getByLabel("User name").fill(name);
  await expect(add.getByRole("checkbox", { name: "Admin" })).not.toBeChecked();
  await add.getByRole("button", { name: "Add user" }).click();
  await expect(add).toHaveCount(0);

  // The password shows once, with a copy button.
  const generated = main.getByRole("status").filter({ hasText: `Password for ${name}` });
  await expect(generated).toBeVisible();
  const password = (await generated.getByRole("code").textContent())?.trim() ?? "";
  expect(password.length).toBeGreaterThanOrEqual(12);
  await generated.getByRole("button", { name: "Copy to clipboard" }).click();
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(password);
  await generated.getByRole("button", { name: "Done" }).click();
  await expect(generated).toHaveCount(0);

  const user = main.getByRole("row").filter({ has: page.getByText(name, { exact: true }) });
  await expect(user).toContainText("must change password");

  // Locking asks first; unlocking does not.
  await user.getByRole("button", { name: `Actions for ${name}` }).click();
  await page.getByRole("menuitem", { name: "Lock" }).click();
  const confirm = page.getByRole("dialog", { name: `Lock “${name}”?` });
  await expect(confirm).toBeVisible();
  await expect(user.getByText("locked", { exact: true })).toHaveCount(0);
  await confirm.getByRole("button", { name: "Lock" }).click();
  await expect(user.getByText("locked", { exact: true })).toBeVisible();
  await user.getByRole("button", { name: `Actions for ${name}` }).click();
  await page.getByRole("menuitem", { name: "Unlock" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(user.getByText("locked", { exact: true })).toHaveCount(0);
  await expect(user).toContainText("must change password");

  // Removing from the row asks first; the row goes and the focus goes to the list's heading.
  await user.getByRole("button", { name: `Actions for ${name}` }).click();
  await page.getByRole("menuitem", { name: "Remove" }).click();
  const remove = page.getByRole("dialog", { name: `Remove “${name}”?` });
  await remove.getByRole("button", { name: "Remove" }).click();
  await expect(remove).toHaveCount(0);
  await expect(user).toHaveCount(0);
  await expect(main.getByRole("heading", { name: "Users", level: 1 })).toBeFocused();
});

test("a user's page: open it from the table, act from its menu, remove the user, back on the list", async ({ page }) => {
  const name = unique("e2e-detail");
  await page.goto("/?view=users");
  const main = page.getByRole("main");
  await main.getByRole("button", { name: "Add user" }).click();
  const add = page.getByRole("dialog", { name: "Add user" });
  await add.getByLabel("User name").fill(name);
  await add.getByRole("button", { name: "Add user" }).click();
  await expect(add).toHaveCount(0);
  await main.getByRole("status").getByRole("button", { name: "Done" }).click();

  // The name is the row's header and its link; the second line is for phones only.
  const row = main.getByRole("row").filter({ has: page.getByRole("rowheader", { name: new RegExp(`^${name}`) }) });
  await expect(row.locator(".users-table__more")).toBeHidden();
  await row.getByRole("link", { name, exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`\\?view=users&id=${name}$`));
  const title = main.getByRole("heading", { name, level: 1 });
  await expect(title).toBeFocused();
  await expect(main.getByText("Created", { exact: true })).toBeVisible();
  await expect(main.getByRole("region", { name: "API keys" }).getByText("No API keys yet.")).toBeVisible();

  // The breadcrumb is a navigation landmark; back through it the focus is on the list's h1, and on the page's h1 again (#330, #335 R2).
  const crumbs = main.getByRole("navigation", { name: "Breadcrumb", exact: true });
  await crumbs.getByRole("link", { name: "Users", exact: true }).click();
  await expect(page).toHaveURL(/\?view=users$/);
  await expect(main.getByRole("heading", { name: "Users", level: 1 })).toBeFocused();
  await row.getByRole("link", { name, exact: true }).click();
  await expect(title).toBeFocused();

  // Lock from the page's menu: it asks first.
  const menu = main.getByRole("button", { name: `Actions for ${name}` });
  await menu.click();
  await page.getByRole("menuitem", { name: "Lock" }).click();
  const lock = page.getByRole("dialog", { name: `Lock “${name}”?` });
  await lock.getByRole("button", { name: "Lock" }).click();
  await expect(lock).toHaveCount(0);
  await expect(title.locator("..").getByText("locked", { exact: true })).toBeVisible();
  // The menu item that opened the dialog is gone; the focus goes back to the menu's button.
  await expect(menu).toBeFocused();

  // Remove asks in a danger dialog; afterwards the list replaces the page.
  await menu.click();
  await page.getByRole("menuitem", { name: "Remove" }).click();
  const remove = page.getByRole("dialog", { name: `Remove “${name}”?` });
  await remove.getByRole("button", { name: "Remove" }).click();
  await expect(page).toHaveURL(/\?view=users$/);
  await expect(main.getByRole("heading", { name: "Users", level: 1 })).toBeFocused();
  await expect(main.getByRole("link", { name, exact: true })).toHaveCount(0);
});

test("on a phone the users table and a user's page fit the width", async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 800 });
  await page.goto("/?view=users");
  const main = page.getByRole("main");
  const row = main.getByRole("row").filter({ has: page.getByRole("rowheader", { name: /^kim/ }) });
  await expect(row.getByRole("link", { name: "kim", exact: true })).toBeVisible();
  const noSideScroll = () => page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth);
  expect(await noSideScroll()).toBe(true);
  // The secondary columns give way to the second line under the name.
  await expect(main.getByRole("columnheader", { name: "Role" })).toBeHidden();
  await expect(row.locator(".users-table__more")).toBeVisible();
  await expect(row.locator(".users-table__more")).toHaveText(/^user · /);
  // The row's menu is on screen and opens.
  const menu = row.getByRole("button", { name: "Actions for kim" });
  await expect(menu).toBeInViewport({ ratio: 1 });
  await menu.click();
  await expect(page.getByRole("menuitem", { name: "Reset password" })).toBeInViewport();
  await page.keyboard.press("Escape");

  await row.getByRole("link", { name: "kim", exact: true }).click();
  await expect(main.getByRole("heading", { name: "kim", level: 1 })).toBeVisible();
  expect(await noSideScroll()).toBe(true);
  // The tiles wrap onto more than one line, each inside the window.
  const tiles = await main.locator(".app-summary__tile").evaluateAll((els) => els.map((el) => el.getBoundingClientRect()).map((r) => ({ top: r.top, left: r.left, right: r.right })));
  expect(tiles).toHaveLength(5);
  expect(new Set(tiles.map((r) => r.top)).size).toBeGreaterThan(1);
  for (const r of tiles) expect(r.left >= 0 && r.right <= 375).toBe(true);
});

// A user's page on a phone in German (#369): "Passwort zurücksetzen" and the
// menu share a line, the menu at its end, down to 320 px, with no sideways
// scroll. The UI follows the browser's language while the user's is "auto".
test.describe("in German", () => {
  test.use({ locale: "de-DE" });
  for (const width of [320, 375, 402]) {
    test(`at ${width} px a user's page keeps its menu at the end of the actions row`, async ({ page }) => {
      await page.setViewportSize({ width, height: 800 });
      await page.goto("/?view=users");
      const main = page.getByRole("main");
      await expect(main.getByRole("heading", { name: "Benutzer", level: 1 })).toBeVisible();
      const row = main.getByRole("row").filter({ has: page.getByRole("rowheader", { name: /^kim/ }) });
      await row.getByRole("link", { name: "kim", exact: true }).click();
      await expect(main.getByRole("heading", { name: "kim", level: 1 })).toBeVisible();
      await expect(main.locator(".page-header__actions").getByRole("button", { name: "Passwort zurücksetzen" })).toBeVisible();
      await menuAtRowEnd(page, main, /^Aktionen für kim$/);
    });
  }
});
