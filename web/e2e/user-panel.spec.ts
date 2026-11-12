// The header's user actions in a real browser. On a wide window with hover
// they are four icons with tooltips in the header bar (#488): each named,
// its tooltip on hover and on keyboard focus, inside the window. Elsewhere
// (phones, touch screens, a narrow window) the user panel (#284): the one
// user action opens it below the header and inside the screen, by click, tap
// and keyboard; the focus moves into it, Tab walks its items (each an icon
// and its text), Escape and a Tab out of it close it, and the shortcuts
// overview opens from it. A failed Log out (#327) shows its alert in view
// and keeps the app. App.test.tsx covers the rest in jsdom.
import { devices, type Page } from "@playwright/test";
import { isChunkOf } from "../scripts/lazyChunks.mjs";
import { admin } from "./demo";
import { expect, test } from "./fixtures";
import { focusOnBody, useClickModel } from "./clickModel";
import { clickIntoFrame, openLong } from "./mailFrame";
import { tooltipInside } from "./layout";

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
 * with a 204. After the 204 the gate reads the session again (#380); as the
 * server's session lives on, the GETs from then on get the 401 that a real
 * logout would give. Other requests to the path go through.
 */
async function failLogoutOnce(page: Page) {
  let n = 0;
  await page.route("/api/session", (route) => {
    const method = route.request().method();
    if (method === "GET" && n >= 2) {
      return route.fulfill({
        status: 401,
        contentType: "application/json",
        body: JSON.stringify({ error: "login required", setupRequired: false, passkeyOrigin: new URL(page.url()).origin }),
      });
    }
    if (method !== "DELETE") return route.continue();
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

/** The header bar's actions (#488), in their order. */
const bar = (page: Page) => page.locator("header.cds--header .cds--header__global .cds--header__action");
const shortcutsChunk = (page: Page) =>
  page.waitForResponse((r) => r.request().method() === "GET" && isChunkOf(new URL(r.url()).pathname.split("/").pop() ?? "", "ShortcutsModal"));

for (const lang of [
  { locale: "en-US", profile: `Profile (${admin.name})`, shortcuts: "Keyboard shortcuts", dark: "Switch to dark mode", light: "Switch to light mode", logOut: "Log out" },
  { locale: "de-DE", profile: `Profil (${admin.name})`, shortcuts: "Tastenkürzel", dark: "Zum dunklen Modus wechseln", light: "Zum hellen Modus wechseln", logOut: "Abmelden" },
]) {
  test.describe(`desktop (mouse) 1280×800, ${lang.locale}`, () => {
    test.use({ viewport: { width: 1280, height: 800 }, locale: lang.locale });
    const banner = (page: Page) => page.getByRole("banner");
    const profile = (page: Page) => banner(page).getByRole("link", { name: lang.profile, exact: true });
    const button = (page: Page, name: string | RegExp) => banner(page).getByRole("button", { name, exact: true });
    const theme = (page: Page) => button(page, new RegExp(`^(${lang.dark}|${lang.light})$`));
    const tip = (page: Page, name: string) => page.getByRole("tooltip", { name, exact: true });
    const names = async (page: Page) => [lang.profile, lang.shortcuts, (await theme(page).getAttribute("aria-label"))!, lang.logOut];

    async function readyHere(page: Page) {
      await page.goto("/");
      await expect(page.getByRole("list", { name: lang.locale === "de-DE" ? "Nachrichten" : "Messages" }).getByRole("button").first()).toBeVisible();
    }

    test("four actions in the header, each named, with its tooltip on hover inside the window; no user panel", async ({ page }) => {
      await readyHere(page);
      await expect(bar(page)).toHaveCount(4);
      const expected = await names(page);
      expect(await bar(page).evaluateAll((els) => els.map((e) => e.getAttribute("aria-label")))).toEqual(expected);
      await expect(profile(page)).toHaveAttribute("href", "/?view=profile");
      for (const name of expected.slice(1)) await expect(button(page, name)).toBeVisible();
      for (const el of await bar(page).all()) await expect(el).toBeInViewport({ ratio: 1 });
      await expect(banner(page).getByRole("button", { name: /^(User|Benutzer) \(/ })).toHaveCount(0);
      await expect(page.locator(".app-user-panel")).toHaveCount(0);
      for (const [i, name] of expected.entries()) {
        await bar(page).nth(i).hover();
        await tooltipInside(page, page.locator("header.cds--header"), tip(page, name), name, { viewport: true });
      }
    });

    test("Tab walks Profile, shortcuts, theme and Log out with each tooltip; Escape closes a tooltip, the focus stays; Enter opens the overview", async ({ page }) => {
      await readyHere(page);
      const expected = await names(page);
      // Off the bar, so no hover opens a tooltip.
      await page.mouse.move(0, 400);
      await banner(page).getByRole("link", { name: "Mail Archive" }).focus();
      for (const [i, name] of expected.entries()) {
        await page.keyboard.press("Tab");
        await expect(bar(page).nth(i)).toBeFocused();
        await expect(tip(page, name)).toBeVisible();
      }
      // Escape on Profile: Carbon closes its tooltip; the app takes nothing.
      await page.keyboard.press("Shift+Tab");
      await page.keyboard.press("Shift+Tab");
      await page.keyboard.press("Shift+Tab");
      await expect(profile(page)).toBeFocused();
      await expect(tip(page, lang.profile)).toBeVisible();
      await page.keyboard.press("Escape");
      await expect(tip(page, lang.profile)).toHaveCount(0);
      await expect(profile(page)).toBeFocused();
      await expect(page).toHaveURL(/\/$/);

      await page.keyboard.press("Tab");
      await expect(button(page, lang.shortcuts)).toBeFocused();
      const chunk = shortcutsChunk(page);
      await page.keyboard.press("Enter");
      expect((await chunk).status()).toBe(200);
      const dialog = page.getByRole("dialog", { name: lang.shortcuts });
      await expect(dialog).toBeVisible();
      await page.keyboard.press("Escape");
      await expect(dialog).toHaveCount(0);
      await expect(button(page, lang.shortcuts)).toBeFocused();
    });

    test("a click on the theme action switches the theme; its name follows", async ({ page }) => {
      await readyHere(page);
      const html = page.locator("html");
      const dark = (await html.getAttribute("class"))?.includes("cds--g100") ?? false;
      await button(page, dark ? lang.light : lang.dark).click();
      await expect(html).toHaveClass(dark ? /(^|\s)cds--white(\s|$)/ : /(^|\s)cds--g100(\s|$)/);
      await expect(button(page, dark ? lang.dark : lang.light)).toBeVisible();
    });
  });
}

test.describe("desktop (mouse) 1280×800, the actions", () => {
  test.use({ viewport: { width: 1280, height: 800 } });
  const profile = (page: Page) => page.getByRole("banner").getByRole("link", { name: `Profile (${admin.name})`, exact: true });
  const button = (page: Page, name: string | RegExp) => page.getByRole("banner").getByRole("button", { name, exact: true });

  test("a failed Log out shows an alert under the header and keeps the app, the focus on Log out; Log out again signs out", async ({ page, guards }) => {
    guards.allowHTTPError("/api/session", 500);
    // The faked logout's re-read of the session (failLogoutOnce).
    guards.allowHTTPError("/api/session", 401);
    await failLogoutOnce(page);
    await ready(page);
    const logOut = button(page, "Log out");
    await logOut.click();
    await expect(logoutAlert(page)).toBeInViewport();
    await expect(logoutAlert(page)).toContainText("Signing out failed: store unavailable. You are still signed in; try again.");
    await expect(logOut).toBeFocused();
    await expect(page.getByRole("button", { name: "Log in" })).toHaveCount(0);
    await logOut.click();
    await expect(page.getByRole("button", { name: "Log in" })).toBeVisible();
  });

  // The engine's own click focus, without the model (#398): Chromium and
  // Playwright's WebKit (the Linux port) both focus a clicked button, so the
  // model below is needed in both projects. Not focusing on click is macOS
  // platform behaviour, which no CI browser has. If this ever fails in one
  // engine, the model is wrong for it.
  test("the engine's own click focus: a clicked button takes the focus", async ({ page }) => {
    await ready(page);
    const shortcuts = button(page, "Keyboard shortcuts");
    await shortcuts.hover();
    await page.mouse.down();
    await expect(shortcuts).toBeFocused();
    await page.mouse.up();
    const dialog = page.getByRole("dialog", { name: "Keyboard shortcuts" });
    await expect(dialog).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
  });

  // Safari and Firefox on macOS focus nothing on a click (clickModel.ts).
  test("under the Safari and Firefox click model every action works by click", async ({ page, guards }) => {
    guards.allowHTTPError("/api/session", 500);
    guards.allowHTTPError("/api/session", 401);
    await useClickModel(page);
    await failLogoutOnce(page);
    await ready(page);

    await profile(page).hover();
    await page.mouse.down();
    expect(await focusOnBody(page)).toBe(true);
    await page.mouse.up();
    await expect(page).toHaveURL(/\?view=profile$/);
    await expect(page.getByRole("main").getByRole("heading", { name: "Profile", level: 1 })).toBeVisible();

    await button(page, "Keyboard shortcuts").click();
    const dialog = page.getByRole("dialog", { name: "Keyboard shortcuts" });
    await expect(dialog).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);

    const html = page.locator("html");
    const dark = (await html.getAttribute("class"))?.includes("cds--g100") ?? false;
    await button(page, dark ? "Switch to light mode" : "Switch to dark mode").click();
    await expect(html).toHaveClass(dark ? /(^|\s)cds--white(\s|$)/ : /(^|\s)cds--g100(\s|$)/);

    await button(page, "Log out").click();
    await expect(logoutAlert(page)).toBeVisible();
    await expect(button(page, "Log out")).toBeVisible();
  });
});

// From 42rem to 66rem the bar shows icons while the SideNav is an overlay
// next to the usable header: Log out and Keyboard shortcuts close it first.
test.describe("desktop (mouse) 900×800, next to the SideNav overlay", () => {
  test.use({ viewport: { width: 900, height: 800 } });
  const button = (page: Page, name: string | RegExp) => page.getByRole("banner").getByRole("button", { name, exact: true });
  const nav = (page: Page) => page.getByRole("navigation", { name: "Main navigation" });
  async function openNav(page: Page) {
    await button(page, "Open navigation").click();
    await expect(nav(page).getByRole("link", { name: "All mail" })).toBeFocused();
  }

  test("a failed Log out closes the SideNav; the alert is in view and not inert; Log out again signs out", async ({ page, guards }) => {
    guards.allowHTTPError("/api/session", 500);
    guards.allowHTTPError("/api/session", 401);
    await failLogoutOnce(page);
    await ready(page);
    await expect(bar(page)).toHaveCount(4);
    await openNav(page);
    await button(page, "Log out").click();
    await expect(button(page, "Open navigation")).toHaveAttribute("aria-expanded", "false");
    await expect(logoutAlert(page)).toBeInViewport();
    expect(await logoutAlert(page).evaluate((e) => e.closest("[inert]") === null)).toBe(true);
    await expect(button(page, "Log out")).toBeFocused();
    await button(page, "Log out").click();
    await expect(page.getByRole("button", { name: "Log in" })).toBeVisible();
  });

  test("Keyboard shortcuts closes the SideNav and opens the overview; Escape returns the focus to the action", async ({ page }) => {
    await ready(page);
    await openNav(page);
    await button(page, "Keyboard shortcuts").click();
    const dialog = page.getByRole("dialog", { name: "Keyboard shortcuts" });
    await expect(dialog).toBeVisible();
    await expect(button(page, "Open navigation")).toHaveAttribute("aria-expanded", "false");
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
    await expect(button(page, "Keyboard shortcuts")).toBeFocused();
  });
});

// A desktop window narrower than 42rem keeps the panel (breakpoints.ts, headerActions).
test.describe("narrow window (mouse) 600×800", () => {
  test.use({ viewport: { width: 600, height: 800 } });

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
    // The faked logout's re-read of the session (failLogoutOnce).
    guards.allowHTTPError("/api/session", 401);
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
    // The faked logout's re-read of the session (failLogoutOnce).
    guards.allowHTTPError("/api/session", 401);
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
    // The faked logout's re-read of the session (failLogoutOnce).
    guards.allowHTTPError("/api/session", 401);
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

/** Every panel item shows its icon (decorative) and its text, which is its name. */
async function itemsShowIconAndText(page: Page, names: (string | RegExp)[]) {
  const items = panel(page).locator(".cds--switcher__item-link");
  await expect(items).toHaveCount(names.length);
  for (const [i, name] of names.entries()) {
    const item = items.nth(i);
    const text = item.locator(".app-user-panel__item > span");
    await expect(text).toBeVisible();
    await expect(text).toHaveText(name);
    const icon = item.locator(".app-user-panel__item > svg");
    await expect(icon).toBeVisible();
    await expect(icon).toHaveAttribute("aria-hidden", "true");
    await expect(item).toHaveAccessibleName(name);
    // The icon lies before the text, on its line.
    const a = (await icon.boundingBox())!;
    const b = (await text.boundingBox())!;
    expect(a.x + a.width).toBeLessThanOrEqual(b.x + 0.5);
  }
}

test.describe("phone, icon and text", () => {
  const { defaultBrowserType: _, ...phone } = devices["iPhone 13"];
  test.use(phone);

  test("no icon bar; every panel item shows its icon and its text, the icon hidden from screen readers", async ({ page }) => {
    await ready(page);
    expect(await page.evaluate(() => matchMedia("(hover: hover)").matches)).toBe(false);
    await expect(bar(page)).toHaveCount(1);
    await action(page).tap();
    await placed(page);
    await itemsShowIconAndText(page, ["Profile", "Keyboard shortcuts", /^Switch to (dark|light) mode$/, "Log out"]);
  });

  test.describe("de-DE", () => {
    test.use({ locale: "de-DE" });

    test("the German panel shows icon and text, and nothing scrolls sideways", async ({ page }) => {
      await page.goto("/");
      await expect(page.getByRole("list", { name: "Nachrichten" }).getByRole("button").first()).toBeVisible();
      const de = page.getByRole("banner").getByRole("button", { name: `Benutzer (${admin.name})`, exact: true });
      await de.tap();
      const list = page.getByRole("list", { name: `Benutzer (${admin.name})`, exact: true });
      await expect(list).toBeVisible();
      const items = list.locator(".cds--switcher__item-link");
      const names = ["Profil", "Tastenkürzel", /^Zum (dunklen|hellen) Modus wechseln$/, "Abmelden"];
      await expect(items).toHaveCount(names.length);
      for (const [i, name] of names.entries()) {
        await expect(items.nth(i).locator(".app-user-panel__item > svg")).toHaveAttribute("aria-hidden", "true");
        await expect(items.nth(i).locator(".app-user-panel__item > span")).toBeVisible();
        await expect(items.nth(i)).toHaveAccessibleName(name);
      }
      const box = page.locator(".cds--header-panel.app-user-panel");
      // Every item fits: nothing in the panel sticks out of it, and no item scrolls inside.
      const outside = await box.evaluate((e) => {
        const right = e.getBoundingClientRect().right;
        return Array.from(e.querySelectorAll(".cds--switcher__item *")).filter((c) => c.getBoundingClientRect().right > right + 0.5).length;
      });
      expect(outside).toBe(0);
      expect(await items.evaluateAll((els) => els.every((e) => e.scrollWidth <= e.clientWidth))).toBe(true);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
    });
  });
});

// A touch tablet has no hover either: it keeps the panel, not the icons.
test.describe("tablet 820×1180 with touch", () => {
  test.use({ viewport: { width: 820, height: 1180 }, hasTouch: true, isMobile: true });

  test("gets the user panel with icon and text, not the icon bar", async ({ page }) => {
    await ready(page);
    await expect(bar(page)).toHaveCount(1);
    await expect(page.getByRole("banner").getByRole("link", { name: `Profile (${admin.name})` })).toHaveCount(0);
    await action(page).tap();
    await placed(page);
    await itemsShowIconAndText(page, ["Profile", "Keyboard shortcuts", /^Switch to (dark|light) mode$/, "Log out"]);
  });
});

// The notch and the home indicator (#324, #335 I5): the safe-area insets of
// an iPhone, set through the DevTools protocol, so Chromium only (@cdp,
// #398). The header reaches under the notch and grows by the top inset; the
// panel opens below it and keeps its last item above the home indicator,
// also scrolled to its end.
test.describe("phone with safe-area insets", () => {
  const { defaultBrowserType: _, ...iphone } = devices["iPhone 13"];
  // Short enough that the open panel scrolls.
  test.use({ ...iphone, viewport: { width: 390, height: 240 } });
  const insets = { top: 47, bottom: 34 };

  test("the header grows by the top inset; the panel opens below it and its last item clears the bottom inset", { tag: "@cdp" }, async ({ page }) => {
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
