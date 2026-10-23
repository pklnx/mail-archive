import { devices, type Page } from "@playwright/test";
import { expect, test } from "./fixtures";

// The message view scrolls as one unit (#200): the reading pane from 48rem,
// the page on phones. The HTML frame is as tall as the mail (#203): it is
// sandboxed with the same origin and without scripts, and the app sizes it
// to its document. The demo's offsite conversation (work / INBOX, six
// messages) has a long HTML mail as its 4th message; its subject wraps on
// a phone. Chromium only: the iPhone is its viewport and touch, not WebKit.

const { defaultBrowserType: _pixel, ...pixel7 } = devices["Pixel 7"];
const { defaultBrowserType: _iphone, ...iphone13 } = devices["iPhone 13"];
const desktop = { viewport: { width: 1280, height: 800 } };
const sizes = [
  { name: "desktop 1280×800", use: desktop, phone: false },
  { name: "Pixel 7", use: pixel7, phone: true },
  { name: "iPhone 13", use: iphone13, phone: true },
];

const sandbox = "allow-same-origin allow-popups allow-popups-to-escape-sandbox";
const frameOf = (page: Page) => page.getByRole("article").getByTitle("Message content");

// Everything about the mail's document is read from the parent page through
// iframe.contentDocument (the frame is same-origin), and clicks into it are
// mouse clicks at coordinates: no Playwright locator or evaluate runs inside
// the frame. Such a call initializes the frame's main world, and with it the
// guards' init script, which the sandbox blocks and the browser logs; if the
// frame is still its initial about:blank, the guards fail the test.

/** Whether the frame shows a loaded mail (not its initial about:blank), and its text matches if asked. */
function mailLoaded(page: Page, text?: RegExp): Promise<boolean> {
  return frameOf(page).evaluate(
    (f: HTMLIFrameElement, src) => {
      const doc = f.contentDocument;
      if (!doc || !/\/api\/messages\/[0-9a-f]{64}\/html$/.test(new URL(doc.URL).pathname) || doc.readyState !== "complete") return false;
      return src === null || new RegExp(src).test(doc.body.textContent ?? "");
    },
    text ? text.source : null,
  );
}

/** Waits until the frame shows a loaded mail (and the text, if asked). */
async function mailReady(page: Page, text?: RegExp) {
  await expect.poll(() => mailLoaded(page, text)).toBe(true);
}

/** The center of the mail's link with this text (also in an open shadow root), in page coordinates (the frame's box plus the link's box in it). */
function linkCenter(page: Page, name: string): Promise<{ x: number; y: number }> {
  return frameOf(page).evaluate((f: HTMLIFrameElement, name) => {
    const doc = f.contentDocument!;
    const roots = [doc, ...[...doc.querySelectorAll("*")].flatMap((e) => (e.shadowRoot ? [e.shadowRoot] : []))];
    const a = roots.flatMap((r) => [...r.querySelectorAll("a")]).find((e) => e.textContent === name)!;
    const r = a.getBoundingClientRect();
    const box = f.getBoundingClientRect();
    return { x: box.left + f.clientLeft + r.left + r.width / 2, y: box.top + f.clientTop + r.top + r.height / 2 };
  }, name);
}

/** Waits until the open message is laid out: heading, conversation (it loads on its own) and the frame's content. */
async function ready(page: Page, subject: RegExp) {
  const article = page.getByRole("article");
  await expect(article.getByRole("heading", { level: 1 })).toHaveText(subject);
  await expect(article.getByRole("region", { name: "Conversation" })).toBeVisible();
  await mailReady(page);
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
 * Adds markup to the HTML mails the server sends, after the server's
 * preamble or at the end; with images, only to those with remote images
 * allowed (elsewhere the frame's CSP refuses the images, which the guards
 * would report).
 */
async function addToMail(page: Page, markup: string, where: "start" | "end", images = false) {
  await page.route(images ? /\/api\/messages\/[0-9a-f]{64}\/html\?images=1$/ : /\/api\/messages\/[0-9a-f]{64}\/html(\?[^#]*)?$/, async (route) => {
    const response = await route.fetch();
    const body = await response.text();
    // The preamble (<base> and the height reset) ends with its own line; it follows the mail's doctype, if any.
    const end = "</style>\n";
    const at = body.indexOf(end) + end.length;
    await route.fulfill({ response, body: where === "start" ? body.slice(0, at) + markup + body.slice(at) : body + markup });
  });
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
  return frameOf(page).evaluate((f: HTMLIFrameElement) => {
    const range = f.contentDocument!.createRange();
    range.selectNodeContents(f.contentDocument!.querySelector("p")!);
    const r = range.getClientRects()[0]!;
    const top = f.getBoundingClientRect().top + f.clientTop;
    return { top: top + r.top, bottom: top + r.bottom };
  });
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

interface Fit {
  /** The frame's inner height (its document's viewport). */
  frame: number;
  /** The document's scroll height (the scrolling element's: <html>, or the body in quirks mode). */
  content: number;
  /** What the document could scroll inside the frame. */
  overflow: number;
  /** From the bottom of the mail's lowest element to the end of the frame: the blank space below the mail. */
  below: number;
}

/** The frame and its document's geometry, read from the parent (same origin). */
function fitOf(page: Page): Promise<Fit | null> {
  return frameOf(page).evaluate((f: HTMLIFrameElement) => {
    const doc = f.contentDocument;
    // Not yet the mail (the initial about:blank) or not loaded: nothing to compare.
    if (!doc || !/\/html$/.test(new URL(doc.URL).pathname) || doc.readyState !== "complete") return null;
    const se = doc.scrollingElement!;
    const bottoms = [...doc.body.querySelectorAll("*")].map((e) => e.getBoundingClientRect().bottom);
    return { frame: f.clientHeight, content: se.scrollHeight, overflow: se.scrollHeight - se.clientHeight, below: f.clientHeight - Math.max(0, ...bottoms) };
  });
}

/**
 * Waits until the frame is as tall as its content: nothing to scroll inside,
 * the frame's height equal to the content's, and no more blank space below
 * the mail than the margins of the body and the last paragraph. Polls: the
 * observers fit after the frame's load.
 */
async function fitted(page: Page): Promise<Fit> {
  await expect
    .poll(async () => {
      const f = await fitOf(page);
      return f !== null && f.overflow <= 1 && Math.abs(f.frame - f.content) <= 1 && f.below >= 0 && f.below <= 40;
    })
    .toBe(true);
  return (await fitOf(page))!;
}

/** The frame's document: its rendering mode, the element that scrolls it and the head's first elements. */
function modeOf(page: Page) {
  return frameOf(page).evaluate((f: HTMLIFrameElement) => {
    const doc = f.contentDocument!;
    return {
      compat: doc.compatMode,
      scroller: doc.scrollingElement!.tagName,
      head: [...doc.head.children].slice(0, 2).map((e) => e.tagName + (e.getAttribute("target") ?? "")),
    };
  });
}

const rect = (page: Page, selector: string) => page.locator(selector).first().evaluate((e) => e.getBoundingClientRect().toJSON() as DOMRect);

for (const size of sizes) {
  test.describe(size.name, () => {
    test.use(size.use);
    const { phone } = size;

    test("the body is on the first screen, the frame is as tall as the mail, the pane or page is the one scroller", async ({ page }) => {
      await openLong(page);
      const article = page.getByRole("article");
      const frame = frameOf(page);
      const screenBottom = await page.evaluate(() => window.innerHeight);
      const bar = await rect(page, ".reader-bar");
      expect(await scrollTop(page, phone)).toBe(0);
      await expect(frame).toHaveAttribute("sandbox", sandbox);

      // The first body line is fully on screen without any scroll; no hint covers it.
      const line = await firstLine(page, true);
      expect(line.top).toBeGreaterThanOrEqual(bar.bottom);
      expect(line.bottom).toBeLessThanOrEqual(screenBottom);
      await expect(article.getByRole("button", { name: "Show full message" })).toHaveCount(0);

      // The frame grows with the mail, far past the screen, and has nothing to scroll.
      const fit = await fitted(page);
      expect(fit.frame).toBeGreaterThan(2 * screenBottom);
      await expect(frame).not.toHaveCSS("pointer-events", "none");

      // One scroller: the reading pane on desktop (next to the list's own),
      // the page on phones. The header, the conversation, the frame's box
      // and the frame's document have none.
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
      // Back to HTML: the new frame starts at the last height and is fitted to the same.
      await article.getByRole("button", { name: "Show HTML" }).click();
      expect(await frame.evaluate((f) => f.style.blockSize)).toBe(`${fit.frame}px`);
      await mailReady(page);
      expect((await fitted(page)).frame).toBe(fit.frame);

      await conversation.getByRole("button", { name: "Show 2 earlier messages" }).click();
      await expect(conversation.getByRole("listitem")).toHaveCount(5);
      await expect(conversation.getByRole("button", { name: /earlier/ })).toHaveCount(0);
      // The longer conversation pushes the frame down; it keeps its height.
      expect((await fitted(page)).frame).toBe(fit.frame);
    });

    test("a mail with a doctype renders in standards mode and the frame fits it", async ({ page }) => {
      await openLong(page);
      // The preamble follows the doctype: <base target> and the reset style open the head.
      await expect.poll(() => modeOf(page)).toEqual({ compat: "CSS1Compat", scroller: "HTML", head: ["BASE_blank", "STYLE"] });
      const fit = await fitted(page);
      expect(fit.frame).toBeGreaterThan(await page.evaluate(() => window.innerHeight));
    });

    test("a mail without a doctype renders in quirks mode and the frame fits it", async ({ page }) => {
      await page.goto("/?q=weekend");
      const list = page.getByRole("list", { name: "Messages" });
      await expect(list.getByRole("button")).toHaveCount(1);
      await list.getByRole("button").click();
      // A single message has no conversation: wait for the heading and the frame's content.
      await expect(page.getByRole("article").getByRole("heading", { level: 1 })).toHaveText(/^Photos from the weekend/);
      await mailReady(page);
      await expect.poll(() => modeOf(page)).toMatchObject({ compat: "BackCompat", scroller: "BODY" });
      await fitted(page);
    });

    test("a wheel over the frame moves the message, and a link in the mail works on the first click", async ({ page, context }) => {
      await addToMail(page, `<p><a href="https://links.example/agenda">Agenda</a></p>`, "start");
      await context.route("https://links.example/**", (route) => route.fulfill({ contentType: "text/html", body: "<p>agenda</p>" }));
      await openLong(page);
      const frame = frameOf(page);
      await fitted(page);
      const frameScroll = () => frame.evaluate((f: HTMLIFrameElement) => f.contentWindow!.scrollY);

      // The wheel over the mail at scroll 0 moves the pane or page; the
      // frame's document has nothing to scroll.
      const box = (await frame.boundingBox())!;
      await page.mouse.move(box.x + 20, box.y + 60);
      await page.mouse.wheel(0, 200);
      await expect.poll(() => scrollTop(page, phone)).toBeGreaterThan(0);
      expect(await frameScroll()).toBe(0);

      // At scroll 0 a pointer on the link reaches the frame, and the click
      // opens the link in a new tab right away.
      await scrollTop(page, phone, "start");
      const at = await linkCenter(page, "Agenda");
      const hit = await page.evaluate(({ x, y }) => document.elementFromPoint(x, y)?.className, at);
      expect(hit).toBe("message__frame");
      const opened = context.waitForEvent("page");
      await page.mouse.click(at.x, at.y);
      const tab = await opened;
      await expect.poll(() => tab.url()).toBe("https://links.example/agenda");
      // The frame still shows the mail.
      expect(await frame.evaluate((f: HTMLIFrameElement) => new URL(f.contentDocument!.URL).pathname)).toMatch(/\/html$/);
    });

    test("the frame fits again after a resize or rotation and in the dark theme; the next message starts at the top", async ({ page }) => {
      await openLong(page);
      const before = await fitted(page);
      const vp = page.viewportSize()!;
      // Rotation on phones, a narrower window on desktop: the text reflows.
      await page.setViewportSize(phone ? { width: vp.height, height: vp.width } : { width: 800, height: vp.height });
      const turned = await fitted(page);
      expect(turned.frame).not.toBe(before.frame);
      await page.setViewportSize(vp);
      await expect.poll(async () => (await fitted(page)).frame).toBe(before.frame);

      // The dark sheet: padding (a narrower frame) and a border, which the
      // fit adds to the frame's height.
      const light = await frameOf(page).evaluate((f) => f.clientWidth);
      await page.emulateMedia({ colorScheme: "dark" });
      await expect(page.locator(".cds--g100 .message__html")).toBeAttached();
      await expect.poll(() => frameOf(page).evaluate((f) => f.clientWidth)).toBeLessThan(light);
      const dark = await fitted(page);
      expect(await frameOf(page).evaluate((f) => f.style.blockSize)).toBe(`${dark.frame + 2}px`);

      // j opens the next message in the list (the 3rd of the conversation,
      // a short HTML mail) at its top, in a frame as short as the mail.
      const article = page.getByRole("article");
      await scrollTop(page, phone, "end");
      const url = page.url();
      await page.keyboard.press("j");
      await expect(page).not.toHaveURL(url);
      await expect(article.getByRole("region", { name: "Conversation" })).toBeVisible();
      await mailReady(page, /I booked the venue/);
      expect(await scrollTop(page, phone)).toBe(0);
      expect((await fitted(page)).frame).toBeLessThan(300);
    });
  });
}

test.describe("the frame's sandbox and the API guard", () => {
  test.use(desktop);

  test("no script runs in the mail", async ({ page }) => {
    await addToMail(
      page,
      `<script>document.documentElement.dataset.ran = "1"</script>` +
        `<img src="data:," onerror="document.documentElement.dataset.err = '1'">` +
        `<svg><script>document.documentElement.dataset.svg = "1"</script></svg>`,
      "start",
    );
    await openLong(page);
    const frame = frameOf(page);
    await expect(frame).toHaveAttribute("sandbox", sandbox);
    // The frame's load has fired (it is fitted after it): the scripts and the handler had their chance.
    await mailReady(page);
    await fitted(page);
    expect(await frame.evaluate((f: HTMLIFrameElement) => ({ ...f.contentDocument!.documentElement.dataset }))).toEqual({});
  });

  test("an image grows the frame when it arrives, and images from the app's own origin are refused", async ({ page, guards }) => {
    // With remote images: one whose answer the test holds, and one on the
    // app's own origin, which img-src https: http: allows too.
    guards.allowHTTPError("/api/status", 403);
    await addToMail(page, `<img src="https://img.example/late.svg" style="display:block"><img src="/api/status" style="display:none">`, "end", true);
    let onRequest = () => {};
    const requested = new Promise<void>((r) => (onRequest = r));
    let release = () => {};
    const released = new Promise<void>((r) => (release = r));
    await page.route("https://img.example/**", async (route) => {
      onRequest();
      await released;
      await route.fulfill({ contentType: "image/svg+xml", body: `<svg xmlns="http://www.w3.org/2000/svg" width="10" height="300"/>` });
    });
    await openLong(page);
    const frame = frameOf(page);
    const before = await fitted(page);

    const probe = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/status");
    await page.getByRole("article").getByRole("button", { name: "Load remote images" }).click();
    await requested;
    // While the image loads, the new frame keeps the last height: the page does not jump.
    await expect(frame).toHaveAttribute("src", /images=1/);
    expect(await frame.evaluate((f) => f.style.blockSize)).toBe(`${before.frame}px`);
    // The same-origin image went out (with the session cookie) and was refused.
    const answer = await probe;
    // Without the browser's Fetch Metadata it would pass: the session is valid.
    expect(answer.status()).toBe(403);

    // The image arrives: the frame grows by its 300px (and the margin before it at most).
    release();
    await expect.poll(async () => ((await fitOf(page))?.frame ?? 0) - before.frame).toBeGreaterThanOrEqual(300);
    expect((await fitted(page)).frame - before.frame).toBeLessThanOrEqual(320);
  });

  test("links into the app are blocked in the frame, other links open", async ({ page, context }) => {
    await addToMail(
      page,
      `<p><a href="/api/status" target="_self">Status here</a> <a href="/api/status">Status tab</a> <a href="/api/messages?q=x">Search</a> ` +
        `<a href="https://links.example/agenda">Agenda</a></p>` +
        // An open declarative shadow root: the blocker finds the link through the event's composed path.
        `<div><template shadowrootmode="open"><p><a href="/api/openapi.json">Spec</a></p></template></div>`,
      "start",
    );
    await context.route("https://links.example/**", (route) => route.fulfill({ contentType: "text/html", body: "<p>agenda</p>" }));
    const appRequests: string[] = [];
    context.on("request", (r) => {
      const u = new URL(r.url());
      if (u.pathname === "/api/status" || u.pathname === "/api/openapi.json" || (u.pathname === "/api/messages" && u.search === "?q=x")) appRequests.push(r.url());
    });
    await openLong(page);
    const pages: string[] = [];
    context.on("page", (p) => pages.push(p.url()));
    await fitted(page);
    const click = async (name: string, button: "left" | "middle" = "left") => {
      const at = await linkCenter(page, name);
      await page.mouse.click(at.x, at.y, { button });
    };
    await click("Status here");
    await click("Status tab");
    await click("Status tab", "middle");
    await click("Search");
    await click("Spec");
    await click("Spec", "middle");
    // An external link opens: the signal that the clicks before it were handled.
    const opened = context.waitForEvent("page");
    await click("Agenda");
    await opened;
    expect(appRequests).toEqual([]);
    expect(pages).toHaveLength(1);
    expect(await frameOf(page).evaluate((f: HTMLIFrameElement) => new URL(f.contentDocument!.URL).pathname)).toMatch(/\/html$/);
  });

  test("a navigation to an API route a browser does not open is refused", async ({ page, guards }) => {
    guards.allowHTTPError("/api/status", 403);
    const status = await page.goto("/api/status");
    // Refused by the Fetch Metadata guard (a navigation): the session is valid.
    expect(status!.status()).toBe(403);
    expect(await status!.json()).toEqual({ error: "request not allowed from this context" });
    // The API description is one a browser opens.
    const spec = await page.goto("/api/openapi.json");
    expect(spec!.status()).toBe(200);
  });
});
