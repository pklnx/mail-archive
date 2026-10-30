import { expect } from "./fixtures";
import type { Page } from "@playwright/test";

// The mail frame in the e2e specs, shared by reading.spec.ts and the specs
// of popovers that must close when the focus enters the frame (#349).

export const frameOf = (page: Page) => page.getByRole("article").getByTitle("Message content");

// Everything about the mail's document is read from the parent page through
// iframe.contentDocument (the frame is same-origin), and clicks into it are
// mouse clicks at coordinates: no Playwright locator or evaluate runs inside
// the frame. Such a call runs Playwright's own script in the frame, and
// Playwright injecting script into the sandboxed frame is what logged the
// #260 errors (there, the trace snapshotter). If the frame is still its
// initial about:blank, the call also reads a different document than the mail.

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
export async function mailReady(page: Page, text?: RegExp) {
  await expect.poll(() => mailLoaded(page, text)).toBe(true);
}

/** The conversation's toggle in the action row (#203). */
export const conversationToggle = (page: Page) => page.getByRole("article").getByRole("button", { name: /^Conversation \(\d+\)$/ });

/** Waits until the open message is laid out: heading, the conversation's toggle (it loads on its own) and the frame's content. */
async function ready(page: Page, subject: RegExp) {
  const article = page.getByRole("article");
  await expect(article.getByRole("heading", { level: 1 })).toHaveText(subject);
  await expect(conversationToggle(page)).toBeVisible();
  await mailReady(page);
}

/**
 * Opens the long HTML mail. Found by its word "itinerary", then opened in
 * the full list (?m=), so j has a next message.
 */
export async function openLong(page: Page) {
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
 * Clicks into the mail's document at a point of its visible part that is
 * not a link and not covered by the page's own panels: the left edge of the
 * frame, vertically in the middle of what shows. The point is computed in
 * the parent from the frame's box; the mail's document is only read.
 */
export async function clickIntoFrame(page: Page) {
  const at = await frameOf(page).evaluate((f: HTMLIFrameElement) => {
    const box = f.getBoundingClientRect();
    const top = Math.max(box.top, document.querySelector("header")!.getBoundingClientRect().bottom);
    const bottom = Math.min(box.bottom, window.innerHeight);
    const x = box.left + f.clientLeft + 6;
    const y = (top + bottom) / 2;
    const inside = f.contentDocument!.elementFromPoint(x - box.left - f.clientLeft, y - box.top - f.clientTop);
    return { x, y, link: inside?.closest("a") != null };
  });
  expect(at.link).toBe(false);
  await page.mouse.click(at.x, at.y);
}
