import type { BrowserContext, Page } from "@playwright/test";
import { referrerOrigin } from "./demo";
import { expect, test } from "./fixtures";
import { frameOf, mailReady } from "./mailFrame";
import { recorder } from "./recorder";

// The server sends Referrer-Policy: no-referrer with every answer, the mail
// frame's included. A mail could override it with <meta name="referrer"> or
// a referrerpolicy attribute, and then the sender would learn the archive's
// address and the message's SHA-256 (/api/messages/<sha>/html) from a click
// or a remote image; with crossorigin, an image would send the archive's
// origin in Origin (GHSA-w6jg-ffvw-q4f5). The server renames content,
// referrerpolicy and crossorigin (neutralizeHTML in internal/web), so none
// of them takes effect. The demo's checks (tools/demo, referrerCheck and
// hintCheck) point their links and images at referrerOrigin, where this spec
// runs a server that records each request's headers (recorder.ts).

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

    // Only <img>: a mail with an SVG <image> has inline SVG, so it gets no
    // remote images at all (css-images.spec.ts, the stereoscope mail).
    const img = server.requested("/attr-img");
    await page.getByRole("article").getByRole("button", { name: "Load remote images" }).click();
    const headers = await img;
    expect(headers.referer, "Referer of the <img>").toBeUndefined();
    expect(headers.origin, "Origin of the <img>").toBeUndefined();
  } finally {
    await server.close();
  }
});
