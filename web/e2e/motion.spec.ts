// Motion (#287): the folder overlay slides in and the conversation opens
// with Carbon's motion, on open only; Carbon's SideNav and user panel keep
// their own transitions. With the system's "reduce motion" setting none of
// it moves. Each check reads the computed style once the element is shown,
// never after a wait for time.
import { devices, type Locator, type Page } from "@playwright/test";
import { expect, test } from "./fixtures";

const { defaultBrowserType: _, ...pixel7 } = devices["Pixel 7"];
const desktop = { viewport: { width: 1280, height: 800 } };

/** The element's animation-name and transition-duration, as computed. */
const motionOf = (el: Locator) =>
  el.evaluate((e) => {
    const s = getComputedStyle(e);
    return { animation: s.animationName, transition: s.transitionDuration };
  });

/** Whether any of a comma-separated list of durations is above zero. */
const moves = (durations: string) => durations.split(",").some((d) => parseFloat(d) > 0);

async function mailList(page: Page) {
  await page.goto("/");
  await expect(page.getByRole("list", { name: "Messages" }).getByRole("button").first()).toBeVisible();
}

/** Opens the demo's offsite conversation message and its conversation. */
async function openConversation(page: Page) {
  await page.goto("/?q=itinerary");
  const list = page.getByRole("list", { name: "Messages" });
  await expect(list.getByRole("button")).toHaveCount(1);
  await list.getByRole("button").click();
  const toggle = page.getByRole("article").getByRole("button", { name: /^Conversation \(\d+\)$/ });
  await toggle.click();
  const dialog = page.getByRole("dialog", { name: "Conversation" });
  await expect(dialog).toBeVisible();
  return { dialog, scrim: page.locator(".conversation-overlay__scrim") };
}

for (const reduced of [false, true]) {
  test.describe(reduced ? "reduced motion" : "motion", () => {
    test.use({ reducedMotion: reduced ? "reduce" : "no-preference" });

    test.describe("phone", () => {
      test.use(pixel7);

      test("the SideNav, the folder overlay and their scrims", async ({ page }) => {
        await mailList(page);
        const nav = page.getByRole("navigation", { name: "Main navigation" });
        await page.getByRole("button", { name: "Open navigation" }).click();
        await expect(nav.getByRole("link", { name: "All mail" })).toBeFocused();
        const navMotion = await motionOf(nav);
        const overlay = await motionOf(page.locator(".cds--side-nav__overlay"));
        expect(moves(navMotion.transition), JSON.stringify(navMotion)).toBe(!reduced);
        expect(moves(overlay.transition), JSON.stringify(overlay)).toBe(!reduced);
        await page.keyboard.press("Escape");

        await page.getByRole("button", { name: "Folders", exact: true }).click();
        const folders = page.getByRole("dialog", { name: "Folders" });
        await expect(folders).toBeVisible();
        expect((await motionOf(folders)).animation).toBe(reduced ? "none" : "app-slide-in-start");
        expect((await motionOf(page.locator(".folder-pane__scrim"))).animation).toBe(reduced ? "none" : "app-fade-in");
      });

      test("the conversation sheet and its scrim", async ({ page }) => {
        const { dialog, scrim } = await openConversation(page);
        expect((await motionOf(dialog)).animation).toBe(reduced ? "none" : "app-sheet-in");
        expect((await motionOf(scrim)).animation).toBe(reduced ? "none" : "app-fade-in");
        // Closing is at once (maintainer decision on #287).
        await page.keyboard.press("Escape");
        await expect(dialog).toHaveCount(0);
      });
    });

    test.describe("desktop", () => {
      test.use(desktop);

      test("the conversation panel and the user panel", async ({ page }) => {
        const { dialog } = await openConversation(page);
        expect((await motionOf(dialog)).animation).toBe(reduced ? "none" : "app-contextual-in");
        await page.keyboard.press("Escape");
        await expect(dialog).toHaveCount(0);

        await page.getByRole("banner").getByRole("button", { name: /^User \(/ }).click();
        const panel = page.locator(".cds--header-panel.app-user-panel");
        await expect(panel).toHaveClass(/cds--header-panel--expanded/);
        const user = await motionOf(panel);
        expect(moves(user.transition), JSON.stringify(user)).toBe(!reduced);
      });

      test("the folder column from lg up never moves", async ({ page }) => {
        await mailList(page);
        const column = page.locator("aside.folder-pane");
        await expect(column).toBeVisible();
        expect((await motionOf(column)).animation).toBe("none");
      });
    });
  });
}
