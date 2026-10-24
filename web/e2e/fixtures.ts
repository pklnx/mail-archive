// The test fixture every spec uses. Its guards fail a test on:
// - an uncaught error in a page,
// - a console error,
// - a Content Security Policy violation,
// - a request to any host but the demo server (the request is aborted).
// They keep the suite off the network and catch CDN fonts, injected styles
// and similar regressions while the UI changes.
// A console error also says where it came from: the frames of the page at
// that moment and the recent frame events (#260), because a console message
// itself does not name its frame.
import { writeFile } from "node:fs/promises";
import { expect, test as base, type Frame, type Locator, type Page } from "@playwright/test";
import { origin } from "./demo";

export { expect };

// Console errors that come from the mail's own HTML in the sandboxed message
// frame (/api/messages/<id>/html), not from the app. The browser logs each
// thing the frame's sandbox and CSP block, and blocking is their job.
const escapedOrigin = origin.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
const message = `${escapedOrigin}/api/messages/[0-9a-f]{64}`;
// The HTML view of a message, with or without ?images=….
const mailFrame = new RegExp(`^${message}/html(\\?[^#]*)?$`);
// An absolute http(s) URL on another host than the demo server.
const remote = `https?://(?!localhost[/:]|127\\.0\\.0\\.1[/:])[^'\\s]+`;
const mailFrameErrors: { text: RegExp; url: RegExp }[] = [
  // A remote image the frame's img-src refused: the "Load remote images"
  // gate working. Only images, only remote ones, only in the mail frame;
  // every other CSP violation still fails the test. Chromium has two
  // wordings: up to about 141 "Refused to load …", later "Loading … violates".
  {
    text: new RegExp(`^Refused to load the image '${remote}' because it violates the following Content Security Policy directive: "img-src [^"]*"\\.\\s*$`),
    url: mailFrame,
  },
  {
    text: new RegExp(`^Loading the image '${remote}' violates the following Content Security Policy directive: "img-src [^"]*"\\. The action has been blocked\\.\\s*$`),
    url: mailFrame,
  },
  // The mail's own <script>, which the sandbox (no allow-scripts) blocks and
  // the browser logs, e.g. in "no script runs in the mail". Playwright's DOM
  // snapshotter used to add to these in every frame, and for a frame that had
  // committed about:blank; the config records traces without DOM snapshots
  // now, so it does not (#260). Nothing here covers about:blank.
  {
    text: new RegExp(`^Blocked script execution in '${message}/html[^']*' because the document's frame is sandboxed and the 'allow-scripts' permission is not set\\.$`),
    url: /.*/, // reported with or without the frame's URL
  },
];

export function fromMailFrame(url: string, text: string): boolean {
  return mailFrameErrors.some((e) => e.text.test(text) && e.url.test(url));
}

/** A frame as the diagnostics record it. A frame whose URL is empty (a
 * javascript: navigation) is told apart by its name and its parent. */
export interface FrameInfo {
  name: string;
  url: string;
  parent: { name: string; url: string } | null;
}

/** One frameattached, framenavigated or framedetached event. */
export interface FrameEvent {
  /** Milliseconds since the Unix epoch. */
  at: number;
  kind: "attached" | "navigated" | "detached";
  frame: FrameInfo;
}

/** A console error with what the browser and the page knew when it came. */
export interface ConsoleErrorInfo {
  text: string;
  /** msg.location(): the URL of the document, which is not the frame. */
  location: { url: string; lineNumber: number; columnNumber: number };
  /** msg.timestamp(), milliseconds since the Unix epoch. */
  at: number;
  /** page.frames() at that moment. */
  frames: FrameInfo[];
}

export function describeFrame(f: FrameInfo): string {
  const parent = f.parent ? `parent(name=${JSON.stringify(f.parent.name)} url=${JSON.stringify(f.parent.url)})` : "main frame";
  return `name=${JSON.stringify(f.name)} url=${JSON.stringify(f.url)} ${parent}`;
}

/** The frame events as lines, oldest first: time, event, frame. */
export function formatFrameEvents(events: FrameEvent[]): string {
  return events.map((e) => `${new Date(e.at).toISOString()} ${e.kind} ${describeFrame(e.frame)}`).join("\n");
}

/**
 * The guard's problem text for a console error: the first line is the
 * message and the document URL, then where and when, the frames of the page
 * at that moment, and the last frame events logged until then.
 */
export function formatConsoleError(e: ConsoleErrorInfo, events: FrameEvent[] = [], recent = 12): string {
  const lines = [
    `console error: ${e.text} (${e.location.url})`,
    `  location: ${e.location.url}:${e.location.lineNumber}:${e.location.columnNumber} at ${new Date(e.at).toISOString()}`,
    `  frames (${e.frames.length}):`,
    ...e.frames.map((f) => `    ${describeFrame(f)}`),
  ];
  // No filter by time: the console timestamp is the browser's, the events'
  // times are the test's, and a frame event may arrive after the message.
  const before = events.slice(-recent);
  lines.push(`  frame events logged until then (last ${before.length}):`);
  lines.push(...formatFrameEvents(before).split("\n").filter(Boolean).map((l) => `    ${l}`));
  return lines.join("\n");
}

function frameInfo(f: Frame): FrameInfo {
  const p = f.parentFrame();
  return { name: f.name(), url: f.url(), parent: p ? { name: p.name(), url: p.url() } : null };
}

/** The frame events kept for the failure report. */
const frameLogSize = 200;

/** Lets a test declare the error answers it provokes on purpose. */
export interface Guards {
  /** An answer with this status to a request for this path is expected. */
  allowHTTPError(path: string, status: number): void;
  /**
   * Returns the problems collected so far and forgets them. Only for a test
   * of the guards themselves, which provokes a problem on purpose and checks
   * its report; it must be the test's own page.
   */
  takeProblems(): string[];
  /** The rolling frame event log, formatted. */
  frameLog(): string;
}

export const test = base.extend<{ guards: Guards }>({
  guards: [
    async ({ context }, use, testInfo) => {
      const problems: string[] = [];
      const frameEvents: FrameEvent[] = [];
      const record = (kind: FrameEvent["kind"], f: Frame) => {
        frameEvents.push({ at: Date.now(), kind, frame: frameInfo(f) });
        if (frameEvents.length > frameLogSize) frameEvents.shift();
      };
      const logFrames = (page: Page) => {
        page.on("frameattached", (f) => record("attached", f));
        page.on("framenavigated", (f) => record("navigated", f));
        page.on("framedetached", (f) => record("detached", f));
      };
      // The page fixture may already exist; later pages and popups follow.
      context.pages().forEach(logFrames);
      context.on("page", logFrames);
      const allowed: { path: string; status: number }[] = [];
      // The browser logs every 4xx/5xx answer as a console error.
      const expectedHTTPError = (url: string, text: string) =>
        allowed.some((a) => {
          try {
            const u = new URL(url);
            return u.origin === origin && u.pathname === a.path && text.includes(`status of ${a.status} `);
          } catch {
            return false;
          }
        });
      await context.route(
        (url) => url.origin !== origin,
        (route) => {
          problems.push(`request to another host: ${route.request().method()} ${route.request().url()}`);
          return route.abort("blockedbyclient");
        },
      );
      // The browser logs CSP violations too; the listener also catches the
      // ones of report-only policies and names the directive.
      await context.addInitScript(() => {
        document.addEventListener("securitypolicyviolation", (e) => {
          console.error(`CSP violation: ${e.violatedDirective} blocked ${e.blockedURI || "inline"} in ${e.sourceFile || e.documentURI}`);
        });
      });
      context.on("console", (msg) => {
        if (msg.type() !== "error") return;
        const { url } = msg.location();
        if (fromMailFrame(url, msg.text()) || expectedHTTPError(url, msg.text())) return;
        problems.push(
          formatConsoleError(
            {
              text: msg.text(),
              location: msg.location(),
              at: msg.timestamp(),
              frames: (msg.page()?.frames() ?? []).map(frameInfo),
            },
            frameEvents,
          ),
        );
      });
      context.on("weberror", (err) => problems.push(`page error: ${err.error().stack ?? err.error().message}`));
      await use({
        allowHTTPError: (path, status) => allowed.push({ path, status }),
        takeProblems: () => problems.splice(0),
        frameLog: () => formatFrameEvents(frameEvents),
      });
      if (problems.length > 0) {
        // As files in the test's output folder (web/test-results/…), so the
        // CI artefact of a failed run holds them next to the trace; an
        // attachment with only a body would live in the reporters' output.
        for (const [name, text] of [
          ["guard-problems.txt", problems.join("\n\n")],
          ["frame-events.txt", formatFrameEvents(frameEvents)],
        ] as const) {
          const path = testInfo.outputPath(name);
          await writeFile(path, text + "\n");
          await testInfo.attach(name, { path, contentType: "text/plain" });
        }
      }
      expect(problems, "guards: errors, CSP violations or foreign requests").toEqual([]);
    },
    { auto: true },
  ],
});

/**
 * The tile of an account on the Accounts page: a region named by its
 * heading, the account's name (active and removed accounts alike).
 */
export function accountCard(page: Page, name: string): Locator {
  return page.getByRole("main").getByRole("region", { name, exact: true });
}

/** Accepts the next window.confirm or window.prompt, optionally with text. */
export function acceptDialog(page: Page, text?: string): void {
  page.once("dialog", (d) => void d.accept(text));
}
