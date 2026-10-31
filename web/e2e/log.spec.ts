// The Log page's From/To in a real browser (#377): the search filters'
// Carbon date range, not the browser's date input. Aktion and Wer take
// several values, kept in the address (#376). The calendar is
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

/** The Log's default since: the local day 30 days back, as LogPage's defaultFilter has it. */
const defaultSince = (page: Page) =>
  page.evaluate(() => {
    const d = new Date();
    d.setDate(d.getDate() - 30);
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
  });

/**
 * The instant the request carries for a local day (#402): the start of the
 * day as since (startOf), the start of the next day as until (endOf).
 * Computed before a request promise is set up, so auditRequest stays
 * synchronous.
 */
const localMidnight = (page: Page, day: string, after: number) =>
  page.evaluate(
    ([day, after]) => {
      const [y, m, d] = day.split("-").map(Number);
      return new Date(y!, m! - 1, d! + after).toISOString();
    },
    [day, after] as const,
  );
const startOf = (page: Page, day: string) => localMidnight(page, day, 0);
const endOf = (page: Page, day: string) => localMidnight(page, day, 1);

/** The local days of a request's since and until (until is exclusive), as YYYY-MM-DD or null. */
const daysOf = (page: Page, p: URLSearchParams) =>
  page.evaluate(
    ([since, until]) => {
      const day = (d: Date) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
      const before = (iso: string) => {
        const d = new Date(iso);
        d.setDate(d.getDate() - 1);
        return d;
      };
      return { since: since ? day(new Date(since)) : null, until: until ? day(before(until)) : null };
    },
    [p.get("since"), p.get("until")] as const,
  );

/**
 * Fixes the page's clock at 00:01 of today's local day (capped at the real
 * now), before the page loads: the hour after local midnight in which a
 * plain since=today was read as a UTC day after the server's default until
 * and answered 400 (#402). Timers keep running.
 */
async function justAfterMidnight(page: Page) {
  const at = await page.evaluate(() => {
    const n = new Date();
    return Math.min(new Date(n.getFullYear(), n.getMonth(), n.getDate(), 0, 1).getTime(), n.getTime());
  });
  await page.clock.setFixedTime(at);
}

/** The next /api/audit request that has these query parameters. */
const auditRequest = (page: Page, params: Record<string, string>) =>
  page.waitForRequest((r) => {
    const u = new URL(r.url());
    if (u.pathname !== "/api/audit") return false;
    const want = Object.entries(params);
    return want.every(([k, v]) => u.searchParams.get(k) === v) && [...u.searchParams.keys()].every((k) => k in params);
  });

/**
 * The next /api/audit request with exactly these parameters, repeated ones
 * in this order.
 */
const auditRequestAll = (page: Page, params: Record<string, string[]>) =>
  page.waitForRequest((r) => {
    const u = new URL(r.url());
    if (u.pathname !== "/api/audit") return false;
    const keys = [...new Set(u.searchParams.keys())];
    return keys.length === Object.keys(params).length && keys.every((k) => JSON.stringify(u.searchParams.getAll(k)) === JSON.stringify(params[k]));
  });

/** A filter's field (Carbon's MultiSelect), its menu, the text it shows and its tag's clear button. */
function choice(main: Locator, label: string) {
  const field = main.getByRole("combobox", { name: label });
  // The inner locator of has is relative to the outer one: from the page, not from main.
  const box = main.locator(".cds--multi-select", { has: main.page().getByRole("combobox", { name: label }) });
  return { field, box, menu: box.getByRole("listbox"), text: field.locator(".cds--list-box__label"), clear: box.getByRole("button", { name: "Auswahl leeren" }) };
}

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
        await justAfterMidnight(page);
        // Today's entry in the log, whatever the hour.
        expect((await page.request.get("/api/messages?q=log-local-days")).status()).toBe(200);
        const { main, from, to, calendar } = await open(page);
        const day = await today(page);
        const since = await startOf(page, await defaultSince(page));
        const [start, end] = [await startOf(page, day), await endOf(page, day)];
        // East of UTC, local midnight is 22:00 or 23:00 UTC of the day before.
        const [y, m, d] = day.split("-");
        const utcDayBefore = new Date(Date.UTC(Number(y), Number(m) - 1, Number(d) - 1)).toISOString().slice(0, 10);
        expect(start).toMatch(new RegExp(`^${utcDayBefore}T2[23]:00:00\\.000Z$`));
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
        const ended = auditRequest(page, { since, until: end });
        await todayCell.click();
        expect((await (await ended).response())?.status()).toBe(200);
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
        // A new range: today alone, then today as start and end.
        await openAtToday();
        const alone = auditRequest(page, { since: start });
        await todayCell.click();
        expect((await (await alone).response())?.status()).toBe(200);
        const picked = auditRequest(page, { since: start, until: end });
        await todayCell.click();
        expect((await (await picked).response())?.status()).toBe(200);
        await expect(calendar).toHaveCount(0);
        await expect(from).toHaveValue(`${d}.${m}.${y}`);
        await expect(to).toHaveValue(`${d}.${m}.${y}`);
        // Today's entries (at least the search above).
        await expect(main.getByRole("table", { name: "Protokoll" }).getByRole("row").nth(1)).toBeVisible();
        await noSideScroll(page);

        // Typed, committed with Enter as a user does.
        await from.fill("01.01.2020");
        await from.press("Enter");
        await to.fill("31.01.2020");
        const typed = auditRequest(page, { since: "2019-12-31T23:00:00.000Z", until: "2020-01-31T23:00:00.000Z" });
        await to.press("Enter");
        await typed;
        await expect(main.getByText("Keine Einträge in diesem Zeitraum.")).toBeVisible();
        await expect(from).toHaveValue("01.01.2020");
        await expect(to).toHaveValue("31.01.2020");
      });
    });
  }

  // Von and Bis show exactly the dates that are sent (#385). The fixture's
  // guards fail a test on an uncaught error (flatpickr's TypeError on main).
  test.describe("fields and query agree", () => {
    test.use({ viewport: { width: 1280, height: 800 } });

    /** Every /api/audit request from now on, as its query parameters. */
    function auditRequests(page: Page) {
      const seen: Record<string, string>[] = [];
      page.on("request", (r) => {
        const u = new URL(r.url());
        if (u.pathname === "/api/audit") seen.push(Object.fromEntries(u.searchParams));
      });
      return seen;
    }

    /** A range typed and committed with Enter: 01.01.2020 – 20.01.2020. */
    async function typedRange(page: Page, from: Locator, to: Locator) {
      await from.fill("01.01.2020");
      const start = auditRequest(page, { since: "2019-12-31T23:00:00.000Z" });
      await from.press("Enter");
      await start;
      await to.fill("20.01.2020");
      const range = auditRequest(page, { since: "2019-12-31T23:00:00.000Z", until: "2020-01-20T23:00:00.000Z" });
      await to.press("Enter");
      await range;
    }

    test("the default Von emptied, then Enter: no dates, focus stays in Von", async ({ page }) => {
      const { from, to } = await open(page);
      await from.fill("");
      const cleared = auditRequest(page, {});
      await from.press("Enter");
      await cleared;
      await expect(from).toHaveValue("");
      await expect(to).toHaveValue("");
      await expect(from).toBeFocused();
    });

    test("Von clicked, then a click outside: nothing sent, Von keeps the default day", async ({ page }) => {
      const { main, from, to, calendar } = await open(page);
      const shown = await from.inputValue();
      const since = await startOf(page, await defaultSince(page));
      const seen = auditRequests(page);
      await from.click();
      await expect(calendar).toBeVisible();
      await main.getByRole("heading", { name: "Protokoll", level: 1 }).click();
      await expect(calendar).toHaveCount(0);
      // The next change is the only request.
      const next = auditRequest(page, { since, action: "read" });
      const action = choice(main, "Aktion");
      await action.field.click();
      await action.menu.getByRole("option", { name: "Lesen" }).click();
      await next;
      expect(seen).toEqual([{ since, action: "read" }]);
      await expect(from).toHaveValue(shown);
      await expect(to).toHaveValue("");
    });

    test("a picked range, Bis emptied, then a click outside: an open end", async ({ page }) => {
      const { main, from, to, calendar } = await open(page);
      const sinceDay = await defaultSince(page);
      const since = await startOf(page, sinceDay);
      const until = await endOf(page, await today(page));
      // With the default Von, a click on today ends the range there (the
      // calendar opens at Von's month, up to two months back).
      const todayCell = calendar.locator(".flatpickr-day.today:not(.prevMonthDay):not(.nextMonthDay)");
      await from.click();
      await expect(calendar).toBeVisible();
      for (let i = 0; i < 2 && (await todayCell.count()) === 0; i++) await calendar.getByLabel("Nächster Monat").click();
      const picked = auditRequest(page, { since, until });
      await todayCell.click();
      await picked;
      await to.fill("");
      const ended = auditRequest(page, { since });
      await main.getByRole("heading", { name: "Protokoll", level: 1 }).click();
      await ended;
      await expect(from).toHaveValue(sinceDay.split("-").reverse().join("."));
      await expect(to).toHaveValue("");
    });

    test("after a typed Bis, a new start picked in the calendar keeps that Bis", async ({ page }) => {
      const { from, to, calendar } = await open(page);
      await typedRange(page, from, to);
      await to.fill("25.01.2020");
      const typed = auditRequest(page, { since: "2019-12-31T23:00:00.000Z", until: "2020-01-25T23:00:00.000Z" });
      await to.press("Enter");
      await typed;
      await from.click();
      const picked = auditRequest(page, { since: "2020-01-04T23:00:00.000Z", until: "2020-01-25T23:00:00.000Z" });
      await calendar.locator(".flatpickr-day:not(.prevMonthDay):not(.nextMonthDay)", { hasText: /^5$/ }).click();
      await picked;
      await expect(from).toHaveValue("05.01.2020");
      await expect(to).toHaveValue("25.01.2020");
    });

    test("Von emptied and left, then a day clicked: the fields show what is sent", async ({ page }) => {
      const { main, from, to, calendar } = await open(page);
      await typedRange(page, from, to);
      await from.fill("");
      const cleared = auditRequest(page, {});
      await main.getByRole("heading", { name: "Protokoll", level: 1 }).click();
      await cleared;
      await from.click();
      const sent = page.waitForRequest((r) => new URL(r.url()).pathname === "/api/audit");
      await calendar.locator(".flatpickr-day:not(.prevMonthDay):not(.nextMonthDay)", { hasText: /^15$/ }).click();
      const p = await daysOf(page, new URL((await sent).url()).searchParams);
      // The range plugin forgets the old end 10 ms after a clear (its own
      // timer); a person is slower, a test need not be: either way the fields
      // show exactly the sent range, starting at the clicked day.
      expect(p.since).toMatch(/-15$/);
      const german = (day: string | null) => (day ? day.split("-").reverse().join(".") : "");
      await expect(from).toHaveValue(german(p.since));
      await expect(to).toHaveValue(german(p.until));
    });
  });

  test("at 1280 px Von and Bis are cells of the filter row, beside Aktion and Wer", async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    const { main, from, to } = await open(page);
    const action = choice(main, "Aktion").box;
    const who = choice(main, "Wer").box;
    const [f, t, a] = await boxes(from, to, action);
    const w = (await who.boundingBox())!;
    // One row, the fields' tops level, each field as wide and high as the others.
    expect(Math.abs(f.y - a.y), `Von ${JSON.stringify(f)}, Aktion ${JSON.stringify(a)}`).toBeLessThanOrEqual(1);
    expect(Math.abs(t.y - a.y)).toBeLessThanOrEqual(1);
    expect(Math.abs(w.y - a.y), `Wer ${JSON.stringify(w)}`).toBeLessThanOrEqual(1);
    expect(t.x).toBeGreaterThanOrEqual(f.x + f.width);
    expect(a.x).toBeGreaterThanOrEqual(t.x + t.width);
    expect(w.x).toBeGreaterThanOrEqual(a.x + a.width);
    expect(Math.abs(f.width - a.width), `Von ${JSON.stringify(f)}, Aktion ${JSON.stringify(a)}`).toBeLessThanOrEqual(2);
    expect(Math.abs(w.width - a.width)).toBeLessThanOrEqual(2);
    expect(Math.abs(f.height - a.height), `Von ${f.height}, Aktion ${a.height}`).toBeLessThanOrEqual(1);
  });

  test("at 390 px Von, Bis, Aktion and Wer are stacked full width", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 780 });
    const { main, from, to } = await open(page);
    const action = choice(main, "Aktion");
    const who = choice(main, "Wer");
    const [f, t, a] = await boxes(from, to, action.box);
    const w = (await who.box.boundingBox())!;
    expect(t.y).toBeGreaterThan(f.y + f.height);
    expect(a.y).toBeGreaterThan(t.y + t.height);
    expect(w.y).toBeGreaterThan(a.y + a.height);
    for (const [name, b] of [["Bis", t], ["Aktion", a], ["Wer", w]] as const) {
      expect(Math.abs(b.width - f.width), `Von ${f.width}, ${name} ${b.width}`).toBeLessThanOrEqual(2);
      expect(Math.abs(b.x - f.x), name).toBeLessThanOrEqual(1);
    }
    await noSideScroll(page);

    // Long names: the field stays one line and cuts its text; the open
    // menu stays within the field's width and on screen, nothing scrolls sideways.
    await action.field.click();
    for (const name of ["Aufbewahrung und Legal Holds", "Benutzerverwaltung", "Manuelle Synchronisierungen"]) {
      await action.menu.getByRole("option", { name }).click();
      await expect(action.menu.getByRole("option", { name })).toHaveAttribute("aria-selected", "true");
    }
    await expect(action.text).toHaveText("Benutzerverwaltung, Manuelle Synchronisierungen, Aufbewahrung und Legal Holds");
    const cut = await action.text.evaluate((el) => ({ scroll: el.scrollWidth, client: el.clientWidth, overflow: getComputedStyle(el).textOverflow }));
    expect(cut.scroll, "the text is longer than the field").toBeGreaterThan(cut.client);
    expect(cut.overflow).toBe("ellipsis");
    expect(Math.abs((await action.box.boundingBox())!.height - a.height), "one line").toBeLessThanOrEqual(1);
    const menu = (await action.menu.boundingBox())!;
    const field = (await action.box.boundingBox())!;
    expect(menu.x).toBeGreaterThanOrEqual(field.x - 1);
    expect(menu.x + menu.width).toBeLessThanOrEqual(field.x + field.width + 1);
    await action.menu.getByRole("option").last().scrollIntoViewIfNeeded();
    await expect(action.menu.getByRole("option").last()).toBeInViewport({ ratio: 1 });
    await noSideScroll(page);
    await page.keyboard.press("Escape");
    await expect(action.menu.getByRole("option")).toHaveCount(0);
  });

  test("Aktion and Wer take several values, kept in the address for a reload and for Back", async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    // An opened message of today, by the demo admin.
    const list = await page.request.get("/api/messages?limit=1");
    expect(list.status()).toBe(200);
    const [msg] = ((await list.json()) as { messages: { id: string; subject: string }[] }).messages;
    expect((await page.request.get(`/api/messages/${msg!.id}`)).status()).toBe(200);

    const { main } = await open(page);
    const since = await startOf(page, await defaultSince(page));
    const action = choice(main, "Aktion");
    const who = choice(main, "Wer");
    await expect(action.text).toHaveText("Alle Aktionen");
    await expect(who.text).toHaveText("Alle");

    // Two groups, one at a time.
    await action.field.click();
    const one = auditRequestAll(page, { since: [since], action: ["read"] });
    await action.menu.getByRole("option", { name: "Lesen" }).click();
    await one;
    const two = auditRequestAll(page, { since: [since], action: ["search", "read"] });
    await action.menu.getByRole("option", { name: "Suchen" }).click();
    await two;
    await page.keyboard.press("Escape");
    await expect(action.text).toHaveText("Suchen, Lesen");
    // A user and the command line: entries of either. The actors come first.
    await who.field.click();
    await expect(who.menu.getByRole("option").nth(0)).toHaveText("Kommandozeile");
    await expect(who.menu.getByRole("option").nth(1)).toHaveText("API-Schlüssel");
    const user = auditRequestAll(page, { since: [since], action: ["search", "read"], user: ["demo"] });
    await who.menu.getByRole("option", { name: "demo", exact: true }).click();
    await user;
    const both = auditRequestAll(page, { since: [since], action: ["search", "read"], user: ["demo"], actor: ["cli"] });
    await who.menu.getByRole("option", { name: "Kommandozeile" }).click();
    expect((await (await both).response())?.status()).toBe(200);
    await page.keyboard.press("Escape");
    await expect(who.text).toHaveText("Kommandozeile, demo");
    const address = "?view=log&action=search&action=read&user=demo&actor=cli";
    expect(new URL(page.url()).search).toBe(address);
    const link = main.getByRole("table", { name: "Protokoll" }).getByRole("link", { name: msg!.subject }).first();
    await expect(link).toBeVisible();

    // A reload: the same request, the same fields.
    const reloaded = auditRequestAll(page, { since: [since], action: ["search", "read"], user: ["demo"], actor: ["cli"] });
    await page.reload();
    await reloaded;
    await expect(action.text).toHaveText("Suchen, Lesen");
    await expect(who.text).toHaveText("Kommandozeile, demo");

    // A message opened from the Log, then Back.
    await link.click();
    await expect(page).toHaveURL(/\?m=[0-9a-f]{64}$/);
    const back = auditRequestAll(page, { since: [since], action: ["search", "read"], user: ["demo"], actor: ["cli"] });
    await page.goBack();
    await back;
    expect(new URL(page.url()).search).toBe(address);
    await expect(action.text).toHaveText("Suchen, Lesen");
    await expect(who.text).toHaveText("Kommandozeile, demo");

    // Emptied with the tags' clear buttons: no parameters.
    const noWho = auditRequestAll(page, { since: [since], action: ["search", "read"] });
    await who.clear.click();
    await noWho;
    const none = auditRequestAll(page, { since: [since] });
    await action.clear.click();
    await none;
    await expect(action.text).toHaveText("Alle Aktionen");
    await expect(who.text).toHaveText("Alle");
    expect(new URL(page.url()).search).toBe("?view=log");
  });

  test("Escape on the closed Aktion leaves it and keeps the choice; Delete clears it (#411)", async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    const { main } = await open(page);
    const since = await startOf(page, await defaultSince(page));
    const action = choice(main, "Aktion");
    await expect(action.text).toHaveText("Alle Aktionen");
    let requests = 0;
    page.on("request", (r) => {
      if (new URL(r.url()).pathname === "/api/audit") requests++;
    });

    await action.field.click();
    const one = auditRequestAll(page, { since: [since], action: ["read"] });
    await action.menu.getByRole("option", { name: "Lesen" }).click();
    await one;
    const two = auditRequestAll(page, { since: [since], action: ["search", "read"] });
    await action.menu.getByRole("option", { name: "Suchen" }).click();
    await two;
    const settled = requests;
    const address = "?view=log&action=search&action=read";
    expect(new URL(page.url()).search).toBe(address);

    // The first Escape closes the menu, the focus stays on the field.
    await page.keyboard.press("Escape");
    await expect(action.menu.getByRole("option")).toHaveCount(0);
    await expect(action.field).toBeFocused();
    // The second leaves the field: the choice and the address stay.
    await page.keyboard.press("Escape");
    await expect(action.field).not.toBeFocused();
    await expect(action.text).toHaveText("Suchen, Lesen");
    expect(new URL(page.url()).search).toBe(address);

    // Delete on the field clears it; one request since the choice, so the Escapes sent none.
    await action.field.focus();
    const none = auditRequestAll(page, { since: [since] });
    await page.keyboard.press("Delete");
    await none;
    expect(requests).toBe(settled + 1);
    await expect(action.text).toHaveText("Alle Aktionen");
    expect(new URL(page.url()).search).toBe("?view=log");
  });
});
