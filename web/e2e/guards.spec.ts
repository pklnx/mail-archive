// The guards' exceptions for the sandboxed mail frame stay narrow. No
// browser needed: these check the matcher on the messages Chromium logs.
import { test as base, expect } from "@playwright/test";
import { origin } from "./demo";
import { formatConsoleError, formatFrameEvents, fromMailFrame, test, type ConsoleErrorInfo, type FrameEvent, type FrameInfo } from "./fixtures";

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

// The flaky "Blocked script execution in 'about:blank'" error (#260): the guard
// stays strict, and its report says which frame it was.
const blockedBlank = `Blocked script execution in 'about:blank' because the document's frame is sandboxed and the 'allow-scripts' permission is not set.`;

base("a blocked script in about:blank is not an exception, with or without a mail frame around", () => {
  expect(fromMailFrame("about:blank", blockedBlank)).toBe(false);
  expect(fromMailFrame(frame, blockedBlank)).toBe(false);
  expect(fromMailFrame(`${origin}/`, blockedBlank)).toBe(false);
  // The one this suite's own init script causes in the mail frame stays expected.
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
test("a javascript: navigation of a sandboxed frame is reported with that frame", async ({ page, context, guards }) => {
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
  expect(report.split("\n")[0]).toBe(`console error: ${blockedBlank} (about:blank)`);
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
