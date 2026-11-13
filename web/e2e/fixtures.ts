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
import { origin, setupOrigin } from "./demo";

export { expect };

/** The text as a regular expression that matches it literally. */
export const escapeRegExp = (text: string) => text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

// Console errors that come from the mail's own HTML in the sandboxed message
// frame (/api/messages/<id>/html), not from the app. The browser logs each
// thing the frame's sandbox and CSP block, and blocking is their job.
const escapedOrigin = escapeRegExp(origin);
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
  // WebKit's wording (Playwright's WebKit 27.2, #398). WebKit logs it with an
  // empty location, in the mail frame and on an app page alike, so the URL
  // cannot limit it to the frame here. An app page still fails: the guards'
  // securitypolicyviolation listener runs in the app's documents (it cannot
  // run in the sandboxed frame, which has no allow-scripts) and reports the
  // same refusal as "CSP violation: img-src …" (guards.spec.ts checks this).
  {
    text: new RegExp(`^Refused to load ${remote} because it does not appear in the img-src directive of the Content Security Policy\\.$`),
    url: new RegExp(`^(${message}/html(\\?[^#]*)?)?$`),
  },
  // The mail's own <script>, which the sandbox (no allow-scripts) blocks and
  // the browser logs, e.g. in "no script runs in the mail". Playwright's DOM
  // snapshotter used to add to these in every frame, and for a frame that had
  // committed about:blank; the config records traces without DOM snapshots
  // now, so it does not (#260). Nothing here covers about:blank. WebKit's
  // wording is the same (#459).
  {
    text: new RegExp(`^Blocked script execution in '${message}/html[^']*' because the document's frame is sandboxed and the 'allow-scripts' permission is not set\\.$`),
    url: /.*/, // reported with or without the frame's URL
  },
];

export function fromMailFrame(url: string, text: string): boolean {
  return mailFrameErrors.some((e) => e.text.test(text) && e.url.test(url));
}

// WebKit's own console line for a style it refuses under the CSP, for a
// style attribute and a <style> element alike, located at the page. In
// WebKit the guards accept it by its words (#398): the guards' own
// securitypolicyviolation listener reports every such refusal in an app
// document, with its directive, except WebKit's own <select> style below.
const webkitStyleRefused = "Refused to apply a stylesheet because its hash, its nonce, or 'unsafe-inline' does not appear in the style-src directive of the Content Security Policy.";

/** Whether a console error is WebKit's own line for a refused style, which the guards accept in WebKit only. */
export function fromWebKitStyleRefusal(text: string): boolean {
  return text === webkitStyleRefused;
}

// The page error WebKit reports when a ResizeObserver callback resizes what
// it observes again within one frame (the spec's loop limit; no Error
// object). The web UI's frame fit does that (frameFit.ts); a follow-up
// defers the fit. Accepted in WebKit only and only with exactly this text.
const resizeObserverLoop = "ResizeObserver loop completed with undelivered notifications.";

/** Whether a page error is the ResizeObserver loop limit, which the guards accept in WebKit only. */
export function isResizeObserverLoop(error: { message: string; stack?: string }): boolean {
  return error.message === resizeObserverLoop && !error.stack;
}

// Playwright's WebKit makes a page error of each JavaScript console error
// (wkPage.ts, _onConsoleMessage, Playwright 1.64.1): it splits the line at
// its first colon into the error's name and message and drops the character
// after that colon, which it takes for the space of "Name: message". The
// stack is the whole line followed by the frames, or empty when WebKit gave
// the line no stack trace: a line logged from a network callback, with no
// script running, such as an access-control failure of a fetch (#552). The
// colon is then often the one in "http:", so the message alone is half the
// line, and an empty stack is no text at all.

/** Stands for the character Playwright's WebKit dropped from a page error without a stack (pageErrorText). */
export const lostCharacter = "\uFFFD";

/**
 * The text of a page error in the guards' report, its whole line first. An
 * error with a stack is its stack: its first line is the whole line in both
 * engines. Without a stack, in WebKit the line is rebuilt from the name and
 * the message, with lostCharacter for the character Playwright dropped
 * after the first colon, and a second line says so; in Chromium it is
 * "name: message". A line without a colon is the message.
 */
export function pageErrorText(error: { name: string; message: string; stack?: string }, webkit: boolean): string {
  if (error.stack) return error.stack;
  // No colon in the line, or nothing after it (the message is then the whole line).
  if (!error.name || (webkit && error.message === `${error.name}:`)) return error.message;
  if (!webkit) return `${error.name}: ${error.message}`;
  return `${error.name}:${lostCharacter}${error.message}\n(WebKit gave no stack; ${lostCharacter} is the character Playwright dropped after the first colon)`;
}

// WebKit refuses a fetch that a document starts while its frame has a
// navigation to a new document in flight (#552): CachedResource::load fails
// it before any loader exists, CachedResourceLoader takes the synchronous
// failure for an access-control one, and DocumentThreadableLoader logs this
// line as a JavaScript console error, which Playwright's WebKit reports as a
// page error with the fetch's stack. No request is sent. The guards accept
// it in WebKit only, for a same-origin /api/ URL, and only while a
// main-frame document navigation of that page is in flight and then commits
// (NavigationRefusals). Playwright splits the line at its first colon, the
// one in "http:", into the error's name and message, so the whole line is
// only the first line of the stack.

/**
 * Whether the first line of a page error's stack is WebKit's refusal of a
 * fetch to an /api/ URL of the origin, exactly. A page error without a stack
 * (none in WebKit's real case) never is.
 */
export function isNavigationFetchRefusal(stackFirstLine: string | undefined, origin: string): boolean {
  if (stackFirstLine === undefined) return false;
  return new RegExp(`^Fetch API cannot load ${escapeRegExp(origin)}/api/\\S* due to access control checks\\.$`).test(stackFirstLine);
}

/**
 * One page's main-frame document navigations, for WebKit's fetch refusal
 * (#552). A navigation is in flight from its request (a main-frame
 * navigation request for a document) to its commit, the first main-frame
 * framenavigated after the request: WebKit refuses a fetch only before the
 * commit, and sends the commit before any script of the new document runs.
 * A page error held during that window is accepted once the page's
 * domcontentloaded confirms that a new document followed (a same-document
 * navigation sends framenavigated, but no request and no
 * domcontentloaded). It is reported if the navigation request fails or the
 * test ends first. Outside the window nothing is held: an error before the
 * request, after the commit, or with only same-document navigations is
 * reported at once, by the caller.
 */
export class NavigationRefusals {
  #state: "idle" | "in flight" | "committed" = "idle";
  #request: unknown;
  #held: string[] = [];
  constructor(private readonly report: (problem: string) => void) {}

  /** A main-frame navigation request for a document; a redirect's next request too. */
  navigationRequest(request: unknown): void {
    this.#state = "in flight";
    this.#request = request;
  }

  /** A failed request: the navigation's own ends the window, and what it held is reported. */
  requestFailed(request: unknown): void {
    if (this.#state !== "in flight" || request !== this.#request) return;
    this.#reportHeld();
  }

  /** A main-frame framenavigated: the commit, which ends the window. */
  frameNavigated(): void {
    if (this.#state === "in flight") this.#state = "committed";
  }

  /** The page's domcontentloaded: after a commit, the new document; what was held is accepted. */
  domContentLoaded(): void {
    if (this.#state !== "committed") return;
    this.#held = [];
    this.#state = "idle";
    this.#request = undefined;
  }

  /** Holds a refusal's problem while a navigation is in flight; false otherwise, and the caller reports it. */
  hold(problem: string): boolean {
    if (this.#state !== "in flight") return false;
    this.#held.push(problem);
    return true;
  }

  /** The end of the test: what is still held is reported. */
  end(): void {
    this.#reportHeld();
  }

  #reportHeld(): void {
    for (const p of this.#held.splice(0)) this.report(p);
    this.#state = "idle";
    this.#request = undefined;
  }
}

/** The origin of the page's main frame (its committed document); "null" when it has none. */
function pageOrigin(page: Page): string {
  try {
    return new URL(page.mainFrame().url()).origin;
  } catch {
    return "null";
  }
}

// The text of WebKit's refusal in an exception the page's own code throws
// and nobody catches (a thrown string or a nameless Error is logged verbatim,
// WebKit's retrieveErrorMessage). WebKit's page error for it can carry the
// refusal's exact line, which the guards cannot tell from WebKit's own; the
// guards' init script reports every such exception as a console error.
export const forgedRefusal = "uncaught exception with the text of WebKit's fetch refusal";

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
   * The test also uses the demo's second instance, the new one at
   * setupOrigin (#396): requests to it are not foreign. No other origin can
   * be allowed.
   */
  allowSetupInstance(): void;
  /**
   * A console error whose text matches is expected exactly count times. The
   * matches up to count are not problems; any further match is one, and at
   * the end of the test fewer matches than count are one too.
   * The way for a normal spec to declare a console error it provokes.
   */
  expectConsoleError(text: RegExp, count?: number): void;
  /**
   * Returns the problems collected so far and forgets them. Only for the
   * guards' own tests in guards.spec.ts, which provoke a problem on purpose
   * and check its report; it must be the test's own page. Any other file
   * declares its errors with expectConsoleError (a test in guards.spec.ts
   * fails on any other use).
   */
  takeProblems(): string[];
  /** The rolling frame event log, formatted. */
  frameLog(): string;
}

export const test = base.extend<{ guards: Guards }>({
  guards: [
    async ({ context, browserName }, use, testInfo) => {
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
      // Each page's main-frame document navigations, for WebKit's fetch
      // refusal (#552); a popup's own. Held problems go to the report.
      const navigations = new Map<Page, NavigationRefusals>();
      const watchNavigations = (page: Page) => {
        const n = new NavigationRefusals((p) => problems.push(p));
        navigations.set(page, n);
        page.on("request", (r) => {
          if (r.isNavigationRequest() && r.resourceType() === "document" && r.frame() === page.mainFrame()) n.navigationRequest(r);
        });
        page.on("requestfailed", (r) => n.requestFailed(r));
        page.on("framenavigated", (f) => {
          if (f === page.mainFrame()) n.frameNavigated();
        });
        page.on("domcontentloaded", () => n.domContentLoaded());
      };
      // The page fixture may already exist; later pages and popups follow.
      context.pages().forEach(logFrames);
      context.on("page", logFrames);
      context.pages().forEach(watchNavigations);
      context.on("page", watchNavigations);
      const allowed: { path: string; status: number }[] = [];
      // The demo's origins this test uses: the main one, and the new
      // instance after allowSetupInstance.
      const origins = new Set([origin]);
      const expected: { text: RegExp; count: number; seen: number }[] = [];
      // Up to count matches of an expectation are expected; a match beyond
      // that is counted and reported, so a flood is never silenced.
      const expectedConsoleError = (text: string) => {
        const matching = expected.filter((e) => e.text.test(text));
        const e = matching.find((e) => e.seen < e.count) ?? matching[0];
        if (!e) return false;
        e.seen++;
        return e.seen <= e.count;
      };
      // The browser logs every 4xx/5xx answer as a console error.
      const expectedHTTPError = (url: string, text: string) =>
        allowed.some((a) => {
          try {
            const u = new URL(url);
            return origins.has(u.origin) && u.pathname === a.path && text.includes(`status of ${a.status} `);
          } catch {
            return false;
          }
        });
      await context.route(
        (url) => !origins.has(url.origin),
        (route) => {
          problems.push(`request to another host: ${route.request().method()} ${route.request().url()}`);
          return route.abort("blockedbyclient");
        },
      );
      // The browser logs CSP violations too; the listener also catches the
      // ones of report-only policies and names the directive.
      //
      // WebKit only (#398): WebKit gives every <select>, from the parser or
      // from script, a style attribute in its own user-agent shadow tree
      // (text-overflow:inherit) and checks it against the page's CSP, so each
      // select reports one style-src-attr violation of an inline style, with
      // no sample. Its target is the select, or the document when the select
      // was created by script and not yet inserted. Chromium reports none.
      // The listener skips exactly that: a style-src-attr violation of an
      // inline style without a sample whose target is a select without a
      // style attribute of its own, or the document while a select that the
      // page's script created has not reported yet; at most one per select.
      // Every other violation, a style attribute on a select included, is
      // reported. guards.spec.ts checks both.
      await context.addInitScript(({ webkit, forged }: { webkit: boolean; forged: string }) => {
        const unreported: HTMLSelectElement[] = [];
        const accounted = new WeakSet<HTMLSelectElement>();
        if (webkit) {
          const createElement = Document.prototype.createElement;
          Document.prototype.createElement = function (this: Document, ...args: Parameters<Document["createElement"]>) {
            const el = createElement.apply(this, args);
            if (el instanceof HTMLSelectElement) unreported.push(el);
            return el;
          } as Document["createElement"];
        }
        const selectsOwnStyle = (e: SecurityPolicyViolationEvent) => {
          if (!webkit || e.violatedDirective !== "style-src-attr" || e.blockedURI !== "inline" || e.sample !== "") return false;
          const t = e.target;
          if (t instanceof HTMLSelectElement) {
            if (t.hasAttribute("style") || accounted.has(t)) return false;
            accounted.add(t);
            const i = unreported.indexOf(t);
            if (i >= 0) unreported.splice(i, 1);
            return true;
          }
          if (t === document && unreported.length > 0) {
            accounted.add(unreported.shift()!);
            return true;
          }
          return false;
        };
        document.addEventListener("securitypolicyviolation", (e) => {
          if (selectsOwnStyle(e)) return;
          console.error(`CSP violation: ${e.violatedDirective} blocked ${e.blockedURI || "inline"} in ${e.sourceFile || e.documentURI}`);
        });
        // An uncaught exception of the page's own code that carries the text
        // of WebKit's fetch refusal (#552). WebKit's own refusal is a console
        // line, not an exception, and dispatches no error event.
        window.addEventListener("error", (e) => {
          if (e instanceof ErrorEvent && String(e.message).includes("Fetch API cannot load ")) console.error(`${forged}: ${e.message}`);
        });
      }, { webkit: browserName === "webkit", forged: forgedRefusal });
      context.on("console", (msg) => {
        if (msg.type() !== "error") return;
        const { url } = msg.location();
        if (fromMailFrame(url, msg.text()) || expectedHTTPError(url, msg.text()) || expectedConsoleError(msg.text())) return;
        if (browserName === "webkit" && fromWebKitStyleRefusal(msg.text())) return;
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
      context.on("weberror", (err) => {
        const error = err.error();
        if (browserName === "webkit" && isResizeObserverLoop(error)) return;
        const problem = `page error: ${pageErrorText(error, browserName === "webkit")}`;
        // WebKit's refusal of a fetch started during a navigation of the
        // page's main frame: held until that navigation commits (#552).
        const page = err.page();
        if (browserName === "webkit" && page && isNavigationFetchRefusal(error.stack?.split("\n")[0], pageOrigin(page)) && navigations.get(page)?.hold(problem)) return;
        problems.push(problem);
      });
      await use({
        allowHTTPError: (path, status) => allowed.push({ path, status }),
        allowSetupInstance: () => void origins.add(setupOrigin),
        expectConsoleError: (text, count = 1) => void expected.push({ text, count, seen: 0 }),
        takeProblems: () => problems.splice(0),
        frameLog: () => formatFrameEvents(frameEvents),
      });
      // A refusal still held: its navigation never committed.
      for (const n of navigations.values()) n.end();
      // Fewer than expected: a declared error that went missing. More were
      // reported one by one as they came.
      for (const e of expected) if (e.seen < e.count) problems.push(`expected console error ${e.text} ${e.count}×, saw ${e.seen}`);
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
 * The row of an account in a table on the Accounts page, active and removed
 * accounts alike: the row whose header links to the account by its name.
 */
export function accountRow(page: Page, name: string): Locator {
  return page
    .getByRole("main")
    .getByRole("row")
    .filter({ has: page.getByRole("rowheader").getByRole("link", { name, exact: true }) });
}

/** Accepts the next window.confirm or window.prompt, optionally with text. */
export function acceptDialog(page: Page, text?: string): void {
  page.once("dialog", (d) => void d.accept(text));
}

/**
 * Waits until the element's own animations and transitions have finished
 * (#287): a signal from the browser, not a delay. Only the element's own,
 * not its subtree's, and none that repeat for ever (Carbon's skeleton
 * pulse), so it always resolves; with none running it resolves at once,
 * and a cancelled one counts as finished.
 */
export async function animationsDone(target: Locator): Promise<void> {
  await target.evaluate((el) =>
    Promise.all(
      el
        .getAnimations()
        .filter((a) => a.effect?.getComputedTiming().iterations !== Infinity)
        // A cancelled animation (the element removed or hidden on the way) rejects with AbortError: done as well.
        .map((a) => a.finished.catch(() => undefined)),
    ).then(() => undefined),
  );
}

/**
 * Waits until every finite animation and transition of the page's document
 * has finished: a signal from the browser, not a delay. getAnimations()
 * flushes pending style changes first, so a transition that a class change
 * just started (a theme switch) is included. Repeating ones (Carbon's
 * skeleton pulse) are left out, and a cancelled one counts as finished.
 */
export async function documentAnimationsDone(page: Page): Promise<void> {
  await page.evaluate(() =>
    Promise.all(
      document
        .getAnimations()
        .filter((a) => a.effect?.getComputedTiming().iterations !== Infinity)
        .map((a) => a.finished.catch(() => undefined)),
    ).then(() => undefined),
  );
}
