// The header's user panel (#284) in a real browser: the one user action
// opens it below the header and inside the screen, by click, tap and
// keyboard; the focus moves into it, Tab walks its items, Escape and a Tab
// out of it close it, and the shortcuts overview opens from it. A failed Log
// out (#327) shows its alert in view and keeps the app. App.test.tsx covers
// the rest in jsdom.
import { devices, type Page } from "@playwright/test";
import { isChunkOf } from "../scripts/lazyChunks.mjs";
import { admin } from "./demo";
import { expect, test } from "./fixtures";
import { focusOnBody, useClickModel } from "./clickModel";
import { clickIntoFrame, openLong } from "./mailFrame";

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

/**
 * Answers the app's DELETE /api/session in the browser, never reaching the
 * server (the admin session is shared by all tests): first with a 500, then
 * with a 204. Other requests to the path go through.
 */
async function failLogoutOnce(page: Page) {
  let n = 0;
  await page.route("/api/session", (route) => {
    if (route.request().method() !== "DELETE") return route.continue();
    n++;
    if (n === 1) return route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ error: "store unavailable" }) });
    return route.fulfill({ status: 204 });
  });
}

const logoutAlert = (page: Page) => page.getByRole("alert").filter({ hasText: "Signing out failed: store unavailable." });

/** Log out from the panel fails: the alert is in view under the header, the app stays; Log out again signs out. */
async function logoutFailsThenWorks(page: Page, open: () => Promise<void>, logOut: () => Promise<void>) {
  await open();
  await logOut();
  await expect(logoutAlert(page)).toBeInViewport();
  await expect(logoutAlert(page)).toContainText("Signing out failed: store unavailable. You are still signed in; try again.");
  const header = (await page.getByRole("banner").boundingBox())!;
  const box = (await logoutAlert(page).boundingBox())!;
  expect(box.y).toBeGreaterThanOrEqual(header.y + header.height - 0.5);
  await expect(panel(page)).toHaveCount(0);
  await expect(action(page)).toBeVisible();
  await expect(page.getByRole("button", { name: "Log in" })).toHaveCount(0);

  await open();
  await logOut();
  await expect(page.getByRole("button", { name: "Log in" })).toBeVisible();
  await expect(logoutAlert(page)).toHaveCount(0);
}

test.describe("desktop", () => {
  test.use({ viewport: { width: 1280, height: 800 } });

  test("a click into the mail frame closes the panel (#349)", async ({ page }) => {
    await openLong(page);
    await action(page).click();
    await expect(panel(page)).toBeVisible();
    await expect(action(page)).toHaveAttribute("aria-expanded", "true");
    await clickIntoFrame(page);
    await expect(panel(page)).toHaveCount(0);
    await expect(action(page)).toHaveAttribute("aria-expanded", "false");
  });

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

  test("a failed Log out shows an alert under the header and keeps the app; Log out again signs out", async ({ page, guards }) => {
    guards.allowHTTPError("/api/session", 500);
    await failLogoutOnce(page);
    await ready(page);
    await logoutFailsThenWorks(
      page,
      () => action(page).click(),
      () => panel(page).getByRole("button", { name: "Log out" }).click(),
    );
  });

  // Safari and Firefox on macOS focus nothing on a click (clickModel.ts): the
  // focus leaves the panel with no relatedTarget before the item's click, and
  // the panel must stay open for it (#335, S1).
  test("under the Safari and Firefox click model every item works by click", async ({ page, guards }) => {
    guards.allowHTTPError("/api/session", 500);
    await useClickModel(page);
    await failLogoutOnce(page);
    await ready(page);
    const item = (name: string | RegExp) => panel(page).getByRole(name === "Profile" ? "link" : "button", { name });

    // Profile, pressed by hand: the mousedown leaves the focus on the body, so the model is active.
    await action(page).click();
    await expect(panel(page)).toBeVisible();
    await item("Profile").hover();
    await page.mouse.down();
    expect(await focusOnBody(page)).toBe(true);
    await expect(panel(page)).toBeVisible();
    await page.mouse.up();
    await expect(page).toHaveURL(/\?view=profile$/);
    await expect(page.getByRole("main").getByRole("heading", { name: "Profile", level: 1 })).toBeVisible();
    await expect(panel(page)).toHaveCount(0);

    await action(page).click();
    await item("Keyboard shortcuts").click();
    const dialog = page.getByRole("dialog", { name: "Keyboard shortcuts" });
    await expect(dialog).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);

    // The theme item switches the theme and keeps the panel open.
    await action(page).click();
    const html = page.locator("html");
    const dark = (await html.getAttribute("class"))?.includes("cds--g100") ?? false;
    await item(dark ? "Switch to light mode" : "Switch to dark mode").click();
    await expect(html).toHaveClass(dark ? /(^|\s)cds--white(\s|$)/ : /(^|\s)cds--g100(\s|$)/);
    await expect(item(dark ? "Switch to dark mode" : "Switch to light mode")).toBeVisible();

    // Log out runs: its first answer is the 500 of failLogoutOnce, which never reaches the server.
    await item("Log out").click();
    await expect(logoutAlert(page)).toBeVisible();
    await expect(action(page)).toBeVisible();
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
    // Keyboard shortcuts by tap opens the overview (#335, I5).
    await action(page).tap();
    await panel(page).getByRole("button", { name: "Keyboard shortcuts" }).tap();
    const shortcuts = page.getByRole("dialog", { name: "Keyboard shortcuts" });
    await expect(shortcuts).toBeVisible();
    await expect(panel(page)).toHaveCount(0);
    await page.keyboard.press("Escape");
    await expect(shortcuts).toHaveCount(0);
    await action(page).tap();
    await panel(page).getByRole("link", { name: "Profile" }).tap();
    await expect(page).toHaveURL(/\?view=profile$/);
    await expect(page.getByRole("main").getByRole("heading", { name: "Profile", level: 1 })).toBeVisible();
    await expect(panel(page)).toHaveCount(0);
  });

  test("a failed Log out with a message open and the page scrolled down shows the alert in view", async ({ page, guards }) => {
    guards.allowHTTPError("/api/session", 500);
    await failLogoutOnce(page);
    await page.goto("/?q=itinerary");
    const list = page.getByRole("list", { name: "Messages" });
    await expect(list.getByRole("button")).toHaveCount(1);
    await list.getByRole("button").tap();
    await expect(page.getByRole("article").getByRole("heading", { level: 1 })).toHaveText(/^Re: Offsite planning/);
    // On phones the page itself scrolls while a message is open.
    const scrolled = () =>
      page.evaluate(() => {
        const s = document.scrollingElement!;
        s.scrollTop = s.scrollHeight;
        return s.scrollTop;
      });
    await expect.poll(scrolled).toBeGreaterThan(0);
    await logoutFailsThenWorks(
      page,
      () => action(page).tap(),
      () => panel(page).getByRole("button", { name: "Log out" }).tap(),
    );
  });
});

// The notch and the home indicator (#324, #335 I5): the safe-area insets of
// an iPhone, set through the DevTools protocol. The header reaches under the
// notch and grows by the top inset; the panel opens below it and keeps its
// last item above the home indicator, also scrolled to its end.
test.describe("phone with safe-area insets", () => {
  const { defaultBrowserType: _, ...iphone } = devices["iPhone 13"];
  // Short enough that the open panel scrolls.
  test.use({ ...iphone, viewport: { width: 390, height: 240 } });
  const insets = { top: 47, bottom: 34 };

  test("the header grows by the top inset; the panel opens below it and its last item clears the bottom inset", async ({ page }) => {
    await ready(page);
    const header = page.locator(".app-shell > .cds--header");
    const height = () => header.evaluate((e) => e.getBoundingClientRect().height);
    // Without insets the header is 3rem: the override below is what changes it.
    expect(await height()).toBe(48);
    const cdp = await page.context().newCDPSession(page);
    await cdp.send("Emulation.setSafeAreaInsetsOverride", { insets: { top: insets.top, bottom: insets.bottom, left: 0, right: 0 } });
    await expect.poll(height).toBe(48 + insets.top);

    await action(page).tap();
    await expect(panel(page)).toBeVisible();
    const box = page.locator(".cds--header-panel.app-user-panel");
    const vh = page.viewportSize()!.height;
    expect((await box.boundingBox())!.y).toBe(48 + insets.top);
    expect(await box.evaluate((e) => getComputedStyle(e).paddingBlockEnd)).toBe(`${insets.bottom}px`);
    // It scrolls; at its end the last item lies above the bottom inset.
    expect(await box.evaluate((e) => e.scrollHeight > e.clientHeight)).toBe(true);
    await box.evaluate((e) => e.scrollTo(0, e.scrollHeight));
    const last = panel(page).getByRole("button", { name: "Log out" });
    await expect.poll(async () => (await last.boundingBox())!.y + (await last.boundingBox())!.height).toBeLessThanOrEqual(vh - insets.bottom);
    await cdp.detach();
  });
});
