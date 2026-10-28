import { imapPort, spareLogin, unique } from "./demo";
import { accountRow, expect, test } from "./fixtures";

test("add an IMAP account, choose folders on its page, sync, remove it, delete it completely", async ({ page }) => {
  const name = unique("e2e");
  await page.goto("/?view=accounts");
  const main = page.getByRole("main");
  const listTitle = main.getByRole("heading", { name: "Accounts", level: 1 });
  await expect(listTitle).toBeVisible();

  // Adding is a page under Accounts, in the URL.
  await main.getByRole("button", { name: "Add account" }).click();
  await expect(page).toHaveURL(/\?view=accounts&add=1$/);
  await expect(main.getByRole("heading", { name: "Add account", level: 1 })).toBeFocused();
  await expect(main.getByRole("radio", { name: /^IMAP mailbox/ })).toBeChecked();
  await main.getByLabel("Name", { exact: true }).fill(name);
  await main.getByLabel("IMAP server", { exact: true }).fill("127.0.0.1");
  await main.getByLabel("Connection security").selectOption({ label: "None (insecure)" });
  await main.getByLabel("Port", { exact: true }).fill(String(imapPort));
  await main.getByLabel("Username", { exact: true }).fill(spareLogin.username);
  await main.getByLabel("Password", { exact: true }).fill(spareLogin.password);
  await main.getByRole("button", { name: "Save" }).click();

  // Saving checks the login, syncs the account (2 + 1 + 1 messages) and
  // returns to the list, where the account is a row of the table.
  await expect(page).toHaveURL(/\?view=accounts$/);
  const row = accountRow(page, name);
  await expect(row.getByRole("button", { name: "snapshot (IMAP)" })).toBeVisible();
  await expect(row.getByRole("cell", { name: "4", exact: true })).toBeVisible();
  await expect(row.getByText(/^Last sync .*: \d+ new$/)).toBeVisible();
  // The second line under the name is for phones only.
  await expect(row.locator(".accounts-table__more")).toBeHidden();

  // The name leads to the account's page.
  await row.getByRole("link", { name, exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`\\?view=accounts&id=${name}$`));
  const title = main.getByRole("heading", { name, level: 1 });
  await expect(title).toBeFocused();
  await expect(main.getByRole("button", { name: "Sync", exact: true })).toBeVisible();
  await expect(main.getByRole("button", { name: "Check server" })).toHaveAttribute("title", /compare every folder/);
  const connection = main.getByRole("region", { name: "Connection" });
  await expect(connection.getByText(spareLogin.username, { exact: true })).toBeVisible();

  // Leave Trash out of the next syncs: Folders has its own Edit and Save.
  const folders = main.getByRole("region", { name: "Folders" });
  await folders.getByRole("button", { name: "Edit" }).click();
  await expect(folders.getByRole("checkbox", { name: "INBOX" })).toBeChecked();
  await expect(folders.getByRole("checkbox", { name: "Receipts" })).toBeChecked();
  // Meanwhile a choice in Schedule stays unsaved.
  const schedule = main.getByRole("region", { name: "Schedule" });
  await schedule.getByRole("button", { name: "Edit" }).click();
  await schedule.getByLabel("Schedule").selectOption({ label: "Every 30 minutes" });
  // Carbon draws the box on the label, over the input: a person clicks that.
  const trash = folders.getByRole("checkbox", { name: "Trash" });
  await folders.locator(`label[for="${await trash.getAttribute("id")}"]`).click();
  await expect(trash).not.toBeChecked();
  await folders.getByRole("button", { name: "Save folder selection" }).click();
  await expect(folders.getByText("Saved.", { exact: true })).toBeVisible();
  await expect(folders.getByText("Not archived: Trash")).toBeVisible();
  // The page stays, and so does the unsaved schedule choice.
  await expect(page).toHaveURL(new RegExp(`\\?view=accounts&id=${name}$`));
  await expect(schedule.getByLabel("Schedule")).toHaveValue("30m0s");
  await schedule.getByRole("button", { name: "Cancel" }).click();
  await expect(schedule.getByRole("button", { name: "Edit" })).toBeFocused();

  // The selection is stored. A reload keeps the page; the breadcrumb leads back.
  await page.reload();
  await expect(title).toBeVisible();
  await expect(folders.getByText("Not archived: Trash")).toBeVisible();
  await folders.getByRole("button", { name: "Edit" }).click();
  await expect(folders.getByRole("checkbox", { name: "Trash" })).not.toBeChecked();
  await expect(folders.getByRole("checkbox", { name: "INBOX" })).toBeChecked();
  await main.getByRole("navigation", { name: "Breadcrumb" }).getByRole("link", { name: "Accounts" }).click();
  await expect(page).toHaveURL(/\?view=accounts$/);
  await expect(listTitle).toBeFocused();

  // A manual sync from the row finds nothing new; the archived mail stays.
  const synced = page.waitForResponse((r) => r.url().endsWith(`/api/accounts/${name}/sync`) && r.request().method() === "POST");
  await row.getByRole("button", { name: "Sync", exact: true }).click();
  expect((await synced).ok()).toBe(true);
  await expect(row.getByText(/^Last sync .*: 0 new$/)).toBeVisible();
  await expect(row.getByRole("cell", { name: "4", exact: true })).toBeVisible();

  // The account also shows in the mail view's folder tree, with its folders.
  const nav = page.getByRole("navigation", { name: "Main navigation" });
  const tree = page.getByRole("tree", { name: "Accounts and folders" });
  const accountNode = (label: RegExp) => tree.getByRole("treeitem", { name: label });
  await nav.getByRole("link", { name: "All mail" }).click();
  const node = accountNode(new RegExp(`^${name}$`));
  await expect(node).toHaveAttribute("aria-expanded", "true");
  await expect(node.getByRole("treeitem", { name: /^INBOX \d+$/ })).toBeVisible();
  await nav.getByRole("link", { name: "Accounts" }).click();
  await expect(page).toHaveURL(/\?view=accounts$/);

  // Removing is in the row's menu and asks first, in a danger dialog; the mail stays in the archive.
  const menu = row.getByRole("button", { name: `Actions for ${name}` });
  await menu.click();
  await page.getByRole("menuitem", { name: "Remove" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: `Remove “${name}”?` })).toBeVisible();
  // The safe button has the focus.
  await expect(dialog.getByRole("button", { name: "Cancel" })).toBeFocused();
  const removing = page.waitForResponse((r) => r.url().endsWith(`/api/accounts/${name}`) && r.request().method() === "DELETE");
  await dialog.getByRole("button", { name: "Remove", exact: true }).click();
  expect((await removing).ok()).toBe(true);
  await expect(dialog).toHaveCount(0);
  // The row went: the focus is on the list's h1, not on the body.
  await expect(listTitle).toBeFocused();
  // The removed accounts open to show where it went. The demo's
  // old-provider is listed there already; the row must be ours.
  await expect(main.getByRole("button", { name: /^Removed accounts \(\d+\)$/ })).toHaveAttribute("aria-expanded", "true");
  const removedTable = main.getByRole("table", { name: "Removed accounts" });
  await expect(removedTable.getByRole("link", { name, exact: true })).toBeVisible();
  await expect(main.getByText("Removed. Its mail stays in the archive.")).toBeVisible();
  await nav.getByRole("link", { name: "All mail" }).click();
  await expect(accountNode(new RegExp(`^${name} removed$`))).toBeVisible();
  await nav.getByRole("link", { name: "Accounts" }).click();
  // Back on a fresh Accounts page, the removed accounts start folded.
  const removedAccounts = main.getByRole("button", { name: /^Removed accounts \(\d+\)$/ });
  await expect(removedAccounts).toHaveAttribute("aria-expanded", "false");
  await removedAccounts.click();
  await expect(removedAccounts).toHaveAttribute("aria-expanded", "true");

  // Deleting completely needs the exact count, typed into the dialog.
  await row.getByRole("button", { name: `Actions for ${name}` }).click();
  await page.getByRole("menuitem", { name: "Delete completely…" }).click();
  await expect(dialog.getByRole("heading", { name: `Delete “${name}” completely?` })).toBeVisible();
  const field = dialog.getByLabel(/^Type \d+ to confirm$/);
  await expect(field).toBeFocused();
  const label = (await dialog.getByText(/^Type \d+ to confirm$/).textContent()) ?? "";
  const count = label.match(/\d+/)?.[0] ?? "";
  // Its 4 messages are in no other account.
  expect(count).toBe("4");
  const confirm = dialog.getByRole("button", { name: "Delete completely", exact: true });
  await expect(confirm).toBeDisabled();
  await field.fill("5");
  await expect(confirm).toBeDisabled();
  await field.fill(count);
  await expect(confirm).toBeEnabled();
  const purging = page.waitForResponse((r) => r.url().endsWith(`/api/accounts/${name}/purge`) && r.request().method() === "POST");
  await confirm.click();
  expect((await purging).ok()).toBe(true);
  await expect(dialog).toHaveCount(0);
  // The server deletes in the background; then the row is gone, and the
  // focus that sat on its name goes to the accordion's button.
  await expect(row).toHaveCount(0);
  await expect(removedAccounts).toBeFocused();
  await nav.getByRole("link", { name: "All mail" }).click();
  await expect(accountNode(/^work$/)).toBeVisible();
  await expect(accountNode(new RegExp(`^${name}( removed)?$`))).toHaveCount(0);
});

test("the smtp account's page: the mail server setup from the row's menu, and the imports", async ({ page }) => {
  await page.goto("/?view=accounts");
  const main = page.getByRole("main");
  const row = accountRow(page, "journal");
  await row.getByRole("button", { name: "Actions for journal" }).click();
  await page.getByRole("menuitem", { name: "Mail server setup" }).click();
  await expect(page).toHaveURL(/\?view=accounts&id=journal$/);
  // The focus goes to that section, not to the page's h1.
  const setup = main.getByRole("heading", { name: "Mail server setup", level: 2 });
  await expect(setup).toBeFocused();
  await expect(main.getByRole("region", { name: "Mail server setup" }).getByText(/^Postfix, recipient_bcc_maps/)).toBeVisible();
  const imports = main.getByRole("region", { name: "Imports from IMAP" });
  await imports.getByRole("button", { name: "Import existing mail from IMAP" }).click();
  await expect(page).toHaveURL(/\?view=accounts&import=journal$/);
  await expect(main.getByRole("heading", { name: "Import existing mail from IMAP", level: 1 })).toBeFocused();
});

test("on a phone the accounts table and an account's page fit the width", async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 800 });
  await page.goto("/?view=accounts");
  const main = page.getByRole("main");
  const row = accountRow(page, "personal");
  await expect(row.getByRole("link", { name: "personal", exact: true })).toBeVisible();
  const noSideScroll = () => page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth);
  expect(await noSideScroll()).toBe(true);
  // The secondary columns give way to the second line under the name.
  await expect(main.getByRole("columnheader", { name: "Last activity" })).toBeHidden();
  await expect(row.locator(".accounts-table__more")).toBeVisible();
  await expect(row.locator(".accounts-table__more")).toHaveText(/ messages · /);
  // The row's menu is on screen and opens.
  const menu = row.getByRole("button", { name: "Actions for personal" });
  await expect(menu).toBeInViewport({ ratio: 1 });
  await menu.click();
  await expect(page.getByRole("menuitem", { name: "Check server" })).toBeInViewport();
  await page.keyboard.press("Escape");

  await row.getByRole("link", { name: "personal", exact: true }).click();
  await expect(main.getByRole("heading", { name: "personal", level: 1 })).toBeVisible();
  expect(await noSideScroll()).toBe(true);
  // The tiles wrap onto more than one line, each inside the window.
  const tiles = await main.locator(".app-summary__tile").evaluateAll((els) => els.map((el) => el.getBoundingClientRect()).map((r) => ({ top: r.top, left: r.left, right: r.right })));
  expect(tiles.length).toBeGreaterThanOrEqual(3);
  expect(new Set(tiles.map((r) => r.top)).size).toBeGreaterThan(1);
  for (const r of tiles) expect(r.left >= 0 && r.right <= 375).toBe(true);
  await main.getByRole("region", { name: "Connection" }).getByRole("button", { name: "Edit" }).click();
  expect(await noSideScroll()).toBe(true);
});
