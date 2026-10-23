// The HTML mail frame as tall as its content (#203), so the message scrolls
// as one page. The frame is sandboxed with the same origin and without
// scripts: nothing runs in it, so all of this is on the parent's side. The
// parent reads only the frame document's geometry and adds event listeners
// and observers to it; it never reads the mail's content or writes into it.
//
// Measuring: the frame is set to height 0 and the document's scroll height
// read. At any other height, content sized relative to the frame (100vh,
// height:100%) would grow with each fit. Every mail renders in quirks mode
// (the server's <base> precedes any doctype), where document.scrollingElement
// is the body, not <html>. While measuring, the box around the frame keeps
// its height, so the scroller around it does not lose its scroll position.

/** Above this many CSS pixels the frame keeps the visible-area height and scrolls on its own. */
export const FRAME_CAP = 50_000;
/** Fits in a row that change the height on content resizes alone before the frame stops observing them. */
export const LOOP_LIMIT = 10;

const xlink = "http://www.w3.org/1999/xlink";

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
 * Sizes the frame to its document once. Returns the content height it set,
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
    if (contentHeight(doc) > h + 1) {
      // Still overflowing: content relative to the frame's height. The
      // visible area (the stylesheet's height), if that is more.
      style.removeProperty("block-size");
      const visible = frame.getBoundingClientRect().height;
      if (visible < h + extra) style.setProperty("block-size", `${h + extra}px`);
      return null;
    }
    return h;
  } finally {
    box.style.removeProperty("min-block-size");
  }
}

/**
 * The link a click on target follows: the nearest a or area with an href,
 * HTML or SVG (SVG also with xlink:href). The same element closest("a[href],
 * area[href]") finds, plus SVG's xlink:href, so that an outer link cannot
 * stand in for an inner one the browser would follow.
 */
function linkOf(target: EventTarget | null): Element | null {
  let el = target as Node | null;
  if (el && el.nodeType !== 1) el = el.parentNode;
  for (let e = el as Element | null; e && e.nodeType === 1; e = e.parentElement) {
    if ((e.localName === "a" || e.localName === "area") && (e.hasAttribute("href") || e.hasAttributeNS(xlink, "href"))) return e;
  }
  return null;
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
 * A click or auxclick listener for the frame document: it cancels links into
 * the app itself. The frame shares the app's origin, so such a link would
 * reach the API with the session cookie (the server's Fetch Metadata guard
 * refuses most of them; this is the second layer). A mail has no reason to
 * link into the archive. javascript: links are cancelled too: the sandbox
 * runs none, but a new tab would have the app's origin. Other links are
 * untouched; a link that does not parse is cancelled.
 */
export function blockAppLinks(e: Event, origin = location.origin): void {
  const link = linkOf(e.target);
  if (!link) return;
  const url = linkURL(link);
  if (!url || url.origin === origin || url.protocol === "javascript:") e.preventDefault();
}

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
 * quirks-mode root). After LOOP_LIMIT height-changing fits in a row on
 * content resizes alone (a CSS animation, say), it stops observing them and
 * keeps its height. It also installs the same-origin link blocker.
 *
 * start: the height a replaced frame had (images toggle), so the page does
 * not jump until the new one is measured.
 */
export function attachFrameFit(frame: HTMLIFrameElement, box: HTMLElement, start = "", origin = location.origin): FrameFit {
  if (start) frame.style.setProperty("block-size", start);
  let detachDoc = () => {};
  let width = -1;
  let changes = 0;
  let last: number | null = null;
  // Until the first load the frame shows about:blank: keep the start height.
  let loaded = false;

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
    detachDoc();
    detachDoc = () => {};
    const doc = frameDocument(frame, origin);
    if (!doc) return;
    // ResizeObserver of the parent: the frame runs no script.
    const content = new ResizeObserver(() => {
      if (run(true)) content.disconnect();
    });
    content.observe(doc.documentElement);
    if (doc.body) content.observe(doc.body);
    const refit = () => void run(false);
    const block = (e: Event) => blockAppLinks(e, origin);
    // Capture: load, error and toggle do not bubble.
    for (const type of ["load", "error", "toggle"]) doc.addEventListener(type, refit, true);
    doc.addEventListener("click", block, true);
    doc.addEventListener("auxclick", block, true);
    const fonts = doc.fonts;
    fonts?.addEventListener("loadingdone", refit);
    detachDoc = () => {
      content.disconnect();
      for (const type of ["load", "error", "toggle"]) doc.removeEventListener(type, refit, true);
      doc.removeEventListener("click", block, true);
      doc.removeEventListener("auxclick", block, true);
      fonts?.removeEventListener("loadingdone", refit);
    };
  };

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
  // Loaded before this ran (a cached document).
  if (frameDocument(frame, origin)?.readyState === "complete") onLoad();

  return {
    detach() {
      frame.removeEventListener("load", onLoad);
      outer.disconnect();
      detachDoc();
    },
    height: () => frame.style.getPropertyValue("block-size"),
  };
}
