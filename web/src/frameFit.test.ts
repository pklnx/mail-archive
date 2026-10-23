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
  });
  Object.defineProperty(doc.body, "scrollHeight", {
    get: () => (record(), mail.quirks === false ? mail.content(h()) - 16 : Math.max(mail.content(h()), h())),
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
  const observerOf = (el: Element) => FakeObserver.all.find((o) => o.targets.includes(el))!;

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

describe("blockAppLinks", () => {
  const { doc } = setup({ content: () => 0 });
  /** Whether a click on el is cancelled. */
  const blocked = (el: Element, type = "click") => {
    const e = new MouseEvent(type, { bubbles: true, cancelable: true });
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

  it("cancels javascript: links, which would run with the app's origin in a new tab", () => {
    for (const href of ["javascript:void(0)", " JavaScript:alert(1)", "java\tscript:alert(1)"]) {
      expect(blocked(html(`<a href="${href}">x</a>`).firstElementChild!), href).toBe(true);
    }
  });

  it("cancels a link whose URL does not parse", () => {
    expect(blocked(html(`<a href="https://[bad">x</a>`).firstElementChild!)).toBe(true);
  });
});
