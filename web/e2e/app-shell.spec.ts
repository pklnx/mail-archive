import type { Locator, Page } from "@playwright/test";
import { expect, test } from "./fixtures";

// The app shell behaves like an application, not a website (#283): the page
// does not bounce or scroll, the panes do; no tap highlight and no zoom on
// double tap; the chrome is not selectable as text, the open message and the
// fields are; fields are 16px on touch screens; rows and navigation entries
// show a pressed state. Checked through computed styles and real selections.
// Everything about the mail's document is read from the parent page through
// iframe.contentDocument; nothing evaluates inside the sandboxed frame.

const desktop = { width: 1280, height: 800 };

/** A computed style property of the element. */
function style(el: Locator, prop: string): Promise<string> {
  return el.evaluate((e, p) => getComputedStyle(e).getPropertyValue(p), prop);
}

/** The computed user-select (Chromium reports it unprefixed). */
const userSelect = (el: Locator) => style(el, "user-select");

/** The computed font size in pixels. */
async function fontSize(el: Locator): Promise<number> {
  return parseFloat(await style(el, "font-size"));
}

/** The page coordinates of the middle of the element's first word. */
function firstWord(el: Locator): Promise<{ x: number; y: number; word: string }> {
  return el.evaluate((e) => {
    const walker = document.createTreeWalker(e, NodeFilter.SHOW_TEXT, (n) => (/\w{3,}/.test(n.textContent ?? "") ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_SKIP));
    const node = walker.nextNode() as Text | null;
    if (!node) throw new Error("no text");
    const text = node.textContent!;
    const m = /\w{3,}/.exec(text)!;
    const range = document.createRange();
    range.setStart(node, m.index);
    range.setEnd(node, m.index + m[0].length);
    const r = range.getBoundingClientRect();
    return { x: r.left + r.width / 2, y: r.top + r.height / 2, word: m[0] };
  });
}

/** Double-clicks the element's first word and returns it and the selected text. */
async function doubleClickWord(page: Page, el: Locator): Promise<{ word: string; selected: string }> {
  await page.evaluate(() => getSelection()?.removeAllRanges());
  const { x, y, word } = await firstWord(el);
  await page.mouse.dblclick(x, y);
  return { word, selected: await page.evaluate(() => getSelection()?.toString() ?? "") };
}

/** A colour token resolved to rgb(), as computed styles report colours. */
function token(page: Page, name: string): Promise<string> {
  return page.evaluate((name) => {
    const probe = document.createElement("div");
    probe.style.backgroundColor = `var(${name})`;
    document.body.append(probe);
    const color = getComputedStyle(probe).backgroundColor;
    probe.remove();
    return color;
  }, name);
}

/** Moves the mouse onto the element, presses the button and checks the pressed colour against the hover colour. */
async function pressed(page: Page, el: Locator) {
  const active = await token(page, "--cds-layer-active-01");
  const box = (await el.boundingBox())!;
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  const hover = await style(el, "background-color");
  expect(hover).not.toBe(active);
  await page.mouse.down();
  await expect.poll(() => style(el, "background-color")).toBe(active);
  await page.mouse.up();
}

const messages = (page: Page) => page.getByRole("list", { name: "Messages" });

async function openMessage(page: Page, subject: RegExp) {
  await messages(page).getByRole("button", { name: subject }).first().click();
  await expect(page.getByRole("main").getByRole("heading", { name: subject })).toBeVisible();
}

test.describe("desktop", () => {
  test.use({ viewport: desktop });

  test("the page does not scroll or bounce, the list does", async ({ page }) => {
    await page.goto("/");
    const list = page.locator(".mail-list__items");
    await expect(messages(page).getByRole("button").first()).toBeVisible();
    for (const el of [page.locator("html"), page.locator("body")]) expect(await style(el, "overscroll-behavior-y")).toBe("none");
    // Only vertically: the trackpad's history swipe stays.
    expect(await style(page.locator("html"), "overscroll-behavior-x")).toBe("auto");
    expect(await style(list, "overscroll-behavior-y")).toBe("contain");
    expect(await page.evaluate(() => document.scrollingElement!.scrollHeight <= document.scrollingElement!.clientHeight)).toBe(true);

    const box = (await list.boundingBox())!;
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.mouse.wheel(0, 600);
    await expect.poll(() => list.evaluate((e) => e.scrollTop)).toBeGreaterThan(0);
    expect(await page.evaluate(() => window.scrollY)).toBe(0);
  });

  test("no tap highlight, no double-tap zoom, the resizer keeps its own touch-action", async ({ page }) => {
    await page.goto("/");
    const row = messages(page).getByRole("button").first();
    await expect(row).toBeVisible();
    expect(await style(row, "-webkit-tap-highlight-color")).toBe("rgba(0, 0, 0, 0)");
    expect(await style(row, "touch-action")).toBe("manipulation");
    const resizer = page.locator(".folder-pane__resizer").first();
    await expect(resizer).toBeAttached();
    expect(await style(resizer, "touch-action")).toBe("none");
  });

  test("the chrome is not selectable as text", async ({ page }) => {
    await page.goto("/");
    await expect(messages(page).getByRole("button").first()).toBeVisible();
    // A double click on a word of the date heading selects nothing.
    const heading = page.locator(".mail-list__group").first();
    await expect(heading).toBeVisible();
    expect((await doubleClickWord(page, heading)).selected).toBe("");
    // The rows are buttons: Chromium starts no word selection in a button
    // anyway, so for them the computed style below is the check.

    const chrome = [
      page.locator(".app-shell > .cds--header"),
      page.getByRole("navigation", { name: "Main navigation" }).getByRole("link", { name: "All mail" }),
      page.locator(".cds--btn").first(),
      page.locator(".mail-row").first(),
    ];
    for (const el of chrome) expect(await userSelect(el)).toBe("none");
    // The user panel's items (#284), inside the header.
    await page.getByRole("banner").getByRole("button", { name: "User (demo)", exact: true }).click();
    const item = page.getByRole("list", { name: "User (demo)" }).getByRole("button", { name: "Keyboard shortcuts" });
    await expect(item).toBeVisible();
    expect(await userSelect(item)).toBe("none");
    await page.keyboard.press("Escape");

    await openMessage(page, /Photos from the weekend/);
    expect(await userSelect(page.locator(".reader-bar"))).toBe("none");
  });

  test("the open message, its addresses and the fields stay selectable", async ({ page }) => {
    await page.goto("/");
    await openMessage(page, /Photos from the weekend/);
    const article = page.getByRole("article");

    const subject = article.locator(".message__subject");
    expect(await userSelect(subject)).not.toBe("none");
    const inSubject = await doubleClickWord(page, subject);
    expect(inSubject.selected.trim()).toBe(inSubject.word);

    const details = article.getByRole("button", { name: "Details" });
    if ((await details.getAttribute("aria-expanded")) !== "true") await details.click();
    const fields = article.locator(".message__details .message__fields");
    await expect(fields).toBeVisible();
    expect(await userSelect(fields)).not.toBe("none");
    const from = fields.locator("dd").filter({ hasText: "@" }).first();
    const inFrom = await doubleClickWord(page, from);
    expect(inFrom.selected.trim()).toBe(inFrom.word);

    // The HTML mail's own document, read from the parent page.
    const frame = article.getByTitle("Message content");
    await expect
      .poll(() => frame.evaluate((f: HTMLIFrameElement) => f.contentDocument?.readyState === "complete" && /\/html$/.test(new URL(f.contentDocument.URL).pathname)))
      .toBe(true);
    expect(await frame.evaluate((f: HTMLIFrameElement) => getComputedStyle(f.contentDocument!.body).getPropertyValue("user-select"))).not.toBe("none");

    // A plain-text mail.
    await openMessage(page, /Invoice 2026-10/);
    const text = page.getByRole("article").locator(".message__text");
    await expect(text).toBeVisible();
    expect(await userSelect(text)).not.toBe("none");
    const inText = await doubleClickWord(page, text);
    expect(inText.selected.trim()).toBe(inText.word);

    // The search field sits in the toolbar, which is chrome; its text still selects.
    const search = page.getByRole("searchbox", { name: "Search mail" });
    await search.fill("receipts");
    expect(await userSelect(search)).not.toBe("none");
    await search.dblclick();
    expect(await search.evaluate((e: HTMLInputElement) => [e.selectionStart, e.selectionEnd])).toEqual([0, "receipts".length]);

    // The data on a management page.
    await page.goto("/?view=accounts&id=personal");
    const address = page.getByRole("main").getByText("alex@example.com", { exact: true });
    await expect(address).toBeVisible();
    expect(await userSelect(address)).not.toBe("none");
  });

  test("fields keep Carbon's 14px with a mouse", async ({ page }) => {
    await page.goto("/");
    const search = page.getByRole("searchbox", { name: "Search mail" });
    await expect(search).toBeVisible();
    expect(await fontSize(search)).toBe(14);
  });

  test("rows, navigation links and conversation entries show a pressed state", async ({ page }) => {
    await page.goto("/");
    const row = messages(page).getByRole("button").nth(1);
    await expect(row).toBeVisible();
    await pressed(page, row);

    // The rail, expanded by hover (Carbon delays it).
    const nav = page.getByRole("navigation", { name: "Main navigation" });
    await nav.hover();
    await expect(nav).toHaveClass(/cds--side-nav--expanded/);
    await pressed(page, nav.getByRole("link", { name: "All mail" }));

    await page.goto("/");
    await openMessage(page, /Re: Quote for the kitchen renovation/);
    await page.getByRole("article").getByRole("button", { name: /^Conversation \(\d+\)$/ }).click();
    const conversation = page.getByRole("dialog", { name: "Conversation" });
    const entry = conversation.locator(".conversation__entry:not(.conversation__entry--self)").first();
    await expect(entry).toBeVisible();
    await pressed(page, entry);
  });
});

// Touch screens: a tablet wider than 767px and a phone. pointer: coarse
// matches with hasTouch and isMobile.
for (const size of [
  { name: "tablet 820×1180", viewport: { width: 820, height: 1180 } },
  { name: "phone 390×780", viewport: { width: 390, height: 780 } },
]) {
  test.describe(size.name, () => {
    test.use({ viewport: size.viewport, hasTouch: true, isMobile: true });

    test("fields are at least 16px, so iOS does not zoom on focus", async ({ page }) => {
      await page.goto("/?q=from%3Asam");
      expect(await page.evaluate(() => matchMedia("(pointer: coarse)").matches)).toBe(true);
      const search = page.getByRole("searchbox", { name: "Search mail" });
      await expect(search).toBeVisible();
      expect(await fontSize(search)).toBeGreaterThanOrEqual(16);

      const panel = page.getByRole("group", { name: "Filters", exact: true });
      if (!(await panel.isVisible())) {
        const edit = page.getByRole("list", { name: "Active filters" }).getByRole("button", { name: "Edit filters" });
        await edit.or(page.getByRole("button", { name: "Filters", exact: true })).first().click();
      }
      const from = panel.getByRole("textbox", { name: "From" });
      await expect(from).toBeVisible();
      expect(await fontSize(from)).toBeGreaterThanOrEqual(16);
    });
  });
}
