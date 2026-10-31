import { expect, test } from "./fixtures";
import { frameOf, mailReady } from "./mailFrame";

// Links into the app in an HTML mail (#459), in every engine the config
// runs (E2E_BROWSER): the server renames every href that is not an external
// http(s), mailto: or tel: link, and every attributeName and http-equiv
// (neutralizeHTML in internal/web), so nothing in the browser has to cancel
// a click. WebKit does not run the listeners the web UI adds to the
// sandboxed frame's document, so there this is the layer that holds; in
// Chromium the web UI's blocker is a further one. The demo's rendering
// check (tools/demo, neutralized) holds the cases: boxes marked
// data-link-case, each filled by a link to /api/openapi.json?link=<name>
// (an API route a browser opens: the Fetch Metadata guard lets it through)
// or to a javascript: or data: URL, and a meta refresh. Unlike the client
// blocker's tests in reading.spec.ts, which add their markup after the
// server, this reads what the server sent.

const cases = [
  "root",
  "self",
  "relative",
  "scheme-relative",
  "absolute",
  "uppercase",
  "ip",
  "entity",
  "slash",
  "backslash",
  "fragment",
  "empty",
  "bare",
  "javascript",
  "data",
  "area",
  "svg",
  "svg-xlink",
  "use",
  "set",
  "animate",
  "mathml",
];

/** What the parent page keeps of the mail's document, so that a navigation of the frame shows. */
interface Kept {
  linkDoc?: Document;
  linkDocURL?: string;
  linkFrameLoads?: number;
}

test("no link in the mail leads into the app, whatever the engine", async ({ page, context }) => {
  await context.route("https://links.example/**", (route) => route.fulfill({ contentType: "text/html", body: "<p>check</p>" }));
  const linkRequests: string[] = [];
  context.on("request", (r) => {
    if (r.url().includes("link=")) linkRequests.push(r.url());
  });
  await page.goto("/?q=kaleidoscope");
  const list = page.getByRole("list", { name: "Messages" });
  await expect(list.getByRole("button")).toHaveCount(1);
  await list.getByRole("button").click();
  await expect(page.getByRole("article").getByRole("heading", { level: 1 })).toHaveText("Rendering check: boxes that stay empty");
  await mailReady(page, /kaleidoscope rendering check/);
  const pages: string[] = [];
  context.on("page", (p) => pages.push(p.url()));
  // The document and the frame's loads from now on, kept in the parent page.
  await frameOf(page).evaluate((f: HTMLIFrameElement) => {
    const kept = window as Kept;
    kept.linkDoc = f.contentDocument!;
    kept.linkDocURL = f.contentDocument!.URL;
    kept.linkFrameLoads = 0;
    f.addEventListener("load", () => kept.linkFrameLoads!++);
  });
  const frameState = () =>
    frameOf(page).evaluate((f: HTMLIFrameElement) => {
      const kept = window as Kept;
      return {
        same: f.contentDocument === kept.linkDoc,
        url: f.contentDocument?.URL === kept.linkDocURL,
        loads: kept.linkFrameLoads,
      };
    });

  // 1. The DOM: no box holds a link or an animation of one, and no meta
  // refresh is left; the external sentinel keeps its href.
  const dom = await frameOf(page).evaluate((f: HTMLIFrameElement) => {
    const doc = f.contentDocument!;
    const xlink = "http://www.w3.org/1999/xlink";
    const boxes = [...doc.querySelectorAll("[data-link-case]")];
    const offending = boxes
      .flatMap((b) => [b, ...b.querySelectorAll("*")])
      .filter((e) => e.hasAttribute("href") || e.hasAttributeNS(xlink, "href") || e.getAttributeNames().some((n) => n.toLowerCase() === "attributename"))
      .map((e) => `${e.closest("[data-link-case]")!.getAttribute("data-link-case")}: <${e.localName} ${e.getAttributeNames().join(" ")}>`);
    return {
      cases: boxes.map((b) => b.getAttribute("data-link-case")),
      offending,
      refresh: doc.querySelectorAll("meta[http-equiv]").length,
      sentinel: doc.querySelectorAll('a[href="https://links.example/kaleidoscope"]').length,
    };
  });
  // Soft: a failure here still lets the clicks show what the links do.
  expect(dom.cases).toEqual(cases);
  expect.soft(dom.offending).toEqual([]);
  expect.soft(dom.refresh).toBe(0);
  expect(dom.sentinel).toBe(1);

  // 2. A left and a middle click at the centre of every box. Each box is
  // scrolled into view first (the mail is taller than the window), and the
  // point is on the frame, not on the page's own panels.
  const viewport = page.viewportSize()!;
  for (const name of cases) {
    await test.step(name, async () => {
      // A click on an earlier box that navigated the frame shows here.
      expect(await frameState(), `the frame still shows the mail before ${name}`).toEqual({ same: true, url: true, loads: 0 });
      const at = await frameOf(page).evaluate((f: HTMLIFrameElement, name) => {
        const e = (window as Kept).linkDoc!.querySelector(`[data-link-case="${name}"]`)!;
        e.scrollIntoView({ block: "center" });
        const r = e.getBoundingClientRect();
        const box = f.getBoundingClientRect();
        return {
          x: box.left + f.clientLeft + r.left + r.width / 2,
          y: box.top + f.clientTop + r.top + r.height / 2,
        };
      }, name);
      expect(at.y, name).toBeGreaterThan(0);
      expect(at.y, name).toBeLessThan(viewport.height);
      expect(await page.evaluate(({ x, y }) => document.elementFromPoint(x, y)?.className, at), name).toBe("message__frame");
      await page.mouse.click(at.x, at.y);
      await page.mouse.click(at.x, at.y, { button: "middle" });
    });
  }
  // A click that navigated the frame would leave no sentinel to click.
  expect(await frameState(), "the frame still shows the mail").toEqual({
    same: true,
    url: true,
    loads: 0,
  });

  // 3. The external link opens: the signal that the clicks before it were handled.
  const opened = context.waitForEvent("page");
  const at = await frameOf(page).evaluate((f: HTMLIFrameElement) => {
    const a = (window as Kept).linkDoc!.querySelector('a[href="https://links.example/kaleidoscope"]')!;
    a.scrollIntoView({ block: "center" });
    const r = a.getBoundingClientRect();
    const box = f.getBoundingClientRect();
    return {
      x: box.left + f.clientLeft + r.left + r.width / 2,
      y: box.top + f.clientTop + r.top + r.height / 2,
    };
  });
  await page.mouse.click(at.x, at.y);
  const tab = await opened;
  await expect.poll(() => tab.url()).toBe("https://links.example/kaleidoscope");

  // 4. No request into the app, no other page, and the frame never left the mail.
  expect(linkRequests).toEqual([]);
  expect(pages).toHaveLength(1);
  expect(await frameState()).toEqual({ same: true, url: true, loads: 0 });
});
