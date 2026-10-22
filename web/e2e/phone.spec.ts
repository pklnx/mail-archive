import { devices } from "@playwright/test";
import { expect, test } from "./fixtures";

// A phone with a touch screen: one column at a time, the sidebar a drawer.
const { defaultBrowserType: _, ...phone } = devices["Pixel 7"];
test.use(phone);

test("phone: open the navigation, open a message, back to the list", async ({ page }) => {
  await page.goto("/");
  const list = page.getByRole("list", { name: "Messages" });
  await expect(list.getByRole("button").first()).toBeVisible();
  const nav = page.getByRole("navigation", { name: "Accounts and folders" });
  // The drawer is closed: its navigation is not shown.
  await expect(nav).toBeHidden();

  await page.getByRole("button", { name: "Show accounts and folders" }).click();
  await expect(nav).toBeVisible();
  // personal / Sent (3 messages; work's Sent has 1). Choosing it closes the drawer.
  await nav.getByRole("button", { name: "Sent 3" }).click();
  await expect(nav).toBeHidden();
  await expect(page).toHaveURL(/[?&]account=personal/);
  await expect(page).toHaveURL(/[?&]folder=Sent/);
  await expect(page.getByRole("heading", { name: "Sent", level: 1 })).toBeVisible();

  const item = list.getByRole("button", { name: /Documents for the tax return/ });
  await item.click();
  const message = page.getByRole("article");
  await expect(message.getByRole("heading", { level: 1 })).toHaveText("Documents for the tax return");
  // One column: the list is out of view while the message is open.
  await expect(list).toBeHidden();

  await page.getByRole("button", { name: "Back to the list" }).click();
  await expect(page.getByRole("article")).toHaveCount(0);
  await expect(list).toBeVisible();
  await expect(item).toBeVisible();
});
