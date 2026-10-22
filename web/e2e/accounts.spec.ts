import { imapPort, spareLogin, unique } from "./demo";
import { acceptDialog, accountCard, expect, test } from "./fixtures";

test("add an IMAP account, choose folders, sync, remove it", async ({ page }) => {
  const name = unique("e2e");
  await page.goto("/?view=accounts");
  const main = page.getByRole("main");
  await expect(main.getByRole("heading", { name: "Accounts", level: 1 })).toBeVisible();

  await main.getByRole("button", { name: "Add account" }).click();
  await expect(main.getByRole("heading", { name: "Add account", level: 2 })).toBeVisible();
  await expect(main.getByRole("radio", { name: /^IMAP mailbox/ })).toBeChecked();
  await main.getByLabel("Name", { exact: true }).fill(name);
  await main.getByLabel("IMAP server", { exact: true }).fill("127.0.0.1");
  await main.getByLabel("Connection security").selectOption({ label: "None (insecure)" });
  await main.getByLabel("Port", { exact: true }).fill(String(imapPort));
  await main.getByLabel("Username", { exact: true }).fill(spareLogin.username);
  await main.getByLabel("Password", { exact: true }).fill(spareLogin.password);
  await main.getByRole("button", { name: "Save" }).click();

  // Saving checks the login and syncs the account: 2 + 1 + 1 messages.
  const card = accountCard(page, name);
  await expect(card.getByText(`${spareLogin.username} · 127.0.0.1 · snapshot (IMAP)`)).toBeVisible();
  await expect(card.getByText("4 messages")).toBeVisible();
  await expect(card.getByText(/^Last sync .*: \d+ new$/)).toBeVisible();

  // Leave Trash out of the next syncs.
  // The edit form takes the card's place.
  const form = accountCard(page, name, "Save");
  await card.getByRole("button", { name: "Edit" }).click();
  await form.getByRole("button", { name: "Choose folders" }).click();
  await expect(form.getByRole("checkbox", { name: "INBOX" })).toBeChecked();
  await expect(form.getByRole("checkbox", { name: "Receipts" })).toBeChecked();
  await form.getByRole("checkbox", { name: "Trash" }).uncheck();
  await form.getByRole("button", { name: "Save folder selection" }).click();
  await expect(form.getByText("Saved.")).toBeVisible();
  await form.getByRole("button", { name: "Cancel" }).click();

  // The selection is stored.
  await card.getByRole("button", { name: "Edit" }).click();
  await form.getByRole("button", { name: "Choose folders" }).click();
  await expect(form.getByRole("checkbox", { name: "Trash" })).not.toBeChecked();
  await expect(form.getByRole("checkbox", { name: "INBOX" })).toBeChecked();
  await form.getByRole("button", { name: "Cancel" }).click();

  // A manual sync finds nothing new; the archived mail stays.
  const synced = page.waitForResponse((r) => r.url().endsWith(`/api/accounts/${name}/sync`) && r.request().method() === "POST");
  await card.getByRole("button", { name: "Sync", exact: true }).click();
  expect((await synced).ok()).toBe(true);
  await expect(card.getByText(/^Last sync .*: 0 new$/)).toBeVisible();
  await expect(card.getByText("4 messages")).toBeVisible();

  // The account also shows in the sidebar, with its folders.
  const nav = page.getByRole("navigation", { name: "Accounts and folders" });
  await expect(nav.getByRole("button", { name: `Folders in ${name}` })).toBeVisible();

  // Removing asks first; the mail stays in the archive.
  acceptDialog(page);
  await card.getByRole("button", { name: "Remove" }).click();
  await expect(main.getByRole("heading", { name: "Removed accounts" })).toBeVisible();
  const gone = accountCard(page, name, "Delete completely…");
  await expect(gone.getByText("Removed. Its mail stays in the archive.")).toBeVisible();
  await expect(gone.getByText("4 messages")).toBeVisible();
  await expect(nav.getByRole("button", { name: new RegExp(`^${name} removed$`) })).toBeVisible();
});
