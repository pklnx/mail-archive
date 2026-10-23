import { devices, type Page } from "@playwright/test";
import { expect, test } from "./fixtures";

// The message view scrolls as one unit (#200): the reading pane from 48rem,
// the page on phones. The demo's offsite conversation (work / INBOX, six
// messages) has a long HTML mail as its 4th message; its subject wraps on
// a phone. Chromium only: the iPhone is its viewport and touch, not WebKit.

const { defaultBrowserType: _pixel, ...pixel7 } = devices["Pixel 7"];
const { defaultBrowserType: _iphone, ...iphone13 } = devices["iPhone 13"];
const sizes = [
  { name: "desktop 1280×800", use: { viewport: { width: 1280, height: 800 } }, phone: false },
  { name: "Pixel 7", use: pixel7, phone: true },
  { name: "iPhone 13", use: iphone13, phone: true },
];

const frameOf = (page: Page) => page.getByRole("article").getByTitle("Message content");

/** Waits until the open message is laid out: heading, conversation (it loads on its own) and the frame's content. */
async function ready(page: Page, subject: RegExp) {
  const article = page.getByRole("article");
  await expect(article.getByRole("heading", { level: 1 })).toHaveText(subject);
  await expect(article.getByRole("region", { name: "Conversation" })).toBeVisible();
  await expect(frameOf(page).contentFrame().locator("p").first()).toBeAttached();
}

/**
 * Opens the long HTML mail. Found by its word "itinerary", then opened in
 * the full list (?m=), so j has a next message.
 */
async function openLong(page: Page) {
  await page.goto("/?q=itinerary");
  const list = page.getByRole("list", { name: "Messages" });
  await expect(list.getByRole("button")).toHaveCount(1);
  await list.getByRole("button").click();
  await expect(page).toHaveURL(/[?&]m=[0-9a-f]{64}/);
  const m = new URL(page.url()).searchParams.get("m")!;
  await page.goto(`/?m=${m}`);
  await ready(page, /^Re: Offsite planning/);
}

/**
 * The main scroller (the reading pane from 48rem, the page on phones): its
 * scrollTop, after moving it to the start or the end if asked.
 */
function scrollTop(page: Page, phone: boolean, to?: "start" | "end"): Promise<number> {
  return page.evaluate(
    ({ sel, to }) => {
      const s = sel ? document.querySelector(sel)! : document.scrollingElement!;
      if (to) s.scrollTop = to === "end" ? s.scrollHeight : 0;
      return s.scrollTop;
    },
    { sel: phone ? "" : ".reader-scroll", to },
  );
}

/** The first line of the body in page coordinates: in the frame (its rect plus the line's inside it), or of the text view. */
async function firstLine(page: Page, html: boolean): Promise<{ top: number; bottom: number }> {
  const lineIn = (el: Element) => {
    const range = el.ownerDocument.createRange();
    range.selectNodeContents(el);
    const r = range.getClientRects()[0]!;
    return { top: r.top, bottom: r.bottom };
  };
  if (!html) return page.locator(".mail-text__para").first().evaluate(lineIn);
  const line = await frameOf(page).contentFrame().locator("p").first().evaluate(lineIn);
  const top = await frameOf(page).evaluate((f) => f.getBoundingClientRect().top + f.clientTop);
  return { top: top + line.top, bottom: top + line.bottom };
}

/** Elements of the reading pane, its ancestors and the page that scroll vertically and have something to scroll. */
async function scrollers(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const reader = document.querySelector("main.mail-pane--reader")!;
    const els = [...reader.querySelectorAll("*")];
    for (let e: Element | null = reader; e; e = e.parentElement) els.push(e);
    const found = els
      .filter((e) => ["auto", "scroll"].includes(getComputedStyle(e).overflowY) && e.scrollHeight > e.clientHeight + 1)
      .map((e) => (e.className && typeof e.className === "string" ? e.className : e.tagName));
    const page = document.scrollingElement!;
    if (page.scrollHeight > page.clientHeight + 1) found.push("page");
    return found;
  });
}

const rect = (page: Page, selector: string) => page.locator(selector).first().evaluate((e) => e.getBoundingClientRect().toJSON() as DOMRect);

for (const size of sizes) {
  test.describe(size.name, () => {
    test.use(size.use);
    const { phone } = size;

    test("the body is on the first screen, the pane or page is the one scroller, the conversation is folded", async ({ page }) => {
      await openLong(page);
      const article = page.getByRole("article");
      const screenBottom = await page.evaluate(() => window.innerHeight);
      const bar = await rect(page, ".reader-bar");
      expect(await scrollTop(page, phone)).toBe(0);

      // The first body line is fully on screen without any scroll, and the
      // "Show full message" hint at the bottom of the screen does not cover it.
      const line = await firstLine(page, true);
      expect(line.top).toBeGreaterThanOrEqual(bar.bottom);
      expect(line.bottom).toBeLessThanOrEqual(screenBottom);
      const hint = article.getByRole("button", { name: "Show full message" });
      await expect(hint).toBeInViewport({ ratio: 1 });
      const hintBox = (await hint.boundingBox())!;
      expect(hintBox.y).toBeGreaterThanOrEqual(line.bottom);

      // One scroller: the reading pane on desktop (next to the list's own),
      // the page on phones. The header, the conversation and the frame's box have none.
      expect(await scrollers(page)).toEqual(phone ? ["page"] : ["mail-pane__fill reader-scroll"]);
      for (const sel of [".message__header", ".conversation", ".message__html"]) {
        expect(await page.locator(sel).evaluate((e) => getComputedStyle(e).overflowY), sel).toBe("visible");
      }

      // The fold: the open message with one neighbour on each side.
      const conversation = article.getByRole("region", { name: "Conversation" });
      await expect(conversation.getByRole("listitem")).toHaveCount(3);
      await expect(conversation.getByRole("button", { name: "Show 1 later message" })).toBeVisible();

      if (phone) {
        // The compact header within its budget; the rest behind Details.
        expect((await rect(page, ".message__header")).height).toBeLessThanOrEqual(180);
        await expect(article.getByRole("button", { name: "Details" })).toHaveAttribute("aria-expanded", "false");
        await expect(article.getByRole("term").filter({ hasText: "Found in" })).toBeHidden();
      } else {
        // The desktop header is unchanged.
        await expect(article.getByRole("button", { name: "Details" })).toHaveCount(0);
        await expect(article.getByRole("term").filter({ hasText: /^To$/ })).toBeVisible();
        await expect(article.getByRole("term").filter({ hasText: "Found in" })).toBeVisible();
      }

      // The plain-text view starts on the first screen too.
      await article.getByRole("button", { name: "Show text" }).click();
      await expect(article.locator(".mail-text__para").first()).toBeVisible();
      const text = await firstLine(page, false);
      expect(text.top).toBeGreaterThanOrEqual(bar.bottom);
      expect(text.bottom).toBeLessThanOrEqual(screenBottom);
      await article.getByRole("button", { name: "Show HTML" }).click();
      await expect(frameOf(page).contentFrame().locator("p").first()).toBeAttached();

      await conversation.getByRole("button", { name: "Show 2 earlier messages" }).click();
      await expect(conversation.getByRole("listitem")).toHaveCount(5);
      await expect(conversation.getByRole("button", { name: /earlier/ })).toHaveCount(0);
      // The revealed entry got the focus once; it does not pull the view
      // back when the hint scrolls to the end (regression).
      await hint.click();
      await expect(hint).toHaveCount(0);
      expect(await scrollTop(page, phone)).toBeGreaterThan(0);
      await expect(page.locator(".conversation__entry:focus")).toHaveCount(0);
    });

    test("the frame is gated until the scroller is at its end, and the next message starts at the top", async ({ page }) => {
      await openLong(page);
      const article = page.getByRole("article");
      const frame = frameOf(page);
      const frameScroll = () => frame.contentFrame().locator("html").evaluate((el) => el.ownerDocument.defaultView!.scrollY);
      const hint = article.getByRole("button", { name: "Show full message" });
      // What a pointer at (x, y) hits: the frame, or the box around it.
      const hits = (x: number, y: number) => page.evaluate(({ x, y }) => document.elementFromPoint(x, y)?.className, { x, y });
      await expect(hint).toBeVisible();
      await expect(frame).toHaveCSS("pointer-events", "none");

      // Pointer, wheel and touch over the gated frame reach the box around
      // it, and a wheel there moves the pane or page, not the mail. The step
      // is smaller than what is above the frame, so the scroller cannot reach
      // its end within the gesture and pass the rest on to the frame.
      const box = (await frame.boundingBox())!;
      expect(await hits(box.x + 20, box.y + 30)).toBe("message__html");
      await page.mouse.move(box.x + 20, box.y + 30);
      await page.mouse.wheel(0, 100);
      await expect.poll(() => scrollTop(page, phone)).toBeGreaterThan(0);
      expect(await frameScroll()).toBe(0);

      // The hint shows the whole frame: at the scroller's end it fills the
      // area from the reader bar to the bottom of the screen.
      await hint.click();
      await expect(hint).toHaveCount(0);
      await expect(frame).toHaveCSS("pointer-events", "auto");
      const bar = await rect(page, ".reader-bar");
      const screenBottom = await page.evaluate(() => window.innerHeight);
      const end = (await frame.boundingBox())!;
      expect(Math.abs(end.y - bar.bottom)).toBeLessThanOrEqual(1);
      expect(Math.abs(end.y + end.height - screenBottom)).toBeLessThanOrEqual(1);
      expect(await scrollTop(page, phone)).toBeGreaterThan(0);
      // Now the pointer reaches the mail. (Headless Chromium does not scroll
      // the sandboxed frame's document by wheel at all, gated or not, so
      // the hit test stands in for scrolling inside it.)
      expect(await hits(end.x + 20, end.y + 30)).toBe("message__frame");

      // Scrolled back up, it is gated again.
      await scrollTop(page, phone, "start");
      await expect(hint).toBeVisible();

      // j opens the next message in the list (the 3rd of the conversation,
      // an HTML mail, so it overflows too) at its top.
      await scrollTop(page, phone, "end");
      await expect(hint).toHaveCount(0);
      const before = page.url();
      await page.keyboard.press("j");
      await expect(page).not.toHaveURL(before);
      await expect(article.getByRole("region", { name: "Conversation" })).toBeVisible();
      await expect(frame.contentFrame().getByText(/I booked the venue/)).toBeAttached();
      expect(await scrollTop(page, phone)).toBe(0);
      // It overflows: at the end it would not be 0.
      expect(await scrollTop(page, phone, "end")).toBeGreaterThan(0);
    });
  });
}
