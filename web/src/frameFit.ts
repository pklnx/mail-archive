// The HTML mail frame as tall as its content (#203), so the message scrolls
// as one page. The frame is sandboxed with the same origin and without
// scripts: nothing runs in it, so all of this is on the parent's side. The
// parent reads only the frame document's geometry and adds event listeners
// and observers to it; it never reads the mail's content or writes into it.
//
// Measuring: the frame is set to height 0 and the document's scroll height
// read. At any other height, content sized relative to the frame (100vh,
// height:100%) would grow with each fit. A mail with a doctype renders in
// standards mode (the server puts its <base> after the doctype), one without
// in quirks mode. document.scrollingElement is <html> in the one and the body
// in the other, so the measuring goes by it. While measuring, the box around
// the frame keeps its height, so the scroller around it does not lose its
// scroll position.

/** Above this many CSS pixels the frame keeps the visible-area height and scrolls on its own. */
export const FRAME_CAP = 50_000;
/** Fits in a row that change the height on content resizes alone before the frame stops observing them. */
export const LOOP_LIMIT = 10;

const xlink = "http://www.w3.org/1999/xlink";
const svgNS = "http://www.w3.org/2000/svg";

/** The frame's document while it shows a message of this app, else null (not loaded, or navigated elsewhere). */
export function frameDocument(frame: HTMLIFrameElement, origin = location.origin): Document | null {
  try {
    const doc = frame.contentDocument;
    if (!doc || !doc.URL.startsWith(`${origin}/api/messages/`)) return null;
    return doc;
  } catch {
    return null;
  }
}

/** The document's content height at the frame's current height. */
function contentHeight(doc: Document): number {
  const se = doc.scrollingElement ?? doc.documentElement;
  return Math.max(se.scrollHeight, doc.body?.scrollHeight ?? 0);
}

/** The frame's border, top and bottom: the frame is sized by its border box. */
function borders(frame: HTMLElement): number {
  const s = frame.ownerDocument.defaultView!.getComputedStyle(frame);
  const b = (parseFloat(s.borderTopWidth) || 0) + (parseFloat(s.borderBottomWidth) || 0);
  return s.boxSizing === "border-box" ? b : 0;
}

/**
 * Sizes the frame to its document once. Returns the height it set (the
 * content's, plus a horizontal scrollbar's if there is one),
 * or null when the frame has the visible-area height of the stylesheet (the
 * fallback): no readable document, content over FRAME_CAP, or content that
 * is taller than any height the frame is given (viewport-relative content);
 * the latter gets the larger of its height and the visible area, and keeps
 * its own scrolling for the rest.
 */
export function fit(frame: HTMLIFrameElement, box: HTMLElement, origin = location.origin): number | null {
  const doc = frameDocument(frame, origin);
  const style = frame.style;
  if (!doc) {
    style.removeProperty("block-size");
    return null;
  }
  // Hold the box's height: with the frame at 0 the scroller around it would
  // shrink, and the browser would clamp its scroll position.
  box.style.setProperty("min-block-size", `${box.getBoundingClientRect().height}px`);
  try {
    style.setProperty("block-size", "0px");
    const h = contentHeight(doc);
    if (h > FRAME_CAP) {
      style.removeProperty("block-size");
      return null;
    }
    const extra = borders(frame);
    style.setProperty("block-size", `${h + extra}px`);
    // A mail wider than the frame gets a horizontal scrollbar, which with
    // classic scrollbars takes its height from the viewport and leaves that
    // much to scroll vertically: add it once.
    const se = doc.scrollingElement ?? doc.documentElement;
    const bar = Math.max(0, (doc.defaultView?.innerHeight ?? 0) - se.clientHeight);
    if (bar > 0) style.setProperty("block-size", `${h + bar + extra}px`);
    if (contentHeight(doc) > h + 1) {
      // Still overflowing: content relative to the frame's height. The
      // visible area (the stylesheet's height), if that is more.
      style.removeProperty("block-size");
      const visible = frame.getBoundingClientRect().height;
      if (visible < h + bar + extra) style.setProperty("block-size", `${h + bar + extra}px`);
      return null;
    }
    return h + bar;
  } finally {
    box.style.removeProperty("min-block-size");
  }
}

/** Whether a node is a link: an a or area (HTML or SVG) with href, or SVG's xlink:href. */
function isLink(n: unknown): boolean {
  const e = n as Element | null;
  return (
    !!e &&
    e.nodeType === 1 &&
    (e.localName === "a" || e.localName === "area") &&
    (e.hasAttribute("href") || e.hasAttributeNS(xlink, "href"))
  );
}

/**
 * The link an event on it follows: the innermost link in the event's
 * composed path, so links inside open shadow roots count too. The server
 * neutralizes declarative shadow roots in served mail (#209), so a closed
 * root, whose inside is not in the path, does not occur there; the composed
 * path still covers open ones. Without a path, the nearest link
 * among the target's ancestors. Innermost, so that an outer link cannot
 * stand in for an inner one the browser would follow.
 */
function linkOf(e: Event): Element | null {
  return (pathOf(e).find(isLink) as Element | undefined) ?? null;
}

/** The event's composed path, innermost first; without one, the target and its ancestor elements. */
function pathOf(e: Event): EventTarget[] {
  const path = typeof e.composedPath === "function" ? e.composedPath() : [];
  if (path.length > 0) return path;
  const found: EventTarget[] = [];
  let el = e.target as Node | null;
  if (el && el.nodeType !== 1) el = el.parentNode;
  for (let n = el as Element | null; n && n.nodeType === 1; n = n.parentElement) found.push(n);
  return found;
}

/** Whether a node is an SVG use element. */
function isUse(n: unknown): boolean {
  const e = n as Element | null;
  return !!e && e.nodeType === 1 && e.namespaceURI === svgNS && e.localName === "use";
}

/** The URL a link points to, resolved like the browser does (against the frame document's base URL), or null if invalid. */
export function linkURL(link: Element): URL | null {
  // The attribute, not the href property: in SVG that is an SVGAnimatedString.
  const raw = link.getAttribute("href") ?? link.getAttributeNS(xlink, "href") ?? "";
  try {
    return new URL(raw, link.ownerDocument.baseURI);
  } catch {
    return null;
  }
}

/**
 * A click, auxclick or dragstart listener for the frame document (dragging
 * a link to the tab bar opens it): it cancels links into
 * the app itself. The frame shares the app's origin, so such a link would
 * reach the API with the session cookie (the server's Fetch Metadata guard
 * refuses most of them; this is the second layer). A mail has no reason to
 * link into the archive. javascript:, data: and vbscript: links are
 * cancelled too: the sandbox runs no script, but a new tab would have the
 * app's origin (or, for data:, content the mail chose). Other links are
 * untouched; a link that does not parse is cancelled.
 *
 * Any event whose composed path holds an SVG <use> element is cancelled,
 * whatever it points to: use clones what it references, links included,
 * into a user-agent shadow tree that the path does not show, so the clone's
 * link cannot be told apart (#209). A click on use has no other default
 * action. So links through <use> do not work at all, external ones
 * included; a mail has no need for them.
 */
export function blockAppLinks(e: Event, origin = location.origin): void {
  if (pathOf(e).some(isUse)) {
    e.preventDefault();
    return;
  }
  const link = linkOf(e);
  if (!link) return;
  const url = linkURL(link);
  if (!url || url.origin === origin || unsafeSchemes.has(url.protocol)) e.preventDefault();
}

/** Schemes a link in a mail is never followed to (URL lowercases protocol). */
const unsafeSchemes = new Set(["javascript:", "data:", "vbscript:"]);

export interface FrameFit {
  /** Removes every listener and observer. */
  detach(): void;
  /** The frame's height as last set (CSS value), "" for the stylesheet's. */
  height(): string;
}

/**
 * Keeps the frame as tall as its document. Fits on the frame's load, on
 * content resizes (images, fonts, details opening), on a width change of the
 * frame (window resize, rotation, the dark sheet's padding), on late image
 * loads and on details toggles (closing one does not resize the stretched
 * root in quirks mode). After LOOP_LIMIT height-changing fits in a row on
 * content resizes alone (a CSS animation, say), it stops observing them and
 * keeps its height. It also installs the same-origin link blocker (click,
 * auxclick, dragstart).
 *
 * The blocker comes first (#209): the frame is inert unless its current
 * document is a mail document with the blocker on it, so it takes no click
 * and no focus before. A requestAnimationFrame poll installs it as soon as
 * the mail's document exists (it commits long before its load, which a
 * remote image can hold up), so the mail can be used while it loads; on load
 * it is installed again, with the body. Only install() lifts inert; detach()
 * leaves it, since the frame is then about to go.
 *
 * The same holds for every later document in the frame (#265). When the
 * frame leaves the guarded mail document (its pagehide: a link that opens in
 * the frame itself, Back or Forward), the frame turns inert, the listeners
 * come off and the poll runs again. The parent gets no early signal for a
 * document that comes back (Back to the mail), so this is a gate, not a
 * detector: the next mail document commits into a frame that is already
 * inert, and the poll installs the blocker on it. Until then the frame stays
 * inert, also on a document the parent cannot read (the browser's error page
 * after a link the CSP's frame-src refuses). A pagehide with persisted set
 * (the whole app goes into the back/forward cache) changes nothing: the same
 * document comes back with its listeners.
 *
 * start: the height a replaced frame had (images toggle), so the page does
 * not jump until the new one is measured.
 */
export function attachFrameFit(frame: HTMLIFrameElement, box: HTMLElement, start = "", origin = location.origin): FrameFit {
  // The attribute, not the inert property: the attribute is what the browser
  // acts on, whatever the DOM implementation's IDL support.
  frame.setAttribute("inert", "");
  if (start) frame.style.setProperty("block-size", start);
  let detachDoc = () => {};
  let width = -1;
  let changes = 0;
  let last: number | null = null;
  // Until the first load the frame shows about:blank: keep the start height.
  let loaded = false;
  // The pending poll frame, 0 when none.
  let poll = 0;
  // The guarded document the frame left (its pagehide), until the next
  // install: while it is still the frame's document, it is not installed again.
  let left: Document | null = null;
  // Set by detach(): a later pagehide (React removing the frame) or install changes nothing.
  let detached = false;
  const stopPoll = () => {
    if (poll) cancelAnimationFrame(poll);
    poll = 0;
  };
  const startPoll = () => {
    if (!detached && !poll) poll = requestAnimationFrame(check);
  };

  const run = (fromContent: boolean) => {
    if (!loaded) return false;
    const h = fit(frame, box, origin);
    if (!fromContent) {
      changes = 0;
    } else if (h !== last) {
      changes++;
    }
    last = h;
    return changes >= LOOP_LIMIT;
  };

  const install = () => {
    stopPoll();
    detachDoc();
    detachDoc = () => {};
    const doc = frameDocument(frame, origin);
    if (!doc || doc === left) {
      // No guarded mail document (the error page's load, say): the frame
      // stays inert and the poll waits for the next mail document.
      frame.setAttribute("inert", "");
      startPoll();
      return;
    }
    left = null;
    // ResizeObserver of the parent: the frame runs no script.
    const content = new ResizeObserver(() => {
      if (run(true)) content.disconnect();
    });
    // Installed early, the document may have no root or body yet: load
    // installs again.
    if (doc.documentElement) content.observe(doc.documentElement);
    if (doc.body) content.observe(doc.body);
    const refit = () => void run(false);
    const block = (e: Event) => blockAppLinks(e, origin);
    // Capture: load, error and toggle do not bubble.
    for (const type of ["load", "error", "toggle"]) doc.addEventListener(type, refit, true);
    for (const type of ["click", "auxclick", "dragstart"]) doc.addEventListener(type, block, true);
    const fonts = doc.fonts;
    fonts?.addEventListener("loadingdone", refit);
    // The frame leaves this document: inert until the next one is installed.
    const win = doc.defaultView;
    const hide = (e: Event) => {
      if (detached || (e as PageTransitionEvent).persisted) return;
      frame.setAttribute("inert", "");
      left = doc;
      detachDoc();
      detachDoc = () => {};
      startPoll();
    };
    win?.addEventListener("pagehide", hide);
    detachDoc = () => {
      content.disconnect();
      for (const type of ["load", "error", "toggle"]) doc.removeEventListener(type, refit, true);
      for (const type of ["click", "auxclick", "dragstart"]) doc.removeEventListener(type, block, true);
      fonts?.removeEventListener("loadingdone", refit);
      win?.removeEventListener("pagehide", hide);
    };
    // Only now, with the blocker on the mail's document.
    frame.removeAttribute("inert");
  };

  // Until a mail document other than the left one exists: the frame's
  // initial about:blank first, after a pagehide the left document, an
  // unreadable one or another URL.
  function check() {
    poll = 0;
    const doc = frameDocument(frame, origin);
    if (doc && doc !== left) install();
    else startPoll();
  }

  const onLoad = () => {
    loaded = true;
    install();
    run(false);
  };
  frame.addEventListener("load", onLoad);
  // Width changes only: the frame's own height changes with every fit.
  const outer = new ResizeObserver(() => {
    const w = frame.getBoundingClientRect().width;
    if (w === width) return;
    width = w;
    run(false);
  });
  outer.observe(frame);
  // Loaded before this ran (a cached document); else installed as soon as
  // the document is there.
  if (frameDocument(frame, origin)?.readyState === "complete") onLoad();
  else check();

  return {
    detach() {
      detached = true;
      stopPoll();
      frame.removeEventListener("load", onLoad);
      outer.disconnect();
      detachDoc();
    },
    height: () => frame.style.getPropertyValue("block-size"),
  };
}
