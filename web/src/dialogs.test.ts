// Which key presses belong to a component, so the app's shortcuts leave them alone.
import { afterEach, describe, expect, it } from "vitest";
import { dialogOpen, ownedByWidget } from "./dialogs";

afterEach(() => {
  document.body.innerHTML = "";
});

/** A key press on the element that matches selector in html. */
function press(html: string, selector: string, key: string): boolean {
  document.body.innerHTML = html;
  const target = document.querySelector(selector)!;
  const e = new KeyboardEvent("keydown", { key, bubbles: true });
  Object.defineProperty(e, "target", { value: target });
  return ownedByWidget(e);
}

describe("ownedByWidget", () => {
  it("leaves plain buttons, links and tree nodes to the shortcuts", () => {
    for (const key of ["j", "k", "?", "/", "Escape"]) {
      expect(press('<button id="b">x</button>', "#b", key), key).toBe(false);
      expect(press('<a id="a" href="/">x</a>', "#a", key), key).toBe(false);
      expect(press('<ul role="tree"><li role="treeitem" id="n">INBOX</li></ul>', "#n", key), key).toBe(false);
      // A tooltip's trigger (closed tooltip) is a plain button too.
      expect(press('<span class="cds--popover-container"><button id="t">x</button><span class="cds--popover"><span class="cds--popover-content">hint</span></span></span>', "#t", key), key).toBe(false);
    }
  });

  it("gives every key to fields, Carbon's Search included", () => {
    for (const key of ["j", "?", "/", "Escape"]) {
      expect(press('<div class="cds--search"><input id="s" type="search"></div>', "#s", key), key).toBe(true);
      expect(press('<textarea id="t"></textarea>', "#t", key), key).toBe(true);
      expect(press('<select id="s"><option>a</option></select>', "#s", key), key).toBe(true);
      expect(press('<div id="c" contenteditable="true">x</div>', "#c", key), key).toBe(true);
    }
  });

  it("gives every key to open menus, list boxes, popover content and dialogs", () => {
    for (const key of ["j", "k", "u", "Escape"]) {
      expect(press('<ul role="menu"><li><button role="menuitem" id="m">Copy</button></li></ul>', "#m", key), key).toBe(true);
      expect(press('<div role="listbox"><div role="option" id="o">a</div></div>', "#o", key), key).toBe(true);
      expect(press('<span class="cds--popover--open"><span class="cds--popover-content"><button id="p">x</button></span></span>', "#p", key), key).toBe(true);
      expect(press('<div class="cds--modal is-visible"><div role="dialog"><button id="d">x</button></div></div>', "#d", key), key).toBe(true);
    }
  });

  it("gives Escape to an open tooltip anywhere and to the SideNav, other keys not", () => {
    const tooltip = '<span class="cds--popover-container cds--popover--open"><button id="t">x</button></span><button id="b">y</button>';
    expect(press(tooltip, "#b", "Escape")).toBe(true);
    expect(press(tooltip, "#t", "Escape")).toBe(true);
    expect(press(tooltip, "#t", "j")).toBe(false);
    const nav = '<nav class="cds--side-nav"><a id="l" href="/">Log</a></nav>';
    expect(press(nav, "#l", "Escape")).toBe(true);
    expect(press(nav, "#l", "j")).toBe(false);
  });
});

describe("dialogOpen", () => {
  it("sees a visible Carbon Modal, the folder overlay and the conversation overlay, not a closed Modal", () => {
    document.body.innerHTML = '<div class="cds--modal"></div>';
    expect(dialogOpen()).toBe(false);
    document.body.innerHTML = '<div class="cds--modal is-visible"></div>';
    expect(dialogOpen()).toBe(true);
    document.body.innerHTML = '<aside class="folder-pane folder-pane--overlay"></aside>';
    expect(dialogOpen()).toBe(true);
    document.body.innerHTML = '<div class="conversation-overlay"><div role="dialog" aria-modal="true"></div></div>';
    expect(dialogOpen()).toBe(true);
  });
});
