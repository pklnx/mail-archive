// The Log page's From/To in a real browser (#377): the search filters'
// Carbon date range, not the browser's date input. The calendar is
// flatpickr's, appended to the body, so it neither widens nor scrolls the
// page or its scroll container (.accounts-page). The fixture's guards fail
// a test on any console error or CSP violation, which covers flatpickr's
// positioning under style-src 'self'.
import type { Locator, Page } from "@playwright/test";
import { expect, test } from "./fixtures";
import { sideScroll, sideScrolls } from "./layout";

/** Today in the page's time zone, as YYYY-MM-DD. */
const today = (page: Page) =>
  page.evaluate(() => {
    const d = new Date();
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
  });

/** The Log's default since: 30 days ago as a UTC day, as LogPage's defaultFilter has it. */
const defaultSince = (page: Page) => page.evaluate(() => new Date(Date.now() - 30 * 86_400_000).toISOString().slice(0, 10));

/** Whether the page's local day is the UTC day: the server's since/until are UTC days. */
const sameDayAsUTC = (page: Page) => page.evaluate(() => new Date().getDate() === new Date().getUTCDate());

/** The next /api/audit request that has these query parameters. */
const auditRequest = (page: Page, params: Record<string, string>) =>
  page.waitForRequest((r) => {
    const u = new URL(r.url());
    if (u.pathname !== "/api/audit") return false;
    const want = Object.entries(params);
    return want.every(([k, v]) => u.searchParams.get(k) === v) && [...u.searchParams.keys()].every((k) => k in params);
  });

async function open(page: Page) {
  await page.goto("/?view=log");
  const main = page.getByRole("main");
  await expect(main.getByRole("heading", { name: "Protokoll", level: 1 })).toBeVisible();
  const from = main.getByRole("textbox", { name: "Von" });
  const to = main.getByRole("textbox", { name: "Bis" });
  await expect(from).not.toHaveValue("");
  return { main, from, to, calendar: page.locator(".flatpickr-calendar.open") };
}

/** Neither the page nor the Log's scroll container scrolls sideways. */
async function noSideScroll(page: Page) {
  expect(await sideScrolls(page), "the page scrolls sideways").toBe(false);
  const s = await sideScroll(page.locator(".accounts-page"));
  expect(s.scrollLeft, ".accounts-page scrollLeft").toBe(0);
  expect(s.scrollWidth, ".accounts-page scrollWidth").toBe(s.clientWidth);
}

type Box = { x: number; y: number; width: number; height: number };
/** The boxes of From, To and the Action select. */
async function boxes(from: Locator, to: Locator, action: Locator): Promise<[Box, Box, Box]> {
  const box = async (l: Locator) => {
    const b = await l.boundingBox();
    expect(b).not.toBeNull();
    return b!;
  };
  return [await box(from), await box(to), await box(action)];
}

async function calendarOnScreen(page: Page, calendar: Locator) {
  await expect(calendar).toBeVisible();
  await expect(calendar).toHaveAttribute("aria-label", "Kalender");
  await expect(calendar).toBeInViewport({ ratio: 1 });
  await noSideScroll(page);
}

test.describe("in German", () => {
  test.use({ locale: "de-DE" });

  for (const size of [
    { name: "desktop 1280×800", viewport: { width: 1280, height: 800 } },
    { name: "phone 390×780", viewport: { width: 390, height: 780 } },
  ]) {
    test.describe(size.name, () => {
      test.use({ viewport: size.viewport });

      test("Von and Bis are text fields in the user's format that open Carbon's calendar on screen", async ({ page }) => {
        const { from, to, calendar } = await open(page);
        for (const field of [from, to]) {
          await expect(field).toHaveAttribute("type", "text");
          await expect(field).toHaveAttribute("placeholder", "TT.MM.JJJJ");
          await expect(field).toHaveAccessibleDescription("Format: TT.MM.JJJJ");
        }
        await expect(from).toHaveValue(/^\d{2}\.\d{2}\.\d{4}$/);
        await from.click();
        await calendarOnScreen(page, calendar);
        // Escape in the field closes the calendar.
        await page.keyboard.press("Escape");
        await expect(calendar).toHaveCount(0);
        await to.click();
        await calendarOnScreen(page, calendar);
        await page.keyboard.press("Escape");
        await expect(calendar).toHaveCount(0);
      });

      test("a picked range and a picked single day send since/until as local days; a typed past range shows no entries", async ({ page }) => {
        const { main, from, to, calendar } = await open(page);
        const day = await today(page);
        const since = await defaultSince(page);
        // The calendar opens at From's month. The default From is 30 days
        // back, which can be two months back (on 1 March, and on 2 March in a
        // non-leap year), so go forward until today's cell is there, at most
        // twice. flatpickr redraws the month on the click itself.
        const todayCell = calendar.locator(".flatpickr-day.today:not(.prevMonthDay):not(.nextMonthDay)");
        const openAtToday = async () => {
          await from.click();
          await expect(calendar).toBeVisible();
          for (let i = 0; i < 2 && (await todayCell.count()) === 0; i++) await calendar.getByLabel("Nächster Monat").click();
          await expect(todayCell).toHaveCount(1);
        };
        // With the default From set, a click on today ends the range there.
        await openAtToday();
        const ended = auditRequest(page, { since, until: day });
        await todayCell.click();
        await ended;
        await expect(calendar).toHaveCount(0);
        // Emptied From, left with a click outside: no dates (the server's 30
        // days). (Tab moves into the calendar and commits nothing: #385.)
        const cleared = auditRequest(page, {});
        await from.fill("");
        await main.getByRole("heading", { name: "Protokoll", level: 1 }).click();
        await cleared;
        await expect(from).toHaveValue("");
        await expect(to).toHaveValue("");
        await expect(calendar).toHaveCount(0);
        // A new range: today as start and end.
        await openAtToday();
        await todayCell.click();
        const picked = auditRequest(page, { since: day, until: day });
        await todayCell.click();
        await picked;
        await expect(calendar).toHaveCount(0);
        const [y, m, d] = day.split("-");
        await expect(from).toHaveValue(`${d}.${m}.${y}`);
        await expect(to).toHaveValue(`${d}.${m}.${y}`);
        // Today's entries (at least the demo admin's login of this run),
        // unless the local day starts before the UTC day the server counts in.
        if (await sameDayAsUTC(page)) await expect(main.getByRole("table", { name: "Protokoll" }).getByRole("row").nth(1)).toBeVisible();
        await noSideScroll(page);

        // Typed, committed with Enter as a user does.
        await from.fill("01.01.2020");
        await from.press("Enter");
        await to.fill("31.01.2020");
        const typed = auditRequest(page, { since: "2020-01-01", until: "2020-01-31" });
        await to.press("Enter");
        await typed;
        await expect(main.getByText("Keine Einträge in diesem Zeitraum.")).toBeVisible();
        await expect(from).toHaveValue("01.01.2020");
        await expect(to).toHaveValue("31.01.2020");
      });
    });
  }

  test("at 1280 px Von and Bis are cells of the filter row, beside Aktion and Wer", async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    const { main, from, to } = await open(page);
    const action = main.getByRole("combobox", { name: "Aktion" });
    const [f, t, a] = await boxes(from, to, action);
    // One row, the fields' tops level, each field as wide as the Select.
    expect(Math.abs(f.y - a.y), `Von ${JSON.stringify(f)}, Aktion ${JSON.stringify(a)}`).toBeLessThanOrEqual(1);
    expect(Math.abs(t.y - a.y)).toBeLessThanOrEqual(1);
    expect(t.x).toBeGreaterThanOrEqual(f.x + f.width);
    expect(Math.abs(f.width - a.width), `Von ${f.width}, Aktion ${a.width}`).toBeLessThanOrEqual(2);
  });

  test("at 390 px Von and Bis are stacked full width", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 780 });
    const { main, from, to } = await open(page);
    const action = main.getByRole("combobox", { name: "Aktion" });
    const [f, t, a] = await boxes(from, to, action);
    expect(t.y).toBeGreaterThan(f.y + f.height);
    expect(Math.abs(f.width - a.width), `Von ${f.width}, Aktion ${a.width}`).toBeLessThanOrEqual(2);
    expect(Math.abs(t.x - f.x)).toBeLessThanOrEqual(1);
    await noSideScroll(page);
  });
});
