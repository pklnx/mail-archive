import { createServer, type IncomingHttpHeaders } from "node:http";
import type { BrowserContext, Page } from "@playwright/test";
import { referrerOrigin } from "./demo";
import { expect, test } from "./fixtures";
import { frameOf, mailReady } from "./mailFrame";

// The server sends Referrer-Policy: no-referrer with every answer, the mail
// frame's included. A mail could override it with <meta name="referrer"> or
// a referrerpolicy attribute, and then the sender would learn the archive's
// address and the message's SHA-256 (/api/messages/<sha>/html) from a click
// or a remote image; with crossorigin, an image would send the archive's
// origin in Origin (GHSA-w6jg-ffvw-q4f5). The server renames content,
// referrerpolicy and crossorigin (neutralizeHTML in internal/web), so none
// of them takes effect. The demo's checks (tools/demo, referrerCheck and
// hintCheck) point their links and images at referrerOrigin, where this spec
// runs a server that records each request's headers.

/** Starts the recording server; requested(path) settles with the headers of the first request for path. */
async function recorder(context: BrowserContext) {
  const seen = new Map<string, IncomingHttpHeaders>();
  const waiting = new Map<string, (h: IncomingHttpHeaders) => void>();
  const server = createServer((req, res) => {
    const path = new URL(req.url ?? "/", referrerOrigin).pathname;
    if (!seen.has(path)) {
      seen.set(path, req.headers);
      waiting.get(path)?.(req.headers);
    }
    if (/-(img|svgimg)$/.test(path)) {
      // A 1×1 transparent GIF.
      res.writeHead(200, { "content-type": "image/gif" }).end(Buffer.from("R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7", "base64"));
    } else {
      res.writeHead(200, { "content-type": "text/html" }).end("<p>check</p>");
    }
  });
  const { hostname, port } = new URL(referrerOrigin);
  await new Promise<void>((resolve, reject) => server.once("error", reject).listen(Number(port), hostname, resolve));
  // Past the guards' block of other hosts: this one is ours.
  await context.route(`${referrerOrigin}/**`, (route) => route.continue());
  return {
    requested: (path: string) =>
      new Promise<IncomingHttpHeaders>((resolve) => {
        const h = seen.get(path);
        if (h) resolve(h);
        else waiting.set(path, resolve);
      }),
    close: () => new Promise((resolve) => server.close(resolve)),
  };
}

/** Opens the demo mail found by word. */
async function openMail(page: Page, word: string, subject: string) {
  await page.goto(`/?q=${word}`);
  const list = page.getByRole("list", { name: "Messages" });
  await expect(list.getByRole("button")).toHaveCount(1);
  await list.getByRole("button").click();
  await expect(page.getByRole("article").getByRole("heading", { level: 1 })).toHaveText(subject);
  await mailReady(page, new RegExp(`${word} check`));
}

/** Clicks the mail's link to href at its centre, from the parent page, and closes the tab it opens. */
async function clickLink(page: Page, context: BrowserContext, href: string) {
  const at = await frameOf(page).evaluate((f: HTMLIFrameElement, href) => {
    const a = f.contentDocument!.querySelector(`a[href="${href}"]`)!;
    a.scrollIntoView({ block: "center" });
    const r = a.getBoundingClientRect();
    const box = f.getBoundingClientRect();
    return { x: box.left + f.clientLeft + r.left + r.width / 2, y: box.top + f.clientTop + r.top + r.height / 2 };
  }, href);
  const opened = context.waitForEvent("page");
  await page.mouse.click(at.x, at.y);
  return opened;
}

test("a mail's <meta name=referrer> does not override the server's policy", async ({ page, context }) => {
  const server = await recorder(context);
  try {
    await openMail(page, "semaphore", "Referrer check: links that tell nothing");
    const click = server.requested("/meta-click");
    const tab = await clickLink(page, context, `${referrerOrigin}/meta-click`);
    expect((await click).referer, "Referer of the link click").toBeUndefined();
    await tab.close();

    const img = server.requested("/meta-img");
    await page.getByRole("article").getByRole("button", { name: "Load remote images" }).click();
    expect((await img).referer, "Referer of the remote image").toBeUndefined();
  } finally {
    await server.close();
  }
});

test("a mail's referrerpolicy and crossorigin attributes do not take effect", async ({ page, context }) => {
  const server = await recorder(context);
  try {
    await openMail(page, "periscope", "Request check: hints that stay silent");
    const click = server.requested("/attr-click");
    const tab = await clickLink(page, context, `${referrerOrigin}/attr-click`);
    expect((await click).referer, "Referer of the link click").toBeUndefined();
    await tab.close();

    const img = server.requested("/attr-img");
    const svg = server.requested("/attr-svgimg");
    await page.getByRole("article").getByRole("button", { name: "Load remote images" }).click();
    for (const [name, headers] of [
      ["<img>", await img],
      ["SVG <image>", await svg],
    ] as const) {
      expect(headers.referer, `Referer of the ${name}`).toBeUndefined();
      expect(headers.origin, `Origin of the ${name}`).toBeUndefined();
    }
  } finally {
    await server.close();
  }
});
