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
 * The Legal holds toolbar: the toggle (its label, drawn switch and state
 * text) and Add hold lie inside the toolbar's box, which paints its
 * background, and do not overlap; the table starts below it (#370).
 * Carbon's switch button itself is visually hidden.
 */
export async function holdsToolbarFits(page: Page, panel: Locator, names: { add: string; toggle: string; off: string }) {
  const toolbar = panel.locator(".cds--table-toolbar");
  const toggle = panel.locator(".retention-toolbar__toggle");
  const add = panel.getByRole("button", { name: names.add });
  // The holds table, or the empty list's text when no hold is active.
  const body = panel.locator(".retention-table table:not(.cds--skeleton), .retention-table__empty").first();
  await expect(body).toBeVisible();
  await expect(panel.getByRole("switch", { name: names.toggle })).toHaveCount(1);
  await expect(toggle.getByText(names.off, { exact: true })).toBeVisible();
  const box = await rect(toolbar);
  const parts: [string, Locator][] = [
    ["toggle", toggle],
    ["switch", toggle.locator(".cds--toggle__switch")],
    ["state text", toggle.getByText(names.off, { exact: true })],
    [names.add, add],
  ];
  for (const [name, el] of parts) {
    const r = await rect(el);
    const where = `${name} ${JSON.stringify(r)}, toolbar ${JSON.stringify(box)}`;
    expect(
      r.top >= box.top - 0.5 && r.bottom <= box.bottom + 0.5 && r.left >= box.left - 0.5 && r.right <= box.right + 0.5,
      `${name} inside the toolbar: ${where}`,
    ).toBe(true);
  }
  const t = await rect(toggle);
  const a = await rect(add);
  const apart = t.bottom <= a.top || a.bottom <= t.top || t.right <= a.left || a.right <= t.left;
  expect(apart, `toggle ${JSON.stringify(t)} and ${names.add} ${JSON.stringify(a)} overlap`).toBe(true);
  const below = await rect(body);
  expect(below.top, "the table starts below the toolbar").toBeGreaterThanOrEqual(box.bottom - 0.5);
  expect(await sideScrolls(page), "the page scrolls sideways").toBe(false);
}

// Tooltips in scroll containers (#379). Kept self-contained: its own rect
// reader, so it does not depend on the helpers above.
type Box = { top: number; bottom: number; left: number; right: number };

/** A scroll container's sideways scroll state and box. */
export const sideScroll = (container: Locator) =>
  container.evaluate((e) => {
    const r = e.getBoundingClientRect();
    const box: Box = { top: r.top, bottom: r.bottom, left: r.left, right: r.right };
    return { scrollLeft: e.scrollLeft, scrollWidth: e.scrollWidth, clientWidth: e.clientWidth, box };
  });

/**
 * An open tooltip (or toggletip) neither widens nor scrolls its scroll
 * container sideways, and its bubble lies inside the container's box
 * (±0.5px) and, with `viewport`, inside the window.
 */
export async function tooltipInside(page: Page, container: Locator, tip: Locator, name: string, opts: { viewport?: boolean } = {}) {
  await expect(tip).toBeVisible();
  const c = await sideScroll(container);
  // Carbon puts role="tooltip" on the popover's anchor box; the bubble is
  // its .cds--popover-content.
  const t = await tip.evaluate((e): Box => {
    const r = (e.matches(".cds--popover-content") ? e : (e.querySelector(".cds--popover-content") ?? e)).getBoundingClientRect();
    return { top: r.top, bottom: r.bottom, left: r.left, right: r.right };
  });
  const where = `${name}: tooltip ${JSON.stringify(t)}, container ${JSON.stringify(c)}`;
  expect(c.scrollLeft, `the container is not scrolled sideways, ${where}`).toBe(0);
  expect(c.scrollWidth, `the container does not get wider, ${where}`).toBe(c.clientWidth);
  expect(t.left >= c.box.left - 0.5 && t.right <= c.box.right + 0.5, `the tooltip lies inside the container, ${where}`).toBe(true);
  if (opts.viewport) {
    const width = await page.evaluate(() => document.documentElement.clientWidth);
    expect(t.left >= -0.5 && t.right <= width + 0.5, `the tooltip lies inside the window (${width}px), ${where}`).toBe(true);
  }
}
