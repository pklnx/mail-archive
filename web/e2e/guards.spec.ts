// The guards' exceptions for the sandboxed mail frame stay narrow. No
// browser needed: these check the matcher on the messages Chromium and
// WebKit log.
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { test as base, expect, type BrowserContext, type Page } from "@playwright/test";
import { origin } from "./demo";
import { escapeRegExp, formatConsoleError, formatFrameEvents, fromMailFrame, fromWebKitStyleRefusal, isResizeObserverLoop, test, type ConsoleErrorInfo, type FrameEvent, type FrameInfo, type Guards } from "./fixtures";

const frame = `${origin}/api/messages/${"ab".repeat(32)}/html`;
const pixel = "https://tracker.example/pixel.gif";
const policy = `"img-src data:"`;
// Chromium 156 (Playwright's headless shell) and Chromium 141.
const blocked = `Loading the image '${pixel}' violates the following Content Security Policy directive: ${policy}. The action has been blocked.`;
const refused = `Refused to load the image '${pixel}' because it violates the following Content Security Policy directive: ${policy}.\n`;

base("a remote image blocked in the mail frame is expected, in both wordings", () => {
  expect(fromMailFrame(frame, blocked)).toBe(true);
  expect(fromMailFrame(frame, refused)).toBe(true);
  expect(fromMailFrame(`${frame}?images=1`, blocked)).toBe(true);
});

base("other CSP violations still fail", () => {
  // Outside the mail frame: the app itself.
  expect(fromMailFrame(`${origin}/`, blocked)).toBe(false);
  expect(fromMailFrame(`${origin}/api/messages/${"ab".repeat(32)}/parts/1`, blocked)).toBe(false);
  // Another directive, or a style instead of an image.
  expect(fromMailFrame(frame, blocked.replace("img-src", "style-src"))).toBe(false);
  expect(fromMailFrame(frame, blocked.replace("the image", "the stylesheet"))).toBe(false);
  // An image from the demo server itself is not "remote".
  expect(fromMailFrame(frame, blocked.replace(pixel, `${origin}/logo.png`))).toBe(false);
  expect(fromMailFrame(frame, blocked.replace(pixel, "http://127.0.0.1:18181/logo.png"))).toBe(false);
  // A part the frame would load: inline images are embedded as data: URLs,
  // so a blocked part request is an error again (#182).
  expect(fromMailFrame(`${origin}/api/messages/${"ab".repeat(32)}/parts/1`, "Failed to load resource: net::ERR_BLOCKED_BY_RESPONSE.NotSameOrigin")).toBe(false);
  // Inline script, fonts, connections.
  expect(fromMailFrame(frame, `Refused to execute inline script because it violates the following Content Security Policy directive: "script-src 'none'".`)).toBe(false);
  expect(fromMailFrame(frame, `Loading the font 'https://fonts.example/x.woff2' violates the following Content Security Policy directive: "font-src 'none'". The action has been blocked.`)).toBe(false);
});

// WebKit's wording (Playwright's WebKit 27.2, #398), logged with an empty
// location: in the mail frame and on an app page alike, so the matcher takes
// it by its words. An app page still fails through the guards' own listener
// (the browser tests below).
const refusedWebKit = `Refused to load ${pixel} because it does not appear in the img-src directive of the Content Security Policy.`;

base("a remote image blocked in the mail frame is expected in WebKit's wording too", () => {
  expect(fromMailFrame("", refusedWebKit)).toBe(true);
  expect(fromMailFrame(frame, refusedWebKit)).toBe(true);
  expect(fromMailFrame(`${frame}?images=1`, refusedWebKit)).toBe(true);
});

base("other CSP violations in WebKit's wording still fail", () => {
  // A location that is not the mail frame: the app itself, a part.
  expect(fromMailFrame(`${origin}/`, refusedWebKit)).toBe(false);
  expect(fromMailFrame(`${origin}/api/messages/${"ab".repeat(32)}/parts/1`, refusedWebKit)).toBe(false);
  expect(fromMailFrame("about:blank", refusedWebKit)).toBe(false);
  // Another directive.
  for (const directive of ["style-src", "font-src", "frame-src", "connect-src", "media-src"])
    expect(fromMailFrame("", refusedWebKit.replace("img-src", directive))).toBe(false);
  // An image from the demo server itself is not "remote".
  expect(fromMailFrame("", refusedWebKit.replace(pixel, `${origin}/logo.png`))).toBe(false);
  expect(fromMailFrame("", refusedWebKit.replace(pixel, "http://127.0.0.1:18181/logo.png"))).toBe(false);
  // Not WebKit's whole sentence.
  expect(fromMailFrame("", `${refusedWebKit} More.`)).toBe(false);
  expect(fromMailFrame("", `Refused to apply a stylesheet because its hash, its nonce, or 'unsafe-inline' does not appear in the style-src directive of the Content Security Policy.`)).toBe(false);
});

// The flaky "Blocked script execution in 'about:blank'" error (#260): the guard
// stays strict, and its report says which frame it was.
const blockedBlank = `Blocked script execution in 'about:blank' because the document's frame is sandboxed and the 'allow-scripts' permission is not set.`;

base("a blocked script in about:blank is not an exception, with or without a mail frame around", () => {
  expect(fromMailFrame("about:blank", blockedBlank)).toBe(false);
  expect(fromMailFrame(frame, blockedBlank)).toBe(false);
  expect(fromMailFrame(`${origin}/`, blockedBlank)).toBe(false);
  // The one the trace snapshotter causes in the mail frame stays expected.
  expect(fromMailFrame(frame, `Blocked script execution in '${frame}' because the document's frame is sandboxed and the 'allow-scripts' permission is not set.`)).toBe(true);
});

const app: FrameInfo = { name: "", url: `${origin}/inbox`, parent: null };
const mail: FrameInfo = { name: "mail-content", url: "", parent: { name: "", url: `${origin}/inbox` } };
const events: FrameEvent[] = [
  { at: Date.UTC(2026, 9, 24, 12, 0, 0, 100), kind: "navigated", frame: app },
  { at: Date.UTC(2026, 9, 24, 12, 0, 1, 250), kind: "attached", frame: mail },
  { at: Date.UTC(2026, 9, 24, 12, 0, 1, 900), kind: "detached", frame: mail },
];
const error: ConsoleErrorInfo = {
  text: blockedBlank,
  location: { url: "about:blank", lineNumber: 0, columnNumber: 0 },
  at: Date.UTC(2026, 9, 24, 12, 0, 1, 500),
  frames: [app, mail],
};

base("the report of a console error lists the frames with name, URL and parent", () => {
  const lines = formatConsoleError(error, events).split("\n");
  // The first line keeps the shape the guard always had.
  expect(lines[0]).toBe(`console error: ${blockedBlank} (about:blank)`);
  expect(lines).toContain("  location: about:blank:0:0 at 2026-10-24T12:00:01.500Z");
  expect(lines).toContain("  frames (2):");
  expect(lines).toContain(`    name="" url="${origin}/inbox" main frame`);
  expect(lines).toContain(`    name="mail-content" url="" parent(name="" url="${origin}/inbox")`);
});

base("the report of a console error holds the last frame events, oldest first", () => {
  const report = formatConsoleError(error, events);
  expect(report).toContain("  frame events logged until then (last 3):");
  const log = report.split("\n").filter((l) => /^    \S+Z (navigated|attached|detached) /.test(l));
  expect(log).toEqual([
    `    2026-10-24T12:00:00.100Z navigated name="" url="${origin}/inbox" main frame`,
    `    2026-10-24T12:00:01.250Z attached name="mail-content" url="" parent(name="" url="${origin}/inbox")`,
    `    2026-10-24T12:00:01.900Z detached name="mail-content" url="" parent(name="" url="${origin}/inbox")`,
  ]);
  // Only the last few.
  const many = Array.from({ length: 30 }, (_, i) => ({ ...events[0]!, at: error.at - 30 + i }));
  const last = formatConsoleError(error, many, 5);
  expect(last).toContain("frame events logged until then (last 5):");
  expect(last.split("\n").filter((l) => l.includes(" navigated "))).toEqual(
    many.slice(-5).map((e) => `    ${new Date(e.at).toISOString()} navigated name="" url="${origin}/inbox" main frame`),
  );
});

base("the frame event log is one line per event, oldest first", () => {
  expect(formatFrameEvents(events).split("\n")).toEqual([
    `2026-10-24T12:00:00.100Z navigated name="" url="${origin}/inbox" main frame`,
    `2026-10-24T12:00:01.250Z attached name="mail-content" url="" parent(name="" url="${origin}/inbox")`,
    `2026-10-24T12:00:01.900Z detached name="mail-content" url="" parent(name="" url="${origin}/inbox")`,
  ]);
  expect(formatFrameEvents([])).toBe("");
});

// The real thing: a sandboxed frame (as MessageView.tsx has it, no
// allow-scripts) navigated to a javascript: URL logs exactly the CI message.
// The page is served from the demo origin, because the guards abort any other
// host. The test reads what the fixture collected and takes the problem, so
// it does not fail on purpose.
test("a javascript: navigation of a sandboxed frame is reported with that frame", async ({ page, context, guards, browserName }) => {
  await page.route(`${origin}/e2e/blocked-frame`, (route) =>
    route.fulfill({
      contentType: "text/html",
      body: `<!doctype html><title>blocked frame</title><iframe name="diag-frame" sandbox="allow-same-origin allow-popups" src="javascript:void(0)"></iframe>`,
    }),
  );
  // Registered after the fixture's own listener, so the fixture has seen the
  // message when this resolves.
  const logged = context.waitForEvent("console", { predicate: (m) => m.type() === "error" });
  await page.goto(`${origin}/e2e/blocked-frame`);
  expect((await logged).text()).toBe(blockedBlank);

  const problems = guards.takeProblems();
  expect(problems).toHaveLength(1);
  const report = problems[0]!;
  // Engine fact (#398, first WebKit run): WebKit logs it without a location.
  expect(report.split("\n")[0]).toBe(`console error: ${blockedBlank} (${browserName === "webkit" ? "" : "about:blank"})`);
  // The snapshot shows the frame under the page. Chromium gives a frame
  // navigated to javascript: neither a name nor a URL, so the parent is what
  // places it.
  const pageURL = `${origin}/e2e/blocked-frame`;
  expect(report).toContain(`    name="" url="${pageURL}" main frame`);
  expect(report).toContain(`    name="" url="" parent(name="" url="${pageURL}")`);
  expect(report).toContain("  frames (2):");
  // The log shows the frame appearing under the page. It may be recorded
  // after the message, so wait for it.
  await expect.poll(() => guards.frameLog()).toContain(` attached name="" url="" parent(name="" url="${pageURL}")`);
});

// #260: Playwright's DOM snapshotter ran script in every frame while a trace
// was recorded, and the browser logged "Blocked script execution" for each
// sandboxed document, among them an about:blank one. The config records
// traces without DOM snapshots, so a sandboxed frame is quiet again. Both
// tests serve their page from the demo origin (the guards abort any other
// host) and end on a console.error sentinel they wait for, so the check does
// not depend on timing.
const sandboxed = `sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox"`;
const sentinel = "snapshotter-sentinel";

async function logSentinel(page: Page, context: BrowserContext) {
  // Registered after the fixture's own listener, so the fixture has seen the
  // message when this resolves.
  const logged = context.waitForEvent("console", { predicate: (m) => m.text() === sentinel });
  await page.evaluate((text) => console.error(text), sentinel);
  await logged;
}

/** The first lines of the problems so far, which it takes from the guard. */
const takeFirstLines = (guards: Guards) => guards.takeProblems().map((p) => p.split("\n")[0]);
/**
 * The first line of a problem for a console.error the test evaluated in the
 * page, whatever location the engine gives it. Chromium gives none; WebKit
 * may give one (it locates the init scripts' lines at Playwright's
 * web-inspector://bootstrap.js, #398 plan review).
 */
const evaluatedLine = (text: string) => expect.stringMatching(new RegExp(`^console error: ${escapeRegExp(text)} \\([^()]*\\)$`));
const sentinelLine = evaluatedLine(sentinel);

test("a sandboxed frame that commits about:blank logs nothing while the trace records", async ({ page, context, guards, browserName }) => {
  // The setting this test depends on: a change back to DOM snapshots fails
  // here first. WebKit's traces have no screenshots either (#343, #398).
  expect(test.info().project.use.trace).toEqual({ mode: "retain-on-failure", snapshots: { dom: false, aria: true, screen: browserName !== "webkit" } });
  await page.route(`${origin}/e2e/outer`, (route) =>
    route.fulfill({ contentType: "text/html", body: `<!doctype html><button>go</button><iframe ${sandboxed} src="/e2e/inner"></iframe>` }),
  );
  await page.route(`${origin}/e2e/inner`, (route) => route.fulfill({ contentType: "text/html", body: `<!doctype html><p>mail</p>` }));
  await page.goto(`${origin}/e2e/outer`);
  await page.evaluate(
    () =>
      new Promise<void>((resolve) => {
        const f = document.querySelector("iframe")!;
        f.addEventListener("load", () => resolve(), { once: true });
        f.src = "about:blank";
      }),
  );
  await page.getByRole("button", { name: "go" }).click();
  await logSentinel(page, context);
  expect(takeFirstLines(guards)).toEqual([sentinelLine]);
});

test("a script blocked in a sandboxed frame outside the mail frame still fails the guard", async ({ page, context, guards, browserName }) => {
  await page.route(`${origin}/e2e/outer`, (route) =>
    route.fulfill({ contentType: "text/html", body: `<!doctype html><button>go</button><iframe ${sandboxed} src="/e2e/inner"></iframe>` }),
  );
  await page.route(`${origin}/e2e/inner`, (route) =>
    route.fulfill({ contentType: "text/html", body: `<!doctype html><p>mail</p><script>parent.document.title = "ran"</script>` }),
  );
  await page.goto(`${origin}/e2e/outer`);
  await page.getByRole("button", { name: "go" }).click();
  await logSentinel(page, context);
  // Engine fact: WebKit logs it without a location (#398 plan review).
  const location = browserName === "webkit" ? "" : `${origin}/e2e/inner`;
  expect(takeFirstLines(guards)).toEqual([
    `console error: Blocked script execution in '${origin}/e2e/inner' because the document's frame is sandboxed and the 'allow-scripts' permission is not set. (${location})`,
    sentinelLine,
  ]);
  expect(await page.title()).toBe("");
});

// The app's own CSP violations fail the guards in every engine (#343, #398):
// the guards make no exception for WebKit. In WebKit, Playwright's trace
// screenshots add an inline style to the page, which the app's CSP refuses;
// the webkit project records no screenshots instead. The app page is the
// real one, with the app's CSP; the test changes its document and waits for
// the browser's securitypolicyviolation event (the guards' listener, from
// the init script, has run before), then for a console sentinel, so the
// guard has seen every message before.
async function violate(page: Page, what: { style: string } | { img: string }) {
  await page.goto("/");
  await expect(page.getByRole("list", { name: "Messages" }).getByRole("button").first()).toBeVisible();
  await page.evaluate(
    (what) =>
      new Promise<void>((resolve) => {
        document.addEventListener("securitypolicyviolation", () => resolve(), { once: true });
        if ("style" in what) {
          const style = document.createElement("style");
          style.textContent = what.style;
          document.head.append(style);
        } else {
          const img = document.createElement("img");
          img.src = what.img;
          document.body.append(img);
        }
      }),
    what,
  );
}

test("an inline style in the app page fails the guards", async ({ page, context, guards }) => {
  await violate(page, { style: "body { outline: 1px solid red }" });
  await logSentinel(page, context);
  const problems = takeFirstLines(guards);
  // The guards' listener names the directive: style-src-elem, or style-src.
  expect(problems).toContainEqual(expect.stringMatching(/^console error: CSP violation: style-src(-elem)? blocked inline in /));
  expect(problems).toContainEqual(sentinelLine);
});

test("a remote image in the app page fails the guards", async ({ page, context, guards }) => {
  // The app's img-src refuses it before any request. WebKit's own console
  // line for it has no location and matches the mail frame's exception
  // (fixtures.ts); the guards' listener still reports it.
  const host = "https://img.example";
  await violate(page, { img: `${host}/app-page.png` });
  await logSentinel(page, context);
  const problems = takeFirstLines(guards);
  // The blocked URI is the image's URL, or only its origin where the engine
  // strips a cross-origin one for reports.
  expect(problems).toContainEqual(expect.stringMatching(new RegExp(`^console error: CSP violation: img-src blocked ${escapeRegExp(host)}(/app-page\\.png)? in `)));
  expect(problems).toContainEqual(sentinelLine);
});

// WebKit's own <select> style (#398): WebKit checks a style attribute in
// the select's user-agent shadow tree against the page's CSP, and the guards
// skip exactly that report (fixtures.ts). These run in both engines; in
// Chromium no select reports anything. A style attribute that the page sets,
// on a select or elsewhere, still fails. Each step ends on a marker: a style
// attribute the test sets on its own element, which every engine refuses
// and reports after the step's own reports.
const styleAttrLines = (problems: string[]) => problems.filter((p) => p.startsWith("console error: CSP violation: style-src-attr ")).length;

/** Runs the step in the page, then sets the marker and waits until the guards have its report; returns the style-src-attr reports. */
async function styleAttrReports(page: Page, context: BrowserContext, guards: Guards, step: () => void): Promise<number> {
  await page.evaluate(step);
  await page.evaluate(
    () =>
      new Promise<void>((resolve) => {
        const marker = document.createElement("p");
        marker.id = "style-marker";
        document.body.append(marker);
        const done = (e: SecurityPolicyViolationEvent) => {
          if (e.target !== marker) return;
          document.removeEventListener("securitypolicyviolation", done);
          resolve();
        };
        document.addEventListener("securitypolicyviolation", done);
        marker.setAttribute("style", "color: red");
      }),
  );
  await logSentinel(page, context);
  // One report is the marker's.
  return styleAttrLines(guards.takeProblems()) - 1;
}

async function appPage(page: Page) {
  await page.goto("/");
  await expect(page.getByRole("list", { name: "Messages" }).getByRole("button").first()).toBeVisible();
}

test("a select created by the page is no problem, attached or not", async ({ page, context, guards }) => {
  await appPage(page);
  expect(
    await styleAttrReports(page, context, guards, () => {
      const attached = document.createElement("select");
      attached.append(new Option("a"));
      document.body.append(attached);
      document.createElement("select").append(new Option("b"));
    }),
  ).toBe(0);
});

test("a style attribute on a select still fails the guards", async ({ page, context, guards }) => {
  await appPage(page);
  // The select first, so its own WebKit report is settled.
  expect(
    await styleAttrReports(page, context, guards, () => {
      const select = document.createElement("select");
      select.id = "styled-select";
      document.body.append(select);
    }),
  ).toBe(0);
  expect(await styleAttrReports(page, context, guards, () => document.getElementById("styled-select")!.setAttribute("style", "color: red"))).toBe(1);
});

test("a style attribute on a new element right after a new select still fails the guards", async ({ page, context, guards }) => {
  await appPage(page);
  // Both reports target the document in WebKit: only the select's is skipped.
  expect(
    await styleAttrReports(page, context, guards, () => {
      document.createElement("select");
      document.createElement("div").setAttribute("style", "color: red");
    }),
  ).toBe(1);
});

base("WebKit's own style refusal line and the ResizeObserver loop are matched exactly", () => {
  const refused = "Refused to apply a stylesheet because its hash, its nonce, or 'unsafe-inline' does not appear in the style-src directive of the Content Security Policy.";
  expect(fromWebKitStyleRefusal(refused)).toBe(true);
  expect(fromWebKitStyleRefusal(refused.replace("style-src", "script-src"))).toBe(false);
  expect(fromWebKitStyleRefusal(`${refused} `)).toBe(false);
  expect(fromWebKitStyleRefusal("Applying inline style violates the following Content Security Policy directive 'style-src 'self''.")).toBe(false);
  const loop = "ResizeObserver loop completed with undelivered notifications.";
  expect(isResizeObserverLoop({ message: loop, stack: "" })).toBe(true);
  expect(isResizeObserverLoop({ message: loop })).toBe(true);
  expect(isResizeObserverLoop({ message: loop, stack: "Error: x\n    at f (app.js:1:1)" })).toBe(false);
  expect(isResizeObserverLoop({ message: "ResizeObserver loop limit exceeded" })).toBe(false);
  expect(isResizeObserverLoop({ message: `${loop} ` })).toBe(false);
});

// guards.takeProblems() empties the guard, so only the guards' own tests here
// may call it; anything else declares its errors with expectConsoleError. A
// shared helper counts too: it would silence the guard for every spec that
// uses it.
base("only guards.spec.ts calls takeProblems", () => {
  const dir = dirname(fileURLToPath(import.meta.url));
  const files = readdirSync(dir).filter((f) => f.endsWith(".ts") && f !== "fixtures.ts" && f !== "guards.spec.ts");
  expect(files).toContain("reading.spec.ts");
  expect(files.filter((f) => readFileSync(join(dir, f), "utf8").includes("takeProblems"))).toEqual([]);
});

// expectConsoleError: a declared error, as often as declared, is no problem;
// one more is, and so is one that never comes. The tests log through the
// page and waits for the console event, as logSentinel does.
const declared = "declared-console-error";

async function logError(page: Page, context: BrowserContext, text: string) {
  // Registered after the fixture's own listener, so the fixture has seen the
  // message when this resolves.
  const logged = context.waitForEvent("console", { predicate: (m) => m.text() === text });
  await page.evaluate((text) => console.error(text), text);
  await logged;
}

test("an expected console error, logged once, is no problem", async ({ page, context, guards }) => {
  guards.expectConsoleError(new RegExp(`^${declared}$`));
  await logError(page, context, declared);
  expect(guards.takeProblems()).toEqual([]);
});

test("an expected console error logged more often than expected is a problem from the next one on", async ({ page, context, guards }) => {
  guards.expectConsoleError(new RegExp(`^${declared}`));
  await logError(page, context, `${declared} 1`);
  await logError(page, context, `${declared} 2`);
  expect(takeFirstLines(guards)).toEqual([evaluatedLine(`${declared} 2`)]);
});

// The teardown's problem is the failure this test expects; the body is only
// the declaration, so nothing else can fail it.
test("an expected console error that never comes fails the test", async ({ guards }) => {
  test.fail();
  guards.expectConsoleError(new RegExp(`^${declared}$`));
});
