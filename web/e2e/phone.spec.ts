import { devices, type Page } from "@playwright/test";
import { expect, test } from "./fixtures";
import { mailReady, openLong } from "./mailFrame";

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
  // The row that had the focus is hidden: the subject takes it (#311).
  await expect(message.getByRole("heading", { level: 1 })).toBeFocused();

  await page.getByRole("button", { name: "Back to the list" }).click();
  await expect(page.getByRole("article")).toHaveCount(0);
  await expect(list).toBeVisible();
  await expect(item).toBeVisible();
  // The Back button is gone: the focus returns to the message's row (#311).
  await expect(item).toBeFocused();
});

test("phone: the quick filters toggle with one tap, Edit filters opens the filter popover", async ({ page }) => {
  await page.goto("/");
  const list = page.getByRole("list", { name: "Messages" });
  const rows = list.getByRole("button");
  await expect(rows.first()).toBeVisible();
  const count = page.locator(".mail-list__count > span").first();
  const all = await count.textContent();
  const quick = page.getByRole("group", { name: "Quick filters" });
  const attachment = quick.getByRole("button", { name: "Has attachment" });
  const grouping = quick.getByRole("button", { name: "Group by conversation" });
  const panel = page.getByRole("group", { name: "Filters", exact: true });
  const tags = page.getByRole("list", { name: "Active filters" });
  // The popover stays closed; the quick filters are under the search.
  await expect(panel).toHaveCount(0);
  await expect(attachment).toHaveAttribute("aria-pressed", "false");

  await attachment.click();
  await expect(page).toHaveURL(/[?&]q=has%3Aattachment/);
  await expect(attachment).toHaveAttribute("aria-pressed", "true");
  await expect(count).not.toHaveText(all!);
  for (const row of await rows.all()) await expect(row.getByRole("img", { name: "Has attachment" })).toBeVisible();
  // No tag repeats it, and the state survives a reload.
  await expect(tags).toHaveCount(0);
  await page.reload();
  await expect(attachment).toHaveAttribute("aria-pressed", "true");
  await attachment.click();
  await expect(page).not.toHaveURL(/has%3Aattachment/);
  await expect(count).toHaveText(all!);

  await grouping.click();
  await expect(page).toHaveURL(/[?&]group=1/);
  await expect(grouping).toHaveAttribute("aria-pressed", "true");
  await grouping.click();
  await expect(page).not.toHaveURL(/group=1/);

  // A set filter's tag: "Edit filters" opens the popover at its field, the × removes it.
  await page.goto("/?q=from%3Asam");
  await expect(rows.first()).toBeVisible();
  await expect(panel).toHaveCount(0);
  await tags.getByRole("button", { name: "Edit filters" }).click();
  await expect(panel).toBeVisible();
  await expect(panel.getByRole("textbox", { name: "From" })).toBeFocused();
  // The popover lies over the tag row on a phone: Escape closes it first.
  await page.keyboard.press("Escape");
  await expect(panel).toHaveCount(0);
  await tags.getByRole("button", { name: "Remove filter: From" }).click();
  await expect(tags).toHaveCount(0);
  await expect(page).not.toHaveURL(/from%3Asam/);
});

// A swipe that starts on a scrim does not scroll the page behind it; a tap
// on it still closes the overlay (#310). The page scrolls while a message is
// open (#200). The swipe is a real touch sequence through the DevTools
// protocol (Input.synthesizeScrollGesture with a touch source does not
// scroll in the headless shell); each call returns once the page has
// handled the event.
async function swipeUp(page: Page, x: number, y: number) {
  const cdp = await page.context().newCDPSession(page);
  // Event timestamps, not real time: 50ms per step, then the finger rests
  // before it lifts, so no fling follows (it would swallow the next tap).
  const t0 = Date.now() / 1000;
  const touch = (type: "touchStart" | "touchMove" | "touchEnd", i: number, at: number) =>
    cdp.send("Input.dispatchTouchEvent", { type, touchPoints: type === "touchEnd" ? [] : [{ x, y: y - i * 30 }], timestamp: t0 + at });
  await touch("touchStart", 0, 0);
  for (let i = 1; i <= 10; i++) await touch("touchMove", i, i * 0.05);
  await touch("touchMove", 10, 1);
  await touch("touchEnd", 10, 1.05);
  await cdp.detach();
}
/** The page's scroll position after two frames, so a scroll from the swipe has been applied. */
const scrollY = (page: Page) => page.evaluate(() => new Promise<number>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r(window.scrollY)))));
/** Whether the page can scroll at all, so an unchanged scrollY says something. */
const scrolls = (page: Page) => page.evaluate(() => document.scrollingElement!.scrollHeight > innerHeight + 100);
const rem = 16;

test("phone: a swipe on the SideNav's scrim does not scroll the message behind it; a tap closes it", async ({ page }) => {
  await openLong(page);
  await expect.poll(() => scrolls(page)).toBe(true);
  const nav = page.getByRole("navigation", { name: "Main navigation" });
  await page.getByRole("button", { name: "Open navigation" }).click();
  await expect(nav).toHaveClass(/cds--side-nav--expanded/);
  const scrim = page.locator(".app-shell > .cds--side-nav__overlay");
  await expect(scrim).toBeVisible();
  const view = page.viewportSize()!;
  // Right of the 16rem panel, below the header.
  const x = Math.round((16 * rem + view.width) / 2);
  const y = Math.round(view.height / 2);
  const before = await scrollY(page);
  await swipeUp(page, x, y);
  expect(await scrollY(page)).toBe(before);
  await page.touchscreen.tap(x, y);
  await expect(nav).not.toHaveClass(/cds--side-nav--expanded/);
  // The same swipe without the scrim scrolls the page: the check above is not vacuous.
  await swipeUp(page, x, y);
  expect(await scrollY(page)).toBeGreaterThan(before);
});

test("phone: a swipe on the Folders scrim does not scroll the message behind it; a tap closes it", async ({ page }) => {
  // Only history brings the overlay over an open message: the Folders
  // button is in the list, which a phone hides while reading.
  await page.goto("/?q=itinerary");
  const list = page.getByRole("list", { name: "Messages" });
  await list.getByRole("button", { name: /Offsite planning/ }).click();
  await expect(page).toHaveURL(/[?&]m=[0-9a-f]{64}/);
  await page.goBack();
  await expect(list).toBeVisible();
  const dialog = page.getByRole("dialog", { name: "Folders" });
  await page.getByRole("button", { name: "Folders", exact: true }).click();
  await expect(dialog).toBeVisible();
  await page.goForward();
  await expect(page).toHaveURL(/[?&]m=[0-9a-f]{64}/);
  await mailReady(page);
  await expect(dialog).toBeVisible();
  await expect.poll(() => scrolls(page)).toBe(true);
  const view = page.viewportSize()!;
  // Right of the overlay, min(20rem, 100vw - 3rem) wide.
  const x = Math.round((Math.min(20 * rem, view.width - 3 * rem) + view.width) / 2);
  const y = Math.round(view.height / 2);
  const before = await scrollY(page);
  await swipeUp(page, x, y);
  expect(await scrollY(page)).toBe(before);
  await page.touchscreen.tap(x, y);
  await expect(dialog).toHaveCount(0);
  // The same swipe without the scrim scrolls the page: the check above is not vacuous.
  await swipeUp(page, x, y);
  expect(await scrollY(page)).toBeGreaterThan(before);
});
