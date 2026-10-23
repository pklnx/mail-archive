import { devices } from "@playwright/test";
import { expect, test } from "./fixtures";

// A phone with a touch screen: one column at a time; the side navigation
// and the folder tree are overlays.
const { defaultBrowserType: _, ...phone } = devices["Pixel 7"];
test.use(phone);

test("phone: the navigation and the folders, open a message, back to the list", async ({ page }) => {
  await page.goto("/");
  const list = page.getByRole("list", { name: "Messages" });
  await expect(list.getByRole("button").first()).toBeVisible();
  // No bottom bar: the sections are behind the menu button.
  await expect(page.getByRole("button", { name: "More", exact: true })).toHaveCount(0);

  // The menu button opens Carbon's SideNav over the page; Escape closes it
  // and focus returns to the button.
  const nav = page.getByRole("navigation", { name: "Main navigation" });
  const menu = page.getByRole("button", { name: "Open navigation" });
  // Closed, it is zero wide and inert (out of the tab order).
  const closed = async () => {
    await expect(nav).not.toHaveClass(/cds--side-nav--expanded/);
    await expect(nav).toHaveAttribute("inert", "");
    // Clipped by the zero-wide SideNav (its own box is not empty).
    await expect(nav.getByRole("link", { name: "Accounts" })).not.toBeInViewport();
  };
  await closed();
  await menu.click();
  await expect(nav.getByRole("link", { name: "All mail" })).toBeFocused();
  for (const name of ["Accounts", "Users", "Log", "Retention", "Profile"]) await expect(nav.getByRole("link", { name })).toBeInViewport();
  await page.keyboard.press("Escape");
  await closed();
  await expect(menu).toBeFocused();

  // A section from the SideNav; it closes on the way.
  await menu.click();
  await nav.getByRole("link", { name: "Log" }).click();
  await expect(page).toHaveURL(/\?view=log$/);
  await closed();
  await menu.click();
  await nav.getByRole("link", { name: "All mail" }).click();
  await expect(page).toHaveURL(/\/$/);

  // The Folders button opens the tree as a modal overlay; Escape closes it.
  const folders = page.getByRole("button", { name: "Folders", exact: true });
  const dialog = page.getByRole("dialog", { name: "Folders" });
  await folders.click();
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole("treeitem").first()).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(folders).toBeFocused();

  // personal / Sent (3 messages; work's Sent has 1). Choosing it closes the overlay.
  await folders.click();
  await dialog.getByRole("treeitem", { name: "Sent 3" }).click();
  await expect(dialog).toHaveCount(0);
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
