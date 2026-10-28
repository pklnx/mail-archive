// The filter button's popover with the lazy filter form and the date range
// (#286), in a real browser: jsdom does not model the order of the
// popover's, Carbon's and flatpickr's listeners, so these paths are proved
// here. The fixture's guards fail a test on any console error or CSP
// violation, which covers flatpickr's positioning under style-src 'self'.
import type { Locator, Page } from "@playwright/test";
import { isChunkOf } from "../scripts/lazyChunks.mjs";
import { expect, test } from "./fixtures";

const fileOf = (path: string) => path.split("/").pop() ?? "";

/** The local day n days from today in the page's time zone, as YYYY-MM-DD. */
function dayFromToday(page: Page, n: number): Promise<string> {
  return page.evaluate((n) => {
    const d = new Date();
    d.setDate(d.getDate() + n);
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
  }, n);
}

function parts(page: Page) {
  const button = page.getByRole("button", { name: "Filters", exact: true });
  const form = page.getByRole("group", { name: "Filters", exact: true });
  const since = form.getByRole("textbox", { name: "Since" });
  const calendar = page.locator(".flatpickr-calendar.open");
  return { button, form, since, calendar };
}

async function ready(page: Page, url = "/") {
  await page.goto(url);
  await expect(page.getByRole("list", { name: "Messages" }).getByRole("button").first()).toBeVisible();
}

/** A click outside the popover: into the search field, which the popover does not cover. */
async function outside(page: Page) {
  await page.getByRole("searchbox", { name: "Search mail" }).click({ position: { x: 8, y: 8 } });
}

/** Whether the box lies inside the viewport. */
async function inViewport(page: Page, el: Locator) {
  const box = await el.boundingBox();
  const vp = page.viewportSize()!;
  expect(box).not.toBeNull();
  expect(box!.x).toBeGreaterThanOrEqual(0);
  expect(box!.y).toBeGreaterThanOrEqual(0);
  expect(box!.x + box!.width).toBeLessThanOrEqual(vp.width);
  expect(box!.y + box!.height).toBeLessThanOrEqual(vp.height);
}

for (const size of [
  { name: "desktop 1280×800", viewport: { width: 1280, height: 800 } },
  { name: "phone 390×780", viewport: { width: 390, height: 780 } },
]) {
  test.describe(size.name, () => {
    test.use({ viewport: size.viewport });

    test("the form's chunk loads on first open; Escape on the button closes the popover", async ({ page }) => {
      await ready(page);
      const { button, form } = parts(page);
      await expect(form).toHaveCount(0);
      const chunk = page.waitForResponse((r) => isChunkOf(fileOf(new URL(r.url()).pathname), "FilterPanel"));
      await button.click();
      expect((await chunk).status()).toBe(200);
      await expect(form).toBeVisible();
      await inViewport(page, form);
      await expect(button).toBeFocused();
      await page.keyboard.press("Escape");
      await expect(form).toHaveCount(0);
      await expect(button).toHaveAttribute("aria-expanded", "false");
      await expect(button).toBeFocused();
    });

    test("a click into Since keeps the popover open; Escape closes the calendar, then the popover", async ({ page }) => {
      await ready(page);
      const { button, form, since, calendar } = parts(page);
      await button.click();
      await since.click();
      await expect(calendar).toBeVisible();
      await expect(form).toBeVisible();
      await expect(since).toBeFocused();
      await inViewport(page, calendar);
      await page.keyboard.press("Escape");
      await expect(calendar).toHaveCount(0);
      await expect(form).toBeVisible();
      await expect(since).toBeFocused();
      await page.keyboard.press("Escape");
      await expect(form).toHaveCount(0);
      await expect(button).toBeFocused();
    });

    test("a key on a calendar day is the calendar's: j opens no message, Escape goes back to Since", async ({ page }) => {
      await ready(page);
      const { button, form, since, calendar } = parts(page);
      await button.click();
      await since.focus();
      await expect(calendar).toBeVisible();
      await page.keyboard.press("Tab");
      await expect(calendar.locator(".flatpickr-day:focus")).toHaveCount(1);
      await page.keyboard.press("j");
      await expect(page).not.toHaveURL(/[?&]m=/);
      await expect(page.getByRole("article")).toHaveCount(0);
      await page.keyboard.press("Escape");
      await expect(calendar).toHaveCount(0);
      await expect(form).toBeVisible();
      await expect(since).toBeFocused();
    });

    test("a range picked in the calendar and a preset give the search's after: and before:, and survive a reload", async ({ page }) => {
      await ready(page);
      const { button, form, since, calendar } = parts(page);
      await button.click();
      await since.click();
      // The 3rd to the 10th of the month shown (today's), the end inclusive.
      const days = calendar.locator(".flatpickr-day:not(.prevMonthDay):not(.nextMonthDay)");
      await days.nth(2).click();
      await days.nth(9).click();
      await expect(calendar).toHaveCount(0);
      await expect(form).toBeVisible();
      const month = (await dayFromToday(page, 0)).slice(0, 7);
      await expect(page).toHaveURL(new RegExp(`[?&]q=after%3A${month}-03(%20|\\+)before%3A${month}-11(&|$)`));
      await expect(form.getByRole("radio", { name: "Custom" })).toBeChecked();
      const tags = page.getByRole("list", { name: "Active filters" });
      await expect(tags.getByText(/^Date: .+ – .+$/)).toBeVisible();

      // A preset fills after: only; an outside click (into the search) closes the popover.
      await form.getByText("Last 30 days", { exact: true }).click();
      const since30 = await dayFromToday(page, -29);
      await expect(page).toHaveURL(new RegExp(`[?&]q=after%3A${since30}(&|$)`));
      await outside(page);
      await expect(form).toHaveCount(0);
      await page.reload();
      await button.click();
      await expect(form.getByRole("radio", { name: "Last 30 days" })).toBeChecked();
      await expect(form.getByRole("textbox", { name: "Since" })).not.toHaveValue("");
    });

    test("an outside click with the calendar open closes both", async ({ page }) => {
      await ready(page);
      const { button, form, since, calendar } = parts(page);
      await button.click();
      await since.click();
      await expect(calendar).toBeVisible();
      await outside(page);
      await expect(calendar).toHaveCount(0);
      await expect(form).toHaveCount(0);
    });

    test("Edit filters opens the popover at the first tag's field, also a second time", async ({ page }) => {
      await ready(page, "/?q=from%3Asam");
      const { form } = parts(page);
      const edit = page.getByRole("list", { name: "Active filters" }).getByRole("button", { name: "Edit filters" });
      await edit.click();
      await expect(form).toBeVisible();
      await expect(form.getByRole("textbox", { name: "From" })).toBeFocused();
      await page.keyboard.press("Escape");
      await expect(form).toHaveCount(0);
      await edit.click();
      await expect(form.getByRole("textbox", { name: "From" })).toBeFocused();
    });
  });
}
