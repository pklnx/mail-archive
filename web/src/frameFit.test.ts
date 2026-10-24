// The auto-height mail frame and the same-origin link blocker (#203). jsdom
// has no layout: the frame document is a detached document whose sizes are
// set by a small model of the mail, as a function of the frame's height.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FRAME_CAP, LOOP_LIMIT, attachFrameFit, blockAppLinks, fit, frameDocument } from "./frameFit";

const origin = "https://archive.example";
const frameURL = `${origin}/api/messages/${"a".repeat(64)}/html`;
const svgNS = "http://www.w3.org/2000/svg";
const xlinkNS = "http://www.w3.org/1999/xlink";

/** The frame's height as set inline, in px; the stylesheet's (the visible area) when unset. */
function frameHeight(frame: HTMLIFrameElement, visible: number): number {
  const v = frame.style.getPropertyValue("block-size");
  return v ? parseFloat(v) : visible;
}

interface Mail {
  /** The content height at a frame height (viewport-relative content depends on it). */
  content: (frameH: number) => number;
  quirks?: boolean;
  url?: string;
}

/** A frame (and the box around it) showing a mail; reads record the frame height they happen at. */
function setup(mail: Mail, visible = 700) {
  const box = document.createElement("div");
  const frame = document.createElement("iframe");
  box.append(frame);
  document.body.append(box);
  const doc = document.implementation.createHTMLDocument("");
  Object.defineProperty(doc, "URL", { value: mail.url ?? frameURL });
  Object.defineProperty(doc, "baseURI", { value: mail.url ?? frameURL });
  Object.defineProperty(doc, "readyState", { value: "loading", configurable: true });
  const reads: { at: number; minBox: string }[] = [];
  // The document's viewport: the frame's height without its border when that is inside it (border-box).
  const h = () => frameHeight(frame, visible) - (frame.style.boxSizing === "border-box" ? 2 * (parseFloat(frame.style.borderTopWidth) || 0) : 0);
  const record = () => reads.push({ at: h(), minBox: box.style.getPropertyValue("min-block-size") });
  // Quirks mode: the body scrolls and is stretched to the frame; <html> is the frame's height.
  // Standards: <html> scrolls; the body is its content without the body margin.
  Object.defineProperty(doc, "scrollingElement", { get: () => (mail.quirks === false ? doc.documentElement : doc.body) });
  Object.defineProperty(doc.documentElement, "scrollHeight", {
    get: () => (record(), mail.quirks === false ? Math.max(mail.content(h()), h()) : h()),
    configurable: true,
  });
  Object.defineProperty(doc.body, "scrollHeight", {
    get: () => (record(), mail.quirks === false ? mail.content(h()) - 16 : Math.max(mail.content(h()), h())),
    configurable: true,
  });
  Object.defineProperty(frame, "contentDocument", { get: () => doc, configurable: true });
  frame.getBoundingClientRect = () => ({ height: h(), width: 600 }) as DOMRect;
  box.getBoundingClientRect = () => ({ height: h() + 40, width: 600 }) as DOMRect;
  return { box, frame, doc, reads };
}

afterEach(() => {
  document.body.replaceChildren();
  vi.unstubAllGlobals();
});

describe("fit", () => {
  it("measures at height 0 and sets the larger of the scrolling element's and the body's height (quirks mode)", () => {
    const { box, frame, reads } = setup({ content: () => 1200 });
    expect(fit(frame, box, origin)).toBe(1200);
    expect(frame.style.getPropertyValue("block-size")).toBe("1200px");
    // The first reads happen at 0, not at the visible area or a previous height.
    expect(reads[0]!.at).toBe(0);
    expect(reads[1]!.at).toBe(0);
  });

  it("measures in standards mode by <html>, which includes the body's margin", () => {
    const { box, frame } = setup({ content: () => 900, quirks: false });
    expect(fit(frame, box, origin)).toBe(900);
    expect(frame.style.getPropertyValue("block-size")).toBe("900px");
  });

  it("does not grow on content sized relative to the frame (height:100% chains)", () => {
    // 100% of the frame plus nothing else: at 0 it is 0, the body's own content wins.
    const { box, frame } = setup({ content: (fh) => Math.max(fh, 400) });
    expect(fit(frame, box, origin)).toBe(400);
    expect(fit(frame, box, origin)).toBe(400);
    expect(fit(frame, box, origin)).toBe(400);
  });

  it("holds the box's height while measuring and clears it afterwards", () => {
    const { box, frame, reads } = setup({ content: () => 1200 });
    frame.style.setProperty("block-size", "3000px");
    fit(frame, box, origin);
    expect(reads[0]!.minBox).toBe("3040px");
    expect(box.style.getPropertyValue("min-block-size")).toBe("");
  });

  it("adds the frame's border with border-box sizing", () => {
    const { box, frame } = setup({ content: () => 1200 });
    frame.style.boxSizing = "border-box";
    frame.style.border = "1px solid";
    expect(fit(frame, box, origin)).toBe(1200);
    expect(frame.style.getPropertyValue("block-size")).toBe("1202px");
  });

  it("adds a horizontal scrollbar's height once, so nothing is left to scroll vertically", () => {
    // A mail wider than the frame: a classic 15px scrollbar takes its height from the viewport.
    const { box, frame, doc } = setup({ content: () => 1200 });
    const viewport = () => parseFloat(frame.style.getPropertyValue("block-size") || "0");
    Object.defineProperty(doc, "defaultView", { value: { get innerHeight() { return viewport(); } } });
    Object.defineProperty(doc.body, "clientHeight", { get: () => viewport() - 15 });
    // The scroll height is the content or the client area (without the scrollbar), whichever is more.
    Object.defineProperty(doc.body, "scrollHeight", { get: () => Math.max(1200, viewport() - 15) });
    Object.defineProperty(doc.documentElement, "scrollHeight", { get: () => viewport() });
    expect(fit(frame, box, origin)).toBe(1215);
    expect(frame.style.getPropertyValue("block-size")).toBe("1215px");
    // Stable: the next fit measures the same.
    expect(fit(frame, box, origin)).toBe(1215);

    // Overlay scrollbars (or none): nothing added.
    const plain = setup({ content: () => 1200 });
    Object.defineProperty(plain.doc, "defaultView", { value: { get innerHeight() { return parseFloat(plain.frame.style.getPropertyValue("block-size")); } } });
    Object.defineProperty(plain.doc.body, "clientHeight", { get: () => parseFloat(plain.frame.style.getPropertyValue("block-size")) });
    expect(fit(plain.frame, plain.box, origin)).toBe(1200);
  });

  it("keeps the visible area over the cap, where the frame scrolls on its own", () => {
    const { box, frame } = setup({ content: () => FRAME_CAP + 1 });
    frame.style.setProperty("block-size", "1000px");
    expect(fit(frame, box, origin)).toBeNull();
    expect(frame.style.getPropertyValue("block-size")).toBe("");
    expect(box.style.getPropertyValue("min-block-size")).toBe("");

    const at = setup({ content: () => FRAME_CAP });
    expect(fit(at.frame, at.box, origin)).toBe(FRAME_CAP);
  });

  it("gives content taller than any frame height (a 100vh hero) the larger of its height and the visible area", () => {
    // A 100vh hero above 500px of text: at height h it is h + 500.
    const hero = setup({ content: (fh) => fh + 500 }, 700);
    expect(fit(hero.frame, hero.box, origin)).toBeNull();
    expect(hero.frame.style.getPropertyValue("block-size")).toBe(""); // the visible area, 700

    const small = setup({ content: (fh) => fh + 500 }, 300);
    expect(fit(small.frame, small.box, origin)).toBeNull();
    expect(small.frame.style.getPropertyValue("block-size")).toBe("500px");
  });

  it("falls back to the visible area without a document, for another URL, or when the document is not readable", () => {
    const { box, frame } = setup({ content: () => 1200 });
    frame.style.setProperty("block-size", "1200px");
    Object.defineProperty(frame, "contentDocument", { get: () => null, configurable: true });
    expect(fit(frame, box, origin)).toBeNull();
    expect(frame.style.getPropertyValue("block-size")).toBe("");

    Object.defineProperty(frame, "contentDocument", {
      get: () => {
        throw new DOMException("Blocked", "SecurityError");
      },
      configurable: true,
    });
    expect(frameDocument(frame, origin)).toBeNull();

    for (const url of ["about:blank", "https://elsewhere.example/api/messages/x", `${origin}/api/status`, `${origin}.evil.example/api/messages/x`]) {
      const other = setup({ content: () => 1200, url });
      other.frame.style.setProperty("block-size", "900px");
      expect(fit(other.frame, other.box, origin), url).toBeNull();
      expect(other.frame.style.getPropertyValue("block-size"), url).toBe("");
    }
  });
});

/** A ResizeObserver that tests drive by hand. */
class FakeObserver {
  static all: FakeObserver[] = [];
  targets: Element[] = [];
  disconnected = false;
  constructor(public callback: () => void) {
    FakeObserver.all.push(this);
  }
  observe(el: Element) {
    this.targets.push(el);
  }
  unobserve() {}
  disconnect() {
    this.disconnected = true;
  }
  fire() {
    if (!this.disconnected) this.callback();
  }
}

describe("attachFrameFit", () => {
  beforeEach(() => {
    FakeObserver.all = [];
    vi.stubGlobal("ResizeObserver", FakeObserver);
  });
  // The latest: the blocker installs once the document exists and again on load.
  const observerOf = (el: Element) => [...FakeObserver.all].reverse().find((o) => o.targets.includes(el))!;

  it("keeps the start height until the frame loads, then fits", () => {
    let content = 1200;
    const { box, frame } = setup({ content: () => content });
    const f = attachFrameFit(frame, box, "800px", origin);
    // The observer's first notification, before the load: about:blank in a real browser.
    observerOf(frame).fire();
    expect(frame.style.getPropertyValue("block-size")).toBe("800px");
    frame.dispatchEvent(new Event("load"));
    expect(f.height()).toBe("1200px");
    content = 1500;
    observerOf(frame.contentDocument!.body).fire();
    expect(f.height()).toBe("1500px");
    f.detach();
  });

  it("observes the document's root and body, and refits on late images, details toggles and width changes only", () => {
    let content = 1200;
    const { box, frame, doc } = setup({ content: () => content });
    const f = attachFrameFit(frame, box, "", origin);
    frame.dispatchEvent(new Event("load"));
    expect(observerOf(doc.documentElement)).toBe(observerOf(doc.body));
    expect(f.height()).toBe("1200px");

    // A late image (load does not bubble: the listener captures).
    content = 1500;
    const img = doc.createElement("img");
    doc.body.append(img);
    img.dispatchEvent(new Event("load"));
    expect(f.height()).toBe("1500px");

    // A details element closes: no resize of the stretched root, the toggle refits.
    content = 400;
    const details = doc.createElement("details");
    doc.body.append(details);
    details.dispatchEvent(new Event("toggle"));
    expect(f.height()).toBe("400px");

    // The frame's own height changes are not a reason to fit; its width is.
    content = 600;
    const outer = observerOf(frame);
    outer.fire(); // the first width
    expect(f.height()).toBe("600px");
    content = 650;
    outer.fire(); // same width: only the height changed
    expect(f.height()).toBe("600px");
    frame.getBoundingClientRect = () => ({ height: 600, width: 390 }) as DOMRect;
    outer.fire();
    expect(f.height()).toBe("650px");
    f.detach();
  });

  it(`stops observing content after ${LOOP_LIMIT} height-changing fits in a row and keeps the height`, () => {
    let n = 0;
    const { box, frame, doc } = setup({ content: () => 1000 + n });
    const f = attachFrameFit(frame, box, "", origin);
    frame.dispatchEvent(new Event("load"));
    const content = observerOf(doc.body);
    for (let i = 0; i < LOOP_LIMIT - 1; i++) {
      n += 10;
      content.fire();
    }
    expect(content.disconnected).toBe(false);
    // A width change or a load in between starts the count again.
    const img = doc.createElement("img");
    doc.body.append(img);
    img.dispatchEvent(new Event("load"));
    for (let i = 0; i < LOOP_LIMIT - 1; i++) {
      n += 10;
      content.fire();
    }
    expect(content.disconnected).toBe(false);
    n += 10;
    content.fire();
    expect(content.disconnected).toBe(true);
    const kept = f.height();
    n += 10;
    content.fire();
    expect(f.height()).toBe(kept);
    f.detach();
  });

  it("does not count fits that leave the height as it is", () => {
    const { box, frame, doc } = setup({ content: () => 1000 });
    const f = attachFrameFit(frame, box, "", origin);
    frame.dispatchEvent(new Event("load"));
    for (let i = 0; i < LOOP_LIMIT * 2; i++) observerOf(doc.body).fire();
    expect(observerOf(doc.body).disconnected).toBe(false);
    f.detach();
  });

  it("disconnects every observer and listener on detach", () => {
    let content = 1200;
    const { box, frame, doc } = setup({ content: () => content });
    const f = attachFrameFit(frame, box, "", origin);
    frame.dispatchEvent(new Event("load"));
    f.detach();
    expect(FakeObserver.all.every((o) => o.disconnected)).toBe(true);
    content = 2000;
    frame.dispatchEvent(new Event("load"));
    doc.body.dispatchEvent(new Event("toggle"));
    expect(f.height()).toBe("1200px");
    // The link blocker is gone with it.
    const a = doc.createElement("a");
    a.setAttribute("href", "/api/status");
    doc.body.append(a);
    const click = new MouseEvent("click", { bubbles: true, cancelable: true });
    a.dispatchEvent(click);
    expect(click.defaultPrevented).toBe(false);
  });

  it("cancels dragging a link into the app, not an external one (dragging to the tab bar opens it)", () => {
    const { box, frame, doc } = setup({ content: () => 1200 });
    const f = attachFrameFit(frame, box, "", origin);
    frame.dispatchEvent(new Event("load"));
    const drag = (href: string) => {
      const a = doc.createElement("a");
      a.setAttribute("href", href);
      doc.body.append(a);
      const e = new Event("dragstart", { bubbles: true, cancelable: true, composed: true });
      a.dispatchEvent(e);
      return e.defaultPrevented;
    };
    expect(drag("/api/status")).toBe(true);
    expect(drag("javascript:void(0)")).toBe(true);
    expect(drag("https://example.org/")).toBe(false);
    f.detach();
    expect(drag("/api/status")).toBe(false);
  });

  it("installs the link blocker for click and auxclick on each load", () => {
    const { box, frame, doc } = setup({ content: () => 1200 });
    const f = attachFrameFit(frame, box, "", origin);
    frame.dispatchEvent(new Event("load"));
    const a = doc.createElement("a");
    a.setAttribute("href", "/api/status");
    a.setAttribute("target", "_self");
    doc.body.append(a);
    for (const type of ["click", "auxclick"]) {
      const e = new MouseEvent(type, { bubbles: true, cancelable: true, button: type === "auxclick" ? 1 : 0 });
      a.dispatchEvent(e);
      expect(e.defaultPrevented, type).toBe(true);
    }
    const out = doc.createElement("a");
    out.setAttribute("href", "https://example.org/");
    doc.body.append(out);
    const e = new MouseEvent("auxclick", { bubbles: true, cancelable: true, button: 1 });
    out.dispatchEvent(e);
    expect(e.defaultPrevented).toBe(false);
    f.detach();
  });
});

/** requestAnimationFrame and cancelAnimationFrame that tests drive by hand: run() calls the pending callbacks once. */
function animationFrames() {
  let next = 1;
  const queue = new Map<number, FrameRequestCallback>();
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
    queue.set(next, cb);
    return next++;
  });
  vi.stubGlobal("cancelAnimationFrame", (id: number) => void queue.delete(id));
  return {
    pending: () => queue.size,
    run() {
      const cbs = [...queue.values()];
      queue.clear();
      for (const cb of cbs) cb(0);
    },
  };
}

describe("attachFrameFit: inert until the link blocker is installed (#209)", () => {
  beforeEach(() => {
    FakeObserver.all = [];
    vi.stubGlobal("ResizeObserver", FakeObserver);
  });

  /**
   * A frame that shows `shown` (the initial about:blank first), with a link
   * into the app in the mail's document. Records, each time inert is
   * removed, whether a click on that link is cancelled at that moment.
   */
  function frameWithMail(readyState = "loading") {
    const s = setup({ content: () => 1200 });
    Object.defineProperty(s.doc, "readyState", { value: readyState, configurable: true });
    const blank = document.implementation.createHTMLDocument("");
    Object.defineProperty(blank, "URL", { value: "about:blank" });
    const elsewhere = document.implementation.createHTMLDocument("");
    Object.defineProperty(elsewhere, "URL", { value: "https://elsewhere.example/" });
    let shown: Document = blank;
    Object.defineProperty(s.frame, "contentDocument", { get: () => shown, configurable: true });
    const a = s.doc.createElement("a");
    a.setAttribute("href", "/api/status");
    s.doc.body.append(a);
    const blockedAtRemoval: boolean[] = [];
    const remove = s.frame.removeAttribute.bind(s.frame);
    s.frame.removeAttribute = (name: string) => {
      if (name === "inert") {
        const click = new MouseEvent("click", { bubbles: true, cancelable: true, composed: true });
        a.dispatchEvent(click);
        blockedAtRemoval.push(click.defaultPrevented);
      }
      remove(name);
    };
    return {
      ...s,
      blockedAtRemoval,
      show: (which: "blank" | "elsewhere" | "mail") => (shown = which === "blank" ? blank : which === "elsewhere" ? elsewhere : s.doc),
    };
  }

  it("sets the inert attribute on attach and keeps it while the frame shows about:blank or another URL", () => {
    const raf = animationFrames();
    const m = frameWithMail();
    const f = attachFrameFit(m.frame, m.box, "", origin);
    expect(m.frame.getAttribute("inert")).toBe("");
    expect(raf.pending()).toBe(1);
    raf.run();
    raf.run();
    expect(m.frame.hasAttribute("inert")).toBe(true);
    m.show("elsewhere");
    raf.run();
    expect(m.frame.hasAttribute("inert")).toBe(true);
    // Still polling: the mail's document is not there yet.
    expect(raf.pending()).toBe(1);
    expect(m.blockedAtRemoval).toEqual([]);
    f.detach();
  });

  it("installs the blocker from the poll as soon as the mail's document exists, before its load, and only then lifts inert", () => {
    const raf = animationFrames();
    const m = frameWithMail();
    const f = attachFrameFit(m.frame, m.box, "", origin);
    raf.run();
    m.show("mail");
    expect(m.frame.hasAttribute("inert")).toBe(true);
    raf.run();
    expect(m.frame.hasAttribute("inert")).toBe(false);
    // At the moment inert went, a click into the app was already cancelled.
    expect(m.blockedAtRemoval).toEqual([true]);
    // The poll stopped with the install.
    expect(raf.pending()).toBe(0);
    raf.run();
    expect(raf.pending()).toBe(0);
    // Load installs again, with the body, and fits.
    m.frame.dispatchEvent(new Event("load"));
    expect(m.blockedAtRemoval).toEqual([true, true]);
    expect(f.height()).toBe("1200px");
    f.detach();
  });

  it("installs on load when the poll has not found the document yet, and stops the poll", () => {
    const raf = animationFrames();
    const m = frameWithMail();
    const f = attachFrameFit(m.frame, m.box, "", origin);
    expect(raf.pending()).toBe(1);
    m.show("mail");
    m.frame.dispatchEvent(new Event("load"));
    expect(m.frame.hasAttribute("inert")).toBe(false);
    expect(m.blockedAtRemoval).toEqual([true]);
    expect(raf.pending()).toBe(0);
    f.detach();
  });

  it("installs at once when the document is there on attach: loaded, or still loading", () => {
    const raf = animationFrames();
    const loaded = frameWithMail("complete");
    loaded.show("mail");
    const f = attachFrameFit(loaded.frame, loaded.box, "", origin);
    expect(loaded.frame.hasAttribute("inert")).toBe(false);
    expect(loaded.blockedAtRemoval).toEqual([true]);
    expect(f.height()).toBe("1200px");
    expect(raf.pending()).toBe(0);
    f.detach();

    const loading = frameWithMail("loading");
    loading.show("mail");
    const g = attachFrameFit(loading.frame, loading.box, "", origin);
    expect(loading.frame.hasAttribute("inert")).toBe(false);
    expect(loading.blockedAtRemoval).toEqual([true]);
    expect(raf.pending()).toBe(0);
    g.detach();
  });

  it("keeps the inert attribute on detach and stops the poll", () => {
    const raf = animationFrames();
    const m = frameWithMail();
    const f = attachFrameFit(m.frame, m.box, "", origin);
    expect(raf.pending()).toBe(1);
    f.detach();
    expect(m.frame.hasAttribute("inert")).toBe(true);
    expect(raf.pending()).toBe(0);
    // The document arrives after detach: nothing installs, the frame stays inert.
    m.show("mail");
    raf.run();
    m.frame.dispatchEvent(new Event("load"));
    expect(m.frame.hasAttribute("inert")).toBe(true);
    expect(m.blockedAtRemoval).toEqual([]);
  });
});

describe("attachFrameFit: inert whenever the frame leaves the guarded mail document (#265)", () => {
  beforeEach(() => {
    FakeObserver.all = [];
    vi.stubGlobal("ResizeObserver", FakeObserver);
  });

  /** A mail document, still loading, with a link into the app and a window that pagehide is dispatched on. */
  function mailDocument(url = frameURL) {
    const doc = document.implementation.createHTMLDocument("");
    Object.defineProperty(doc, "URL", { value: url });
    Object.defineProperty(doc, "baseURI", { value: url });
    Object.defineProperty(doc, "readyState", { value: "loading", configurable: true });
    const win = new EventTarget();
    Object.defineProperty(doc, "defaultView", { value: win });
    const a = doc.createElement("a");
    a.setAttribute("href", "/api/status");
    doc.body.append(a);
    return {
      doc,
      /** Whether a click on its link into the app is cancelled now. */
      clickBlocked() {
        const click = new MouseEvent("click", { bubbles: true, cancelable: true, composed: true });
        a.dispatchEvent(click);
        return click.defaultPrevented;
      },
      /** The frame leaves the document; persisted: into the back/forward cache. */
      pagehide(persisted = false) {
        const e = new Event("pagehide");
        Object.defineProperty(e, "persisted", { value: persisted });
        win.dispatchEvent(e);
      },
    };
  }

  /**
   * A frame that shows `shown` (null: a document the parent cannot read,
   * such as the browser's error page). Records, each time inert is removed,
   * whether a click into the app on the shown document is cancelled then.
   */
  function frameAt(first: Document | null) {
    const s = setup({ content: () => 1200 });
    let shown = first;
    Object.defineProperty(s.frame, "contentDocument", { get: () => shown, configurable: true });
    const blockedAtRemoval: boolean[] = [];
    const remove = s.frame.removeAttribute.bind(s.frame);
    s.frame.removeAttribute = (name: string) => {
      if (name === "inert" && shown) {
        const click = new MouseEvent("click", { bubbles: true, cancelable: true, composed: true });
        shown.querySelector("a")!.dispatchEvent(click);
        blockedAtRemoval.push(click.defaultPrevented);
      }
      remove(name);
    };
    return { ...s, blockedAtRemoval, show: (doc: Document | null) => (shown = doc) };
  }

  /** A same-origin document that is not a mail (a route the frame was navigated to). */
  function otherDocument() {
    const doc = document.implementation.createHTMLDocument("");
    Object.defineProperty(doc, "URL", { value: `${origin}/api/openapi.json?again` });
    doc.body.append(doc.createElement("a"));
    return doc;
  }

  /** A frame with the mail installed by the poll, its blocker on. */
  function installed() {
    const raf = animationFrames();
    const mail = mailDocument();
    const m = frameAt(mail.doc);
    const f = attachFrameFit(m.frame, m.box, "", origin);
    expect(m.frame.hasAttribute("inert")).toBe(false);
    expect(mail.clickBlocked()).toBe(true);
    expect(raf.pending()).toBe(0);
    return { raf, mail, m, f };
  }

  it("turns inert on the guarded document's pagehide, removes its listeners and polls again", () => {
    const { raf, mail, m, f } = installed();
    mail.pagehide();
    expect(m.frame.getAttribute("inert")).toBe("");
    // The listeners are off: a click into the app on the old document is no longer cancelled.
    expect(mail.clickBlocked()).toBe(false);
    expect(raf.pending()).toBe(1);
    f.detach();
  });

  it("stays inert on an unreadable document, another URL and the left document, and installs the next mail document before lifting inert", () => {
    const { raf, mail, m, f } = installed();
    mail.pagehide();
    // The browser's error page (or any document the parent cannot read).
    m.show(null);
    raf.run();
    expect(m.frame.hasAttribute("inert")).toBe(true);
    expect(raf.pending()).toBe(1);
    // A same-origin document that is no mail.
    m.show(otherDocument());
    raf.run();
    expect(m.frame.hasAttribute("inert")).toBe(true);
    expect(raf.pending()).toBe(1);
    // The left document while it is still the frame's.
    m.show(mail.doc);
    raf.run();
    expect(m.frame.hasAttribute("inert")).toBe(true);
    expect(mail.clickBlocked()).toBe(false);
    expect(raf.pending()).toBe(1);
    // Back: the mail's document again (a new one), still loading.
    const again = mailDocument();
    m.show(again.doc);
    expect(m.frame.hasAttribute("inert")).toBe(true);
    raf.run();
    expect(m.frame.hasAttribute("inert")).toBe(false);
    // The first removal was the install on attach; at the second, the new
    // document's click into the app was already cancelled.
    expect(m.blockedAtRemoval).toEqual([true, true]);
    expect(again.clickBlocked()).toBe(true);
    expect(raf.pending()).toBe(0);
    // Its own pagehide gates it again: the left document was cleared and replaced.
    again.pagehide();
    expect(m.frame.hasAttribute("inert")).toBe(true);
    expect(raf.pending()).toBe(1);
    f.detach();
  });

  it("keeps polling and the frame inert on a load without a mail document, and installs a later one from the poll", () => {
    // From attach: the frame's first load is the error page.
    const raf = animationFrames();
    const m = frameAt(null);
    const f = attachFrameFit(m.frame, m.box, "", origin);
    m.frame.dispatchEvent(new Event("load"));
    expect(m.frame.hasAttribute("inert")).toBe(true);
    expect(raf.pending()).toBe(1);
    const mail = mailDocument();
    m.show(mail.doc);
    raf.run();
    expect(m.frame.hasAttribute("inert")).toBe(false);
    expect(m.blockedAtRemoval).toEqual([true]);
    expect(raf.pending()).toBe(0);
    f.detach();

    // After a pagehide: the error page's load, and the left document's.
    const s = installed();
    s.mail.pagehide();
    s.m.show(null);
    s.m.frame.dispatchEvent(new Event("load"));
    expect(s.m.frame.hasAttribute("inert")).toBe(true);
    expect(s.raf.pending()).toBe(1);
    s.m.show(s.mail.doc);
    s.m.frame.dispatchEvent(new Event("load"));
    expect(s.m.frame.hasAttribute("inert")).toBe(true);
    expect(s.mail.clickBlocked()).toBe(false);
    expect(s.raf.pending()).toBe(1);
    const next = mailDocument();
    s.m.show(next.doc);
    s.raf.run();
    expect(s.m.frame.hasAttribute("inert")).toBe(false);
    expect(next.clickBlocked()).toBe(true);
    expect(s.raf.pending()).toBe(0);
    s.f.detach();
  });

  it("changes nothing on a persisted pagehide (back/forward cache): not inert, listeners on, no poll", () => {
    const { raf, mail, m, f } = installed();
    mail.pagehide(true);
    expect(m.frame.hasAttribute("inert")).toBe(false);
    expect(mail.clickBlocked()).toBe(true);
    expect(raf.pending()).toBe(0);
    f.detach();
  });

  it("does nothing on a pagehide after detach, and detach stops a poll that a pagehide started", () => {
    const a = installed();
    a.f.detach();
    a.mail.pagehide();
    expect(a.m.frame.hasAttribute("inert")).toBe(false);
    expect(a.raf.pending()).toBe(0);

    const b = installed();
    b.mail.pagehide();
    expect(b.raf.pending()).toBe(1);
    b.f.detach();
    expect(b.raf.pending()).toBe(0);
    // A mail document after detach: nothing installs, the frame stays inert.
    const later = mailDocument();
    b.m.show(later.doc);
    b.raf.run();
    b.m.frame.dispatchEvent(new Event("load"));
    expect(b.m.frame.hasAttribute("inert")).toBe(true);
    expect(later.clickBlocked()).toBe(false);
  });
});

describe("blockAppLinks", () => {
  const { doc } = setup({ content: () => 0 });
  /** Whether a click on el is cancelled. */
  const blocked = (el: Element, type = "click") => {
    // composed, like the browser's: the event leaves shadow roots.
    const e = new MouseEvent(type, { bubbles: true, cancelable: true, composed: true });
    const listener = (ev: Event) => blockAppLinks(ev, origin);
    doc.addEventListener(type, listener, true);
    el.dispatchEvent(e);
    doc.removeEventListener(type, listener, true);
    return e.defaultPrevented;
  };
  const html = (markup: string) => {
    const div = doc.createElement("div");
    div.innerHTML = markup;
    doc.body.append(div);
    return div;
  };

  it("cancels links into the app, relative, absolute and scheme-relative, with any target", () => {
    for (const href of ["/api/status", "../../status", "/api/messages/x/raw", `${origin}/api/status`, "//archive.example/api/status", "?images=1", "", " /api/status"]) {
      const a = html(`<a href="${href}" target="_self">x</a>`).firstElementChild!;
      expect(blocked(a), href).toBe(true);
      expect(blocked(a, "auxclick"), href).toBe(true);
    }
  });

  it("leaves external links and other schemes alone", () => {
    for (const href of ["https://example.org/", "http://archive.example/", "https://archive.example:8443/", "mailto:sam@example.org", "https://archive.example.evil.example/"]) {
      expect(blocked(html(`<a href="${href}">x</a>`).firstElementChild!), href).toBe(false);
    }
  });

  it("finds the link from an element inside it", () => {
    const div = html(`<a href="/api/status"><span><b>deep</b></span></a>`);
    expect(blocked(div.querySelector("b")!)).toBe(true);
  });

  it("ignores clicks outside links and anchors without href", () => {
    const div = html(`<p>text</p><a name="top">anchor</a>`);
    expect(blocked(div.querySelector("p")!)).toBe(false);
    expect(blocked(div.querySelector("a")!)).toBe(false);
  });

  it("cancels an <area> of an image map into the app, not an external one", () => {
    const div = html(`<map name="m"><area id="in" href="/api/status" shape="rect" coords="0,0,1,1"><area id="out" href="https://example.org/"></map>`);
    expect(blocked(div.querySelector("#in")!)).toBe(true);
    expect(blocked(div.querySelector("#out")!)).toBe(false);
  });

  it("cancels SVG links into the app, with href or xlink:href", () => {
    const svg = doc.createElementNS(svgNS, "svg");
    const a = doc.createElementNS(svgNS, "a");
    a.setAttribute("href", "/api/status");
    const rect = doc.createElementNS(svgNS, "rect");
    a.append(rect);
    const old = doc.createElementNS(svgNS, "a");
    old.setAttributeNS(xlinkNS, "xlink:href", "/api/status");
    const text = doc.createElementNS(svgNS, "text");
    old.append(text);
    const out = doc.createElementNS(svgNS, "a");
    out.setAttribute("href", "https://example.org/");
    svg.append(a, old, out);
    doc.body.append(svg);
    expect(blocked(rect)).toBe(true);
    expect(blocked(text)).toBe(true);
    expect(blocked(out)).toBe(false);
  });

  it("decides by the innermost link, which the browser follows", () => {
    // An external HTML link around an SVG link into the app.
    const outer = doc.createElement("a");
    outer.setAttribute("href", "https://example.org/");
    const svg = doc.createElementNS(svgNS, "svg");
    const inner = doc.createElementNS(svgNS, "a");
    inner.setAttributeNS(xlinkNS, "xlink:href", "/api/status");
    const rect = doc.createElementNS(svgNS, "rect");
    inner.append(rect);
    svg.append(inner);
    outer.append(svg);
    doc.body.append(outer);
    expect(blocked(rect)).toBe(true);
  });

  it("cancels javascript:, data: and vbscript: links, which would run with the app's origin in a new tab", () => {
    for (const href of ["javascript:void(0)", " JavaScript:alert(1)", "JAVASCRIPT:alert(1)", "java\tscript:alert(1)", "data:text/html,x", "DATA:text/html,x", "vbscript:msgbox(1)", "VBScript:msgbox(1)"]) {
      expect(blocked(html(`<a href="${href}">x</a>`).firstElementChild!), href).toBe(true);
    }
  });

  it("cancels a link into the app inside an open shadow root (declarative or attached)", () => {
    const host = doc.createElement("div");
    doc.body.append(host);
    const root = host.attachShadow({ mode: "open" });
    root.innerHTML = `<a href="/api/openapi.json"><span>spec</span></a><a href="https://example.org/">out</a>`;
    expect(blocked(root.querySelector("span")!)).toBe(true);
    expect(blocked(root.querySelector('a[href^="https"]')!)).toBe(false);
  });

  it("does not see into a closed shadow root (a documented residual; the server refuses most routes)", () => {
    const host = doc.createElement("div");
    doc.body.append(host);
    const root = host.attachShadow({ mode: "closed" });
    root.innerHTML = `<a href="/api/status">x</a>`;
    expect(blocked(root.querySelector("a")!)).toBe(false);
  });

  it("cancels click, auxclick and dragstart through an SVG <use>, whatever it points to (#209)", () => {
    // In a browser, use clones the element it references (here a link into
    // the app) into a user-agent shadow tree: the path starts at use.
    const svg = doc.createElementNS(svgNS, "svg");
    const defs = doc.createElementNS(svgNS, "defs");
    const target = doc.createElementNS(svgNS, "a");
    target.setAttribute("id", "spec");
    target.setAttribute("href", "/api/openapi.json");
    defs.append(target);
    const use = doc.createElementNS(svgNS, "use");
    use.setAttribute("href", "#spec");
    // An external link around a use: the clone inside it is what the browser follows.
    const out = doc.createElementNS(svgNS, "a");
    out.setAttribute("href", "https://example.org/");
    const wrapped = doc.createElementNS(svgNS, "use");
    wrapped.setAttribute("href", "#spec");
    out.append(wrapped);
    // The same in an HTML link around the svg.
    const htmlOut = doc.createElement("a");
    htmlOut.setAttribute("href", "https://example.org/");
    const inner = doc.createElementNS(svgNS, "svg");
    const innerUse = doc.createElementNS(svgNS, "use");
    innerUse.setAttribute("href", "#spec");
    inner.append(innerUse);
    htmlOut.append(inner);
    svg.append(defs, use, out);
    doc.body.append(svg, htmlOut);
    for (const type of ["click", "auxclick", "dragstart"]) {
      expect(blocked(use, type), type).toBe(true);
      expect(blocked(wrapped, type), type).toBe(true);
      expect(blocked(innerUse, type), type).toBe(true);
    }
    // An external link without use still opens; an HTML element named use is not SVG's.
    expect(blocked(out)).toBe(false);
    const htmlUse = doc.createElement("use");
    const plain = doc.createElement("a");
    plain.setAttribute("href", "https://example.org/");
    plain.append(htmlUse);
    doc.body.append(plain);
    expect(blocked(htmlUse)).toBe(false);
  });

  it("cancels through an SVG <use> also when the event has no composed path", () => {
    const use = doc.createElementNS(svgNS, "use");
    const svg = doc.createElementNS(svgNS, "svg");
    svg.append(use);
    doc.body.append(svg);
    let prevented = false;
    const e = { target: use, composedPath: () => [], preventDefault: () => (prevented = true) } as unknown as Event;
    blockAppLinks(e, origin);
    expect(prevented).toBe(true);
  });

  it("walks the target's ancestors when the event has no composed path", () => {
    const span = html(`<a href="/api/status"><span>x</span></a>`).querySelector("span")!;
    let prevented = false;
    const e = { target: span, composedPath: () => [], preventDefault: () => (prevented = true) } as unknown as Event;
    blockAppLinks(e, origin);
    expect(prevented).toBe(true);
  });

  it("cancels a link whose URL does not parse", () => {
    expect(blocked(html(`<a href="https://[bad">x</a>`).firstElementChild!)).toBe(true);
  });
});
