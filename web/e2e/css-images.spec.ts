import { chromium, type BrowserContext, type Page, type Request } from "@playwright/test";
import { origin, referrerOrigin } from "./demo";
import { expect, test } from "./fixtures";
import { adminState } from "./global-setup";
import { frameOf } from "./mailFrame";
import { recorder } from "./recorder";

// Chromium sends the archive's origin with every image a document's CSS
// loads (backgrounds, list bullets, border images, masks, shape-outside,
// SVG fill and so on), in Referer and for cors-mode ones in Origin,
// whatever the referrer policy. So the server makes every URL in a served
// mail's CSS inert (neutralizeCSS in internal/web), and with "Load remote
// images" it refuses remote images for a mail with inline SVG or MathML
// (foreignContent) or whose CSS it parses as still loading something
// (cssFetchSurvivors). The demo's CSS checks (tools/demo,
// cssCheck and cssSVGCheck) point every construct at its own path
// /css-<name> on referrerOrigin, where the recorder runs (recorder.ts).
//
// The browser's request events record what the mail asks for: they fire
// when the browser issues a request, in the mail frame too, also for one
// the CSP then blocks; a leak is one that got an answer. The mail's own
// CSS images are issued before its frame's load. The positive controls show
// that this browser leaks from this frame (so an absence means something):
// a CSS background and a CSS mask that the test adds to the mail's document
// through contentDocument (no script runs in the frame) must arrive with
// the origin. A last control, added after them, is the signal for the
// negative check: once it is issued, nothing the mail asked for is still
// to come. The check runs in the fixture's page (the project's browser: the
// headless shell in the chromium project, WebKit in the webkit project) and
// in Playwright's full Chromium, with default launch arguments.

/** The /css-* requests that the browser issued, in order; also those the CSP then blocks. */
function issuedCSS(context: BrowserContext) {
  const requests: Request[] = [];
  context.on("request", (r) => {
    const u = new URL(r.url());
    if (u.origin === referrerOrigin && u.pathname.startsWith("/css-")) requests.push(r);
  });
  return requests;
}

/** Settles with the browser's request for path on the recorder. */
function issued(context: BrowserContext, path: string): Promise<Request> {
  return context.waitForEvent("request", { predicate: (r) => r.url() === referrerOrigin + path, timeout: 0 });
}

/** Opens the demo mail found by word. */
async function openMail(page: Page, word: string, subject: string) {
  await page.goto(`/?q=${word}`);
  const list = page.getByRole("list", { name: "Messages" });
  await expect(list.getByRole("button")).toHaveCount(1);
  await list.getByRole("button").click();
  await expect(page.getByRole("article").getByRole("heading", { level: 1 })).toHaveText(subject);
}

/** Waits until the frame shows the mail loaded with ?images=1. */
async function imagesLoaded(page: Page, word: string) {
  await expect
    .poll(() =>
      frameOf(page).evaluate(
        (f: HTMLIFrameElement, word) => {
          const doc = f.contentDocument;
          return !!doc && new URL(doc.URL).search === "?images=1" && doc.readyState === "complete" && (doc.body.textContent ?? "").includes(word);
        },
        `${word} check`,
      ),
    )
    .toBe(true);
}

/** Adds an element with this style to the mail's document, from the parent page. */
async function addToMail(page: Page, style: string) {
  await frameOf(page).evaluate((f: HTMLIFrameElement, style) => {
    const doc = f.contentDocument!;
    const el = doc.createElement("div");
    el.setAttribute("style", `display:block;width:24px;height:24px;${style}`);
    doc.body.appendChild(el);
  }, style);
}

/**
 * The mail's requests (not the controls) that got an answer, each with the
 * Referer and Origin the recorder saw. It waits for each issued request to
 * end: with an answer, or failed (the CSP blocks a request after the
 * browser issued it).
 */
async function leaks(requests: Request[], headers: (path: string) => Record<string, unknown> | undefined) {
  const out: string[] = [];
  for (const r of requests) {
    const path = new URL(r.url()).pathname;
    if (path.startsWith("/css-control-") || !(await r.response())) continue;
    const h = headers(path);
    out.push(`${path} Referer=${String(h?.referer)} Origin=${String(h?.origin)}`);
  }
  return out;
}

/** The web UI's hint after Load remote images for a mail the server refuses them for. */
const refusedHint = "Remote images stay blocked for this message";

/** The zoetrope check in page: after "Load remote images", only the controls load. */
async function checkCSSImages(page: Page, context: BrowserContext, browserName: string) {
  const server = await recorder(context);
  const requests = issuedCSS(context);
  try {
    await openMail(page, "zoetrope", "CSS image check: backgrounds that stay empty");
    const img = issued(context, "/css-control-img");
    await page.getByRole("article").getByRole("button", { name: "Load remote images" }).click();
    await img;
    await imagesLoaded(page, "zoetrope");

    // Positive controls: remote images are on, and a CSS background and a
    // CSS mask in this frame send the origin.
    const bg = server.requested("/css-control-bg");
    const mask = server.requested("/css-control-mask");
    await addToMail(page, `background-image:url(${referrerOrigin}/css-control-bg)`);
    await addToMail(page, `mask-image:url(${referrerOrigin}/css-control-mask)`);
    // Engine fact (#398, first WebKit run): WebKit honours the frame's
    // Referrer-Policy: no-referrer for CSS images and sends no Referer;
    // Chromium sends the origin whatever the policy. The request itself is
    // the control in both.
    expect((await bg).referer, "Referer of the control background").toBe(browserName === "webkit" ? undefined : `${origin}/`);
    expect((await mask).origin, "Origin of the control mask").toBe(origin);
    expect(await leaks(requests, server.headers), "the mail's CSS images, checked after the controls").toEqual([]);

    // The last control: issued after everything before it.
    const last = issued(context, "/css-control-final");
    await addToMail(page, `background-image:url(${referrerOrigin}/css-control-final)`);
    await last;
    expect(await leaks(requests, server.headers), "the mail's CSS images, checked at the end").toEqual([]);
    // Remote images loaded, so no hint that they stay blocked.
    await expect(page.getByRole("article").getByText(refusedHint)).toHaveCount(0);
  } finally {
    await server.close();
  }
}

test("CSS images in a mail stay unloaded after Load remote images (the project's browser)", async ({ page, context, browserName }) => {
  await checkCSSImages(page, context, browserName);
});

// It launches its own Chromium whatever the project's browser, so it runs in
// the chromium project only (@chromium-launch, #398).
test("CSS images in a mail stay unloaded after Load remote images (full Chromium)", { tag: "@chromium-launch" }, async () => {
  // The full Chromium that `playwright install chromium` brings, as in
  // hints.spec.ts, but with default arguments and the admin session.
  const browser = await chromium.launch({ channel: "chromium", executablePath: process.env.CHROMIUM_PATH || undefined });
  try {
    const context = await browser.newContext({ baseURL: origin, storageState: adminState, locale: "en-US", timezoneId: "Europe/Berlin" });
    await checkCSSImages(await context.newPage(), context, "chromium");
  } finally {
    await browser.close();
  }
});

// The stereoscope check: a mail with inline SVG and MathML, among them SVG
// <style> that the server's HTML parser does not see where the browser
// does (a duplicate attribute with the value "/", a <template> in an
// integration point). The server refuses remote images for any such mail,
// so the CSP of its ?images=1 document keeps img-src data:, and the browser
// blocks every remote image of that document before it issues a request.
// None may have been issued once that document is loaded, nor by the end,
// and the view says that remote images stay blocked.
test("a mail with inline SVG or MathML gets no remote images, and the view says so", async ({ page, context }) => {
  const server = await recorder(context);
  const requests = issuedCSS(context);
  try {
    await openMail(page, "stereoscope", "CSS check, SVG style: images that stay off");
    const html = page.waitForResponse((r) => new URL(r.url()).search === "?images=1" && /\/html$/.test(new URL(r.url()).pathname));
    await page.getByRole("article").getByRole("button", { name: "Load remote images" }).click();
    const csp = (await html).headers()["content-security-policy"] ?? "";
    expect(csp, "the CSP of the refused mail").toMatch(/(^|; )img-src data:;/);
    expect(csp).not.toMatch(/https?:/);
    await imagesLoaded(page, "stereoscope");
    await expect(page.getByRole("article").getByRole("status")).toContainText(refusedHint);
    expect(await leaks(requests, server.headers), "the mail's CSS images, once loaded").toEqual([]);
  } finally {
    await server.close();
  }
});
