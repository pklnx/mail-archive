import { imapPort, spareLogin, unique } from "./demo";
import { accountCard, expect, test } from "./fixtures";

test("add an IMAP account, choose folders, sync, remove it, delete it completely", async ({ page }) => {
  const name = unique("e2e");
  await page.goto("/?view=accounts");
  const main = page.getByRole("main");
  await expect(main.getByRole("heading", { name: "Accounts", level: 1 })).toBeVisible();

  // Adding is a page under Accounts, in the URL.
  await main.getByRole("button", { name: "Add account" }).click();
  await expect(page).toHaveURL(/\?view=accounts&add=1$/);
  await expect(main.getByRole("heading", { name: "Add account", level: 1 })).toBeVisible();
  await expect(main.getByRole("radio", { name: /^IMAP mailbox/ })).toBeChecked();
  await main.getByLabel("Name", { exact: true }).fill(name);
  await main.getByLabel("IMAP server", { exact: true }).fill("127.0.0.1");
  await main.getByLabel("Connection security").selectOption({ label: "None (insecure)" });
  await main.getByLabel("Port", { exact: true }).fill(String(imapPort));
  await main.getByLabel("Username", { exact: true }).fill(spareLogin.username);
  await main.getByLabel("Password", { exact: true }).fill(spareLogin.password);
  await main.getByRole("button", { name: "Save" }).click();

  // Saving checks the login, syncs the account (2 + 1 + 1 messages) and
  // returns to the list.
  await expect(page).toHaveURL(/\?view=accounts$/);
  const card = accountCard(page, name);
  await expect(card.getByText(`${spareLogin.username} · 127.0.0.1`, { exact: true })).toBeVisible();
  await expect(card.getByRole("button", { name: "snapshot (IMAP)" })).toBeVisible();
  await expect(card.getByText("4 messages", { exact: true })).toBeVisible();
  await expect(card.getByText(/^Last sync .*: \d+ new$/)).toBeVisible();

  // Leave Trash out of the next syncs, on the account's Edit page.
  const editing = main.getByRole("heading", { name: `Edit “${name}”`, level: 1 });
  await card.getByRole("button", { name: "Edit" }).click();
  await expect(page).toHaveURL(new RegExp(`[?&]edit=${name}$`));
  await expect(editing).toBeVisible();
  await main.getByRole("button", { name: "Choose folders" }).click();
  await expect(main.getByRole("checkbox", { name: "INBOX" })).toBeChecked();
  await expect(main.getByRole("checkbox", { name: "Receipts" })).toBeChecked();
  // Carbon draws the box on the label, over the input: a person clicks that.
  const trash = main.getByRole("checkbox", { name: "Trash" });
  await main.locator(`label[for="${await trash.getAttribute("id")}"]`).click();
  await expect(trash).not.toBeChecked();
  await main.getByRole("button", { name: "Save folder selection" }).click();
  await expect(main.getByText("Saved.", { exact: true })).toBeVisible();
  await main.getByRole("button", { name: "Cancel" }).click();
  await expect(page).toHaveURL(/\?view=accounts$/);

  // The selection is stored. A reload keeps the page; the breadcrumb leads back.
  await card.getByRole("button", { name: "Edit" }).click();
  await page.reload();
  await expect(editing).toBeVisible();
  await main.getByRole("button", { name: "Choose folders" }).click();
  await expect(main.getByRole("checkbox", { name: "Trash" })).not.toBeChecked();
  await expect(main.getByRole("checkbox", { name: "INBOX" })).toBeChecked();
  await main.getByRole("navigation", { name: "Breadcrumb" }).getByRole("link", { name: "Accounts" }).click();
  await expect(page).toHaveURL(/\?view=accounts$/);

  // A manual sync finds nothing new; the archived mail stays.
  const synced = page.waitForResponse((r) => r.url().endsWith(`/api/accounts/${name}/sync`) && r.request().method() === "POST");
  await card.getByRole("button", { name: "Sync", exact: true }).click();
  expect((await synced).ok()).toBe(true);
  await expect(card.getByText(/^Last sync .*: 0 new$/)).toBeVisible();
  await expect(card.getByText("4 messages", { exact: true })).toBeVisible();

  // The account also shows in the sidebar, with its folders.
  const nav = page.getByRole("navigation", { name: "Accounts and folders" });
  await expect(nav.getByRole("button", { name: `Folders in ${name}` })).toBeVisible();

  // Removing asks first, in a danger dialog; the mail stays in the archive.
  await card.getByRole("button", { name: "Remove", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: `Remove “${name}”?` })).toBeVisible();
  // The safe button has the focus.
  await expect(dialog.getByRole("button", { name: "Cancel" })).toBeFocused();
  const removing = page.waitForResponse((r) => r.url().endsWith(`/api/accounts/${name}`) && r.request().method() === "DELETE");
  await dialog.getByRole("button", { name: "Remove", exact: true }).click();
  expect((await removing).ok()).toBe(true);
  await expect(dialog).toHaveCount(0);
  // The removed accounts open to show where it went. The demo's
  // old-provider is listed there already; the tile must be ours.
  await expect(main.getByRole("button", { name: /^Removed accounts \(\d+\)$/ })).toHaveAttribute("aria-expanded", "true");
  await expect(card).toHaveCount(1);
  await expect(card.getByRole("button", { name: "Edit" })).toHaveCount(0);
  await expect(card.getByText("Removed. Its mail stays in the archive.")).toBeVisible();
  await expect(card.getByText("4 messages", { exact: true })).toBeVisible();
  await expect(nav.getByRole("button", { name: new RegExp(`^${name} removed$`) })).toBeVisible();

  // Deleting completely needs the exact count, typed into the dialog.
  await card.getByRole("button", { name: "Delete completely…" }).click();
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
  // The server deletes in the background; then the account is gone.
  await expect(card).toHaveCount(0);
  await expect(nav.getByRole("button", { name: new RegExp(`^${name} removed$`) })).toHaveCount(0);
});
