import { expect, test } from "./fixtures";

test("search, open a message, switch text and HTML, go back with u", async ({ page }) => {
  await page.goto("/");
  const list = page.getByRole("list", { name: "Messages" });
  await expect(list.getByRole("button").first()).toBeVisible();

  await page.getByRole("searchbox", { name: "Search mail" }).fill("weekend");
  const hit = list.getByRole("button", { name: /Photos from the weekend and the lease/ });
  await expect(hit).toBeVisible();
  // Only the one message has the word.
  await expect(list.getByRole("button")).toHaveCount(1);
  await expect(page).toHaveURL(/[?&]q=weekend/);
  await list.getByRole("button").first().click();

  const message = page.getByRole("article");
  await expect(message.getByRole("heading", { level: 1 })).toHaveText("Photos from the weekend and the lease");
  await expect(message.getByRole("definition").first()).toHaveText("Sam Taylor <sam@example.org>");
  await expect(message.getByText("personal / INBOX")).toBeVisible();
  const attachments = message.getByRole("list", { name: "Attachments" });
  await expect(attachments.getByRole("link", { name: /Lease\.pdf/ })).toBeVisible();
  await expect(attachments.getByRole("link", { name: /Weekend\.png/ })).toBeVisible();

  // The HTML part shows first, in the sandboxed frame.
  const frame = message.getByTitle("Message content").contentFrame();
  await expect(frame.getByRole("heading", { name: "Hi Alex," })).toBeVisible();

  await message.getByRole("button", { name: "Show text" }).click();
  await expect(message.getByTitle("Message content")).toHaveCount(0);
  await expect(message.getByText(/I attached the signed lease as well/)).toBeVisible();

  await message.getByRole("button", { name: "Show HTML" }).click();
  await expect(message.getByTitle("Message content").contentFrame().getByRole("heading", { name: "Hi Alex," })).toBeVisible();

  await page.keyboard.press("u");
  await expect(page.getByRole("article")).toHaveCount(0);
  await expect(page).not.toHaveURL(/[?&]m=/);
  // The search stays.
  await expect(page.getByRole("searchbox", { name: "Search mail" })).toHaveValue("weekend");
  await expect(hit).toBeVisible();
});

test("only in archive, and search all mail from an account", async ({ page }) => {
  await page.goto("/");
  // Only in archive is a section of the shell's SideNav (a rail here); the
  // accounts and folders are the mail view's tree.
  const nav = page.getByRole("navigation", { name: "Main navigation" });
  const tree = page.getByRole("tree", { name: "Accounts and folders" });
  const list = page.getByRole("list", { name: "Messages" });

  await nav.getByRole("link", { name: /^Only in archive \d+$/ }).click();
  await expect(page).toHaveURL(/[?&]gone=1/);
  const gone = list.getByRole("button", { name: /Your 2026 premium invoice/ });
  await expect(gone).toBeVisible();
  await expect(list.getByRole("button")).toHaveCount(1);
  await gone.click();
  await expect(page.getByRole("article").getByText(/no longer on the server/)).toBeVisible();

  // In the work account the word is not found; "Search all mail" finds it.
  await page.goto("/");
  await tree.getByRole("treeitem", { name: "work", exact: true }).click();
  await expect(page).toHaveURL(/[?&]account=work/);
  await page.getByRole("searchbox", { name: "Search mail" }).fill("premium");
  await expect(page.getByText("No messages match your search.")).toBeVisible();
  await page.getByRole("button", { name: "Search all mail" }).click();
  await expect(page).not.toHaveURL(/[?&]account=/);
  await expect(list.getByRole("button", { name: /Your 2026 premium invoice/ })).toBeVisible();
  await expect(page.getByRole("searchbox", { name: "Search mail" })).toHaveValue("premium");
});
