// The header's user panel (#284) in a real browser: the one user action
// opens it below the header and inside the screen, by click, tap and
// keyboard; the focus moves into it, Tab walks its items, Escape and a Tab
// out of it close it, and the shortcuts overview opens from it. App.test.tsx
// covers the rest in jsdom.
import { devices, type Page } from "@playwright/test";
import { isChunkOf } from "../scripts/lazyChunks.mjs";
import { admin } from "./demo";
import { expect, test } from "./fixtures";

const label = `User (${admin.name})`;
const action = (page: Page) => page.getByRole("banner").getByRole("button", { name: label, exact: true });
const panel = (page: Page) => page.getByRole("list", { name: label, exact: true });

async function ready(page: Page) {
  await page.goto("/");
  await expect(page.getByRole("list", { name: "Messages" }).getByRole("button").first()).toBeVisible();
}

/** The panel's list lies below the header and inside the viewport. */
async function placed(page: Page) {
  await expect(panel(page)).toBeVisible();
  const header = await page.getByRole("banner").boundingBox();
  const box = await panel(page).boundingBox();
  const viewport = page.viewportSize();
  if (!header || !box || !viewport) throw new Error("no layout for the header, the panel or the page");
  expect(box.y).toBeGreaterThanOrEqual(header.y + header.height - 0.5);
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(viewport.width + 0.5);
  expect(box.y + box.height).toBeLessThanOrEqual(viewport.height + 0.5);
}

test.describe("desktop", () => {
  test.use({ viewport: { width: 1280, height: 800 } });

  test("a click opens the panel below the header; Enter opens it with the focus on Profile, Tab walks it, Escape and a Tab out close it", async ({ page }) => {
    await ready(page);
    await action(page).click();
    await placed(page);
    await expect(action(page)).toHaveAttribute("aria-expanded", "true");
    await expect(panel(page).getByRole("link", { name: "Profile" })).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(panel(page)).toHaveCount(0);
    await expect(action(page)).toBeFocused();
    await expect(action(page)).toHaveAttribute("aria-expanded", "false");

    // The keyboard: Enter on the action; every item is in the tab order (Switcher expanded).
    await page.keyboard.press("Enter");
    await expect(panel(page).getByRole("link", { name: "Profile" })).toBeFocused();
    await page.keyboard.press("Tab");
    await expect(panel(page).getByRole("button", { name: "Keyboard shortcuts" })).toBeFocused();
    await page.keyboard.press("Tab");
    await expect(panel(page).getByRole("button", { name: /^Switch to (dark|light) mode$/ })).toBeFocused();
    await page.keyboard.press("Tab");
    await expect(panel(page).getByRole("button", { name: "Log out" })).toBeFocused();
    // Past Log out the focus leaves the panel, which closes; the focus stays where Tab put it.
    await page.keyboard.press("Tab");
    await expect(panel(page)).toHaveCount(0);
    await expect(action(page)).toHaveAttribute("aria-expanded", "false");
    await expect(action(page)).not.toBeFocused();
    expect(await page.evaluate(() => document.activeElement !== document.body)).toBe(true);

    // Shift+Tab backwards out of it: past the action to the name link; the panel closes and the shortcuts work again.
    await action(page).click();
    await expect(panel(page).getByRole("link", { name: "Profile" })).toBeFocused();
    await page.keyboard.press("Shift+Tab");
    await expect(action(page)).toBeFocused();
    await expect(panel(page)).toBeVisible();
    await page.keyboard.press("Shift+Tab");
    await expect(panel(page)).toHaveCount(0);
    await expect(action(page)).toHaveAttribute("aria-expanded", "false");
    await page.keyboard.press("j");
    await expect(page).toHaveURL(/[?&]m=/);
  });

  test("opens the shortcuts overview from the panel, loading its chunk; Escape returns the focus to the action", async ({ page }) => {
    await ready(page);
    await action(page).click();
    const chunk = page.waitForResponse((r) => r.request().method() === "GET" && isChunkOf(new URL(r.url()).pathname.split("/").pop() ?? "", "ShortcutsModal"));
    await panel(page).getByRole("button", { name: "Keyboard shortcuts" }).click();
    expect((await chunk).status()).toBe(200);
    const dialog = page.getByRole("dialog", { name: "Keyboard shortcuts" });
    await expect(dialog).toBeVisible();
    await expect(panel(page)).toHaveCount(0);
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
    await expect(action(page)).toBeFocused();
  });
});

test.describe("phone", () => {
  // A touch screen (Pixel 7): the action is there at this width too.
  const { defaultBrowserType: _, ...phone } = devices["Pixel 7"];
  test.use(phone);

  test("a tap opens the panel inside the screen below the header; its items work by tap; a tap outside closes it", async ({ page }) => {
    await ready(page);
    await action(page).tap();
    await placed(page);
    const html = page.locator("html");
    const dark = (await html.getAttribute("class"))?.includes("cds--g100") ?? false;
    await panel(page).getByRole("button", { name: dark ? "Switch to light mode" : "Switch to dark mode" }).tap();
    await expect(html).toHaveClass(dark ? /(^|\s)cds--white(\s|$)/ : /(^|\s)cds--g100(\s|$)/);
    // The theme switch keeps the panel open; its label follows.
    await expect(panel(page).getByRole("button", { name: dark ? "Switch to dark mode" : "Switch to light mode" })).toBeVisible();
    // Outside: the start of the list's title, left of the panel.
    await page.getByRole("heading", { level: 1 }).tap({ position: { x: 4, y: 4 } });
    await expect(panel(page)).toHaveCount(0);
    await action(page).tap();
    await panel(page).getByRole("link", { name: "Profile" }).tap();
    await expect(page).toHaveURL(/\?view=profile$/);
    await expect(page.getByRole("main").getByRole("heading", { name: "Profile", level: 1 })).toBeVisible();
    await expect(panel(page)).toHaveCount(0);
  });
});
