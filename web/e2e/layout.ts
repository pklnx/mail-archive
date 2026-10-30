// Layout checks shared by the settings pages' phone tests (#369, #370).
import type { Locator, Page } from "@playwright/test";
import { expect } from "./fixtures";

/** Whether the page scrolls sideways. */
export const sideScrolls = (page: Page) => page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);

type Rect = { top: number; bottom: number; left: number; right: number };
const rect = (el: Locator) =>
  el.evaluate((e): Rect => {
    const r = e.getBoundingClientRect();
    return { top: r.top, bottom: r.bottom, left: r.left, right: r.right };
  });

/**
 * A detail page's header: the overflow menu (⋮) stays at the end of the
 * actions row, on the line of the last button, and nothing leaves the
 * window (#369). `menuName` is the menu's accessible name.
 */
export async function menuAtRowEnd(page: Page, main: Locator, menuName: RegExp) {
  const actions = main.locator(".page-header__actions");
  const buttons = actions.getByRole("button");
  const menu = actions.getByRole("button", { name: menuName });
  await expect(menu).toHaveCount(1);
  const count = await buttons.count();
  expect(count, "the header has buttons besides the menu").toBeGreaterThan(1);
  // The menu is the last button; the one before it is the last action.
  await expect(buttons.last()).toHaveAccessibleName(menuName);
  const last = await rect(buttons.nth(count - 2));
  const m = await rect(menu);
  const row = await rect(actions);
  const where = `menu ${JSON.stringify(m)}, last button ${JSON.stringify(last)}, actions ${JSON.stringify(row)}`;
  expect(m.top < last.bottom && last.top < m.bottom, `the menu is on the last button's line: ${where}`).toBe(true);
  expect(m.left, `the menu is right of the last button: ${where}`).toBeGreaterThanOrEqual(last.right);
  expect(Math.abs(m.right - row.right), `the menu ends the actions row: ${where}`).toBeLessThanOrEqual(1);
  for (const b of await buttons.all()) await expect(b).toBeInViewport({ ratio: 1 });
  expect(await sideScrolls(page), "the page scrolls sideways").toBe(false);
}

/**
 * The Legal holds toolbar (#370, #375): everything drawn in it lies inside
 * the toolbar's box, which paints its background; the filter button and Add
 * hold do not overlap; the toolbar keeps Carbon's 48 px and the table starts
 * below it. The first check is generic on purpose: it covers whatever the
 * toolbar holds, so it caught the old toggle's switch below the bar.
 * It runs with the filter closed and neither hover nor focus on its button,
 * whose tooltip draws below the toolbar while shown.
 */
export async function holdsToolbarFits(page: Page, panel: Locator, names: { add: string; filter: string }) {
  const toolbar = panel.locator(".cds--table-toolbar");
  const add = panel.getByRole("button", { name: names.add, exact: true });
  const filter = panel.getByRole("button", { name: names.filter, exact: true });
  // The holds table, or the empty list's text when no hold is active.
  const body = panel.locator(".retention-table table:not(.cds--skeleton), .retention-table__empty").first();
  await expect(body).toBeVisible();
  await expect(filter).toHaveCount(1);
  await expect(filter).toHaveAttribute("aria-expanded", "false");
  await expect(add).toBeVisible();
  await page.mouse.move(0, 0);
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await expect(panel.locator(".cds--table-toolbar .cds--popover--open")).toHaveCount(0);
  const box = await rect(toolbar);
  // 1. Every rendered part: a box larger than 1 × 1 px (visually hidden text
  // is 1 px) that the browser draws.
  const outside = await toolbar.evaluate((bar) => {
    const b = bar.getBoundingClientRect();
    const out: string[] = [];
    for (const el of bar.querySelectorAll("*")) {
      const r = el.getBoundingClientRect();
      if (r.width <= 1 || r.height <= 1) continue;
      if (!el.checkVisibility({ opacityProperty: true, visibilityProperty: true })) continue;
      if (r.top >= b.top - 0.5 && r.bottom <= b.bottom + 0.5 && r.left >= b.left - 0.5 && r.right <= b.right + 0.5) continue;
      const name = `${el.tagName.toLowerCase()}.${String(el.getAttribute("class") ?? "").trim().replace(/\s+/g, ".")}`;
      out.push(`${name} ${JSON.stringify({ top: r.top, bottom: r.bottom, left: r.left, right: r.right })}`);
    }
    return out;
  });
  expect(outside, `parts outside the toolbar ${JSON.stringify(box)}`).toEqual([]);
  // 2. The two controls, inside and apart.
  for (const [name, el] of [
    [names.filter, filter],
    [names.add, add],
  ] as const) {
    const r = await rect(el);
    const where = `${name} ${JSON.stringify(r)}, toolbar ${JSON.stringify(box)}`;
    expect(
      r.top >= box.top - 0.5 && r.bottom <= box.bottom + 0.5 && r.left >= box.left - 0.5 && r.right <= box.right + 0.5,
      `${name} inside the toolbar: ${where}`,
    ).toBe(true);
  }
  const f = await rect(filter);
  const a = await rect(add);
  const apart = f.bottom <= a.top || a.bottom <= f.top || f.right <= a.left || a.right <= f.left;
  expect(apart, `${names.filter} ${JSON.stringify(f)} and ${names.add} ${JSON.stringify(a)} overlap`).toBe(true);
  // 3. Carbon's 48 px.
  expect(Math.abs(box.bottom - box.top - 48), `toolbar height ${box.bottom - box.top}`).toBeLessThanOrEqual(0.5);
  // 4. The table below, no sideways scroll.
  const below = await rect(body);
  expect(below.top, "the table starts below the toolbar").toBeGreaterThanOrEqual(box.bottom - 0.5);
  expect(await sideScrolls(page), "the page scrolls sideways").toBe(false);
}
