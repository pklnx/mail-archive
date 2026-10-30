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

/**
 * The vertical centres in each body row of a table: of every visible cell's
 * first text line (a Range over its first visible text node) and of the row
 * menu's icon (⋮), by cell. Cells with nothing visible to read are left out.
 */
export const rowCentres = (table: Locator) =>
  table.evaluate((t) =>
    [...t.querySelectorAll("tbody tr")].map((tr) => {
      const centres: { cell: string; centre: number }[] = [];
      for (const [i, cell] of [...tr.children].entries()) {
        if (!(cell as HTMLElement).checkVisibility()) continue;
        const icon = cell.querySelector(".cds--overflow-menu svg");
        let box: DOMRect | null = icon ? icon.getBoundingClientRect() : null;
        if (!box) {
          const walk = document.createTreeWalker(cell, NodeFilter.SHOW_TEXT);
          for (let n = walk.nextNode(); n && !box; n = walk.nextNode()) {
            const parent = n.parentElement;
            if (!n.textContent?.trim() || !parent?.checkVisibility() || parent.closest(".cds--visually-hidden, .cds--popover-content")) continue;
            const range = document.createRange();
            range.selectNodeContents(n);
            // The first line box of the text: a wrapped text has one per line.
            const first = range.getClientRects()[0];
            if (first && first.height > 0) box = first;
          }
        }
        if (box) centres.push({ cell: icon ? `${i}: ⋮` : `${i}: ${(cell.textContent ?? "").trim().slice(0, 24)}`, centre: box.top + box.height / 2 });
      }
      return centres;
    }),
  );

/**
 * Every row of a table holds its cells on one top alignment: each cell's
 * first line and the ⋮ sit within 1.5 px of the row header's first line
 * (#378). `menus` is how many rows at least have a ⋮ that is checked.
 */
export async function rowsAligned(table: Locator, menus = 1) {
  await expect(table.locator("tbody tr").first()).toBeVisible();
  const all = await rowCentres(table);
  expect(all.filter((cells) => cells.some((c) => c.cell.endsWith("⋮"))).length, `rows with a ⋮ in ${JSON.stringify(all)}`).toBeGreaterThanOrEqual(menus);
  for (const cells of all) {
    expect(cells.length, `cells of ${JSON.stringify(cells)}`).toBeGreaterThan(1);
    const name = cells[0]!.centre;
    for (const c of cells) expect(Math.abs(c.centre - name), `${c.cell} against the row header in ${JSON.stringify(cells)}`).toBeLessThanOrEqual(1.5);
  }
}
