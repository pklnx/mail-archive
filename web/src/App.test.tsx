// The shell's navigation in the app: the SideNav overlay and the folder
// overlay below lg, the rail and the folder column from lg up, and the
// keyboard shortcuts next to them. The API is a stub; no network.
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import type { Account, AccountsResponse, Folder, MessageDetail, MessageSummary, User } from "./api";
import { applySettings, messagesFor, t } from "./i18n";
import { filterTextFor } from "./filterText";
import { lg, twoPane as twoPaneQuery } from "./breakpoints";

const f = (name: string, messages = 1): Folder => ({ name, delimiter: "/", messages, lastSyncedAt: null, lastReconciledAt: null });

function account(name: string, folders: Folder[]): Account {
  return {
    name, kind: "imap", enabled: true, removed: false, purging: false, purge: null, host: "h", port: 993, tls: "tls", username: "u",
    includedFolders: [], excludedFolders: [], syncInterval: "default", folders, goneMessages: 0, lastReconciledAt: null,
    addresses: [], lastReceivedAt: null, fill: null, fills: [], firstReceived: null,
    sync: { state: "idle", lastRun: null, failureStreak: 0, failingSince: null, health: "ok", syncEvery: null, nextAt: null },
  };
}

const accounts: AccountsResponse = {
  accounts: [account("work", [f("INBOX", 5), f("Sent", 2)])],
  manage: true, syncInterval: "6h0m0s", syncIntervalMin: "5m0s", reconcileInterval: "", alertAfter: 3, smtp: null,
};

const user = (admin = false): User => ({ name: "alice", admin, mustChangePassword: false, twoFactorEnabled: false, twoFactorRequired: false, language: "auto", locale: "auto" });

/** matchMedia that answers the lg query (Carbon's 66rem) with wide and the two-pane query (48rem) with twoPane. */
function screenWidth(wide: boolean, twoPane = wide) {
  window.matchMedia = (query: string) =>
    ({
      matches: (wide && query.includes("66rem")) || (twoPane && query.includes("48rem")),
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }) as MediaQueryList;
}

const summary = (id: string, subject: string): MessageSummary => ({
  id, size: 1, subject, from: "Sam <sam@example.org>", sentAt: "2026-10-01T09:00:00Z", sortAt: "2026-10-01T09:00:00Z", hasAttachment: false,
});
const detail = (id: string): MessageDetail => ({
  ...summary(id, `Subject ${id}`), messageId: `<${id}@example.org>`, to: "alice@example.org", cc: "", dateHeader: "", text: "Hello",
  hasHtml: false, truncated: false, parts: [], locations: [], retainUntil: null,
});
/** The messages the list gets; none unless a test sets them. */
let listed: MessageSummary[] = [];

const original = window.matchMedia;
// jsdom lays nothing out: every box is 0 × 0 at the origin, so the filter
// popover's autoAlign (floating-ui's hide middleware) would hide its content
// as if its button were out of view. A box of its own for every element.
const rect = Element.prototype.getBoundingClientRect;
beforeEach(() => {
  Element.prototype.getBoundingClientRect = () => DOMRect.fromRect({ x: 100, y: 100, width: 32, height: 32 });
  // The viewport floating-ui clips to.
  Object.defineProperty(document.documentElement, "clientWidth", { configurable: true, value: 1024 });
  Object.defineProperty(document.documentElement, "clientHeight", { configurable: true, value: 768 });
});
afterEach(() => {
  Element.prototype.getBoundingClientRect = rect;
});
beforeEach(() => {
  listed = [];
  window.history.replaceState(null, "", "/");
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      const body = path.startsWith("/api/accounts")
        ? accounts
        : path.startsWith("/api/messages?")
          ? { messages: listed, nextCursor: null }
          : path.endsWith("/conversation")
            ? { messages: [], total: 0, truncated: false }
            : path.startsWith("/api/messages/")
              ? detail(path.split("/")[3]!)
              : {};
      return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.matchMedia = original;
  localStorage.clear();
});

// The shortcuts overview is a lazy chunk (#288). It is loaded once, before
// the first test: a "?" that must not open it is then checked against a
// loaded chunk, which would show at once, not against one still on its way.
// Its first load and a failed load have their own tests (App.shortcutsLoad,
// App.shortcutsFail).
let shortcutsLoaded = false;
beforeEach(async () => {
  if (shortcutsLoaded) return;
  render(<App user={user()} logout={() => {}} logoutError="" />);
  fireEvent.keyDown(document.body, { key: "?" });
  await screen.findByRole("dialog", { name: t.shortcuts });
  cleanup();
  window.history.replaceState(null, "", "/");
  shortcutsLoaded = true;
});

/** The focus moves into a same-origin frame: the window blurs while the iframe is the active element (#349). */
function intoFrame() {
  const frame = document.body.appendChild(document.createElement("iframe"));
  act(() => frame.focus());
  expect(document.activeElement).toBe(frame);
  act(() => void window.dispatchEvent(new Event("blur")));
  frame.remove();
}

async function renderApp(admin = false, logout = () => {}, logoutError = "") {
  render(<App user={user(admin)} logout={logout} logoutError={logoutError} />);
  // The accounts are loaded: the tree has them.
  await screen.findByRole("treeitem", { name: /^work$/, hidden: true });
}

const menuButton = () => screen.getByRole("button", { name: t.showMenu });
const sideNav = () => screen.getByRole("navigation", { name: t.mainNavigation });
const foldersButton = () => screen.getByRole("button", { name: t.navFolders });
const foldersDialog = () => screen.queryByRole("dialog", { name: t.navFolders });
const key = (el: Element, k: string) => fireEvent.keyDown(el, { key: k });

describe("phone navigation (below lg)", () => {
  beforeEach(() => screenWidth(false));

  it("has no bottom bar and no More sheet: the sections are in the SideNav", async () => {
    await renderApp(true);
    expect(screen.queryByRole("button", { name: "More" })).toBeNull();
    expect(screen.queryAllByRole("navigation", { name: t.mainNavigation })).toHaveLength(1);
    expect(within(sideNav()).getAllByRole("link").map((l) => l.textContent)).toEqual([t.allMail, t.navAccounts, t.users, t.log, t.retention, t.profile]);
  });

  it("opens the SideNav from the menu button, with focus in it, and closes it with Escape", async () => {
    await renderApp();
    const button = menuButton();
    expect(sideNav().hasAttribute("inert")).toBe(true);
    button.focus();
    fireEvent.click(button);
    const nav = sideNav();
    expect(nav.className).toContain("cds--side-nav--expanded");
    expect(button.getAttribute("aria-expanded")).toBe("true");
    expect(nav.hasAttribute("inert")).toBe(false);
    expect(document.activeElement).toBe(within(nav).getByRole("link", { name: t.allMail }));
    // The page behind it is inert; the header is not, so the button closes it.
    expect(foldersButton().closest("[inert]")).not.toBeNull();
    expect(button.closest("[inert]")).toBeNull();
    // The folder overlay cannot open from inside it.
    fireEvent.click(foldersButton());
    expect(foldersDialog()).toBeNull();
    key(document.activeElement!, "Escape");
    expect(nav.className).not.toContain("cds--side-nav--expanded");
    expect(document.activeElement).toBe(button);
    expect(foldersButton().closest("[inert]")).toBeNull();
  });

  it("goes to a section from the SideNav and closes it", async () => {
    await renderApp();
    menuButton().focus();
    fireEvent.click(menuButton());
    fireEvent.click(within(sideNav()).getByRole("link", { name: t.log }));
    expect(window.location.search).toBe("?view=log");
    expect(sideNav().className).not.toContain("cds--side-nav--expanded");
    expect(document.activeElement).toBe(menuButton());
  });

  it("opens the folder tree as a modal overlay: focus trapped, Escape closes, focus returns", async () => {
    await renderApp();
    const button = foldersButton();
    expect(button.getAttribute("aria-haspopup")).toBe("dialog");
    button.focus();
    fireEvent.click(button);
    const dialog = foldersDialog()!;
    expect(dialog.getAttribute("aria-modal")).toBe("true");
    expect(button.getAttribute("aria-expanded")).toBe("true");
    // Focus moves into it, on the first account.
    expect(document.activeElement).toBe(within(dialog).getByRole("treeitem", { name: /^work$/ }));
    // Everything else is inert, the header too: no other overlay opens from it.
    expect(menuButton().closest("[inert]")).not.toBeNull();
    expect(button.closest("[inert]")).not.toBeNull();
    expect(screen.getByRole("list", { hidden: true, name: t.messages }).closest("[inert]")).not.toBeNull();
    expect(dialog.closest("[inert]")).toBeNull();
    expect(document.querySelector(".folder-pane__scrim")).not.toBeNull();
    // The SideNav cannot open from behind it.
    fireEvent.click(menuButton());
    expect(sideNav().className).not.toContain("cds--side-nav--expanded");
    expect(foldersDialog()).not.toBeNull();
    dialog.querySelector<HTMLElement>("[role=treeitem]")!.focus();
    // The shortcuts are off while it is open.
    key(document.activeElement!, "?");
    expect(screen.queryByRole("dialog", { name: t.shortcuts })).toBeNull();
    expect(document.querySelector(".cds--modal")).toBeNull();
    // Escape on a tree node: Carbon's TreeView keeps the key from bubbling.
    key(document.activeElement!, "Escape");
    expect(foldersDialog()).toBeNull();
    expect(document.activeElement).toBe(button);
    expect(menuButton().closest("[inert]")).toBeNull();
    expect(document.querySelector(".folder-pane__scrim")).toBeNull();
  });

  it("closes the folder overlay with the scrim and the close button, and after choosing a folder", async () => {
    await renderApp();
    fireEvent.click(foldersButton());
    fireEvent.click(document.querySelector(".folder-pane__scrim")!);
    expect(foldersDialog()).toBeNull();
    fireEvent.click(foldersButton());
    fireEvent.click(within(foldersDialog()!).getByRole("button", { name: t.closeFolders }));
    expect(foldersDialog()).toBeNull();
    foldersButton().focus();
    fireEvent.click(foldersButton());
    fireEvent.click(within(foldersDialog()!).getByRole("treeitem", { name: "Sent 2" }));
    expect(window.location.search).toBe("?account=work&folder=Sent");
    expect(foldersDialog()).toBeNull();
    expect(document.activeElement).toBe(foldersButton());
  });

  it("focuses the selected folder when the overlay opens", async () => {
    window.history.replaceState(null, "", "/?account=work&folder=Sent");
    await renderApp();
    fireEvent.click(foldersButton());
    expect(document.activeElement).toBe(within(foldersDialog()!).getByRole("treeitem", { name: "Sent 2" }));
  });

  it("speaks German in the header and the overlays", async () => {
    applySettings({ language: "de", locale: "de" });
    try {
      const de = messagesFor("de");
      await renderApp(true);
      expect(screen.getByRole("button", { name: de.showMenu })).toBeTruthy();
      fireEvent.click(screen.getByRole("button", { name: de.userPanelOf("alice") }));
      const panel = screen.getByRole("list", { name: de.userPanelOf("alice") });
      expect(within(panel).getByRole("link", { name: de.profile })).toBeTruthy();
      expect(within(panel).getByRole("button", { name: de.logOut })).toBeTruthy();
      expect(within(panel).getByRole("button", { name: de.shortcuts })).toBeTruthy();
      expect(within(panel).getByRole("button", { name: de.switchTheme("dark") })).toBeTruthy();
      key(document.activeElement!, "Escape");
      fireEvent.click(screen.getByRole("button", { name: de.navFolders }));
      expect(screen.getByRole("dialog", { name: de.navFolders })).toBeTruthy();
      expect(screen.getByRole("button", { name: de.closeFolders })).toBeTruthy();
      const html = document.body.innerHTML;
      for (const s of ["Open navigation", "All folders", "Main navigation", "Log out", "Folders", "Profile", "User (", "Keyboard shortcuts", "Switch to"]) expect(html, s).not.toContain(s);
    } finally {
      applySettings({ language: "en", locale: "en" });
    }
  });
});

describe("desktop navigation (lg and up)", () => {
  beforeEach(() => screenWidth(true));

  it("shows the SideNav as a rail and the folder column, with no overlays", async () => {
    await renderApp();
    const nav = sideNav();
    expect(nav.className).toContain("cds--side-nav--rail");
    expect(nav.hasAttribute("inert")).toBe(false);
    // The tree is a column of the page, not a dialog.
    expect(screen.getByRole("navigation", { name: t.accountsAndFolders }).closest("[role=dialog]")).toBeNull();
    expect(screen.getByRole("separator", { name: t.resizeSidebar })).toBeTruthy();
  });

  it("expands the rail on focus and collapses it again when focus leaves", async () => {
    await renderApp();
    const nav = sideNav();
    const link = within(nav).getByRole("link", { name: t.log });
    // Carbon's rail links stay in the tab order.
    expect(link.tabIndex).toBe(0);
    fireEvent.focus(link);
    await waitFor(() => expect(nav.className).toContain("cds--side-nav--expanded"));
    fireEvent.blur(link, { relatedTarget: document.body });
    await waitFor(() => expect(nav.className).not.toContain("cds--side-nav--expanded"));
  });

  /** matchMedia whose lg and two-pane answers can change while the app runs; the change reaches every listener of either query. */
  function liveWidth(wide: boolean) {
    const state = { wide };
    const listeners: { query: string; fn: (e: { matches: boolean; media: string }) => void }[] = [];
    const matches = (query: string) => state.wide && (query === lg || query === twoPaneQuery);
    window.matchMedia = (query: string) =>
      ({
        get matches() {
          return matches(query);
        },
        media: query,
        onchange: null,
        addEventListener: (_: string, fn: () => void) => listeners.push({ query, fn }),
        removeEventListener: (_: string, fn: () => void) => {
          const at = listeners.findIndex((l) => l.fn === fn);
          if (at >= 0) listeners.splice(at, 1);
        },
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
      }) as unknown as MediaQueryList;
    return (next: boolean) =>
      act(() => {
        state.wide = next;
        listeners.slice().forEach(({ query, fn }) => fn({ matches: matches(query), media: query }));
      });
  }

  it("drops the rail's focus expansion when the window narrows below lg, instead of leaving an inert overlay (#258)", async () => {
    const resize = liveWidth(true);
    await renderApp();
    const link = within(sideNav()).getByRole("link", { name: t.log });
    fireEvent.focus(link);
    // Carbon expands after a short delay: wait for it, so the resize below meets an expanded rail.
    await waitFor(() => expect(sideNav().className).toContain("cds--side-nav--expanded"));
    expect(sideNav().className).toContain("cds--side-nav--rail");
    resize(false);
    const nav = sideNav();
    expect(nav.className).not.toContain("cds--side-nav--rail");
    expect(nav.className).not.toContain("cds--side-nav--expanded");
    expect(nav.hasAttribute("inert")).toBe(true);
    expect(document.querySelector(".cds--side-nav__overlay-active")).toBeNull();
    // The overlay works as usual afterwards: the menu button opens it, Escape closes it and returns the focus.
    menuButton().focus();
    fireEvent.click(menuButton());
    expect(sideNav().className).toContain("cds--side-nav--expanded");
    expect(document.querySelector(".cds--side-nav__overlay-active")).not.toBeNull();
    key(document.activeElement!, "Escape");
    expect(sideNav().className).not.toContain("cds--side-nav--expanded");
    expect(document.querySelector(".cds--side-nav__overlay-active")).toBeNull();
    expect(document.activeElement).toBe(menuButton());
  });

  it("keeps the shortcuts working while a folder has focus", async () => {
    await renderApp();
    const node = screen.getByRole("treeitem", { name: "INBOX 5" });
    node.focus();
    act(() => {
      key(node, "/");
    });
    expect(document.activeElement).toBe(screen.getByRole("searchbox", { name: t.searchMail }));
    node.focus();
    key(node, "g");
    key(node, "a");
    expect(window.location.search).toBe("");
  });

  it("ignores the shortcuts while typing in the search field", async () => {
    window.history.replaceState(null, "", "/?account=work");
    await renderApp();
    const search = screen.getByRole("searchbox", { name: t.searchMail });
    search.focus();
    key(search, "g");
    key(search, "a");
    expect(window.location.search).toBe("?account=work");
  });
});

describe("keyboard shortcuts in the mail view", () => {
  beforeEach(() => {
    screenWidth(true);
    listed = [summary("m1", "First"), summary("m2", "Second")];
  });

  const searchbox = () => screen.getByRole("searchbox", { name: t.searchMail }) as HTMLInputElement;
  const shortcutsDialog = () => screen.queryByRole("dialog", { name: t.shortcuts });
  const listReady = () => screen.findByRole("button", { name: /First/ });
  const params = () => new URLSearchParams(window.location.search);

  it("opens the shortcuts as a passive Carbon Modal; the shortcuts are off while it is open", async () => {
    await renderApp();
    await listReady();
    const node = screen.getByRole("treeitem", { name: "INBOX 5" });
    node.focus();
    key(node, "?");
    const dialog = shortcutsDialog()!;
    expect(dialog.closest(".cds--modal")?.className).toContain("is-visible");
    // Passive: no buttons but its close button.
    expect(within(dialog).getAllByRole("button").map((b) => b.getAttribute("aria-label"))).toEqual([t.carbon.closeModal]);
    expect(within(dialog).getByText(t.keyNext)).toBeTruthy();
    // Focus is in the Modal (Carbon moves it there).
    expect(dialog.contains(document.activeElement)).toBe(true);
    key(document.activeElement!, "j");
    expect(params().get("m")).toBeNull();
    key(document.activeElement!, "Escape");
    await waitFor(() => expect(shortcutsDialog()).toBeNull());
    expect(document.activeElement).toBe(node);
  });

  it("ignores ?, j and k while the search field has focus", async () => {
    window.history.replaceState(null, "", "/?m=m1");
    await renderApp();
    await listReady();
    const search = searchbox();
    search.focus();
    key(search, "?");
    expect(shortcutsDialog()).toBeNull();
    key(search, "j");
    key(search, "k");
    expect(params().get("m")).toBe("m1");
    // The same keys outside a field work.
    const node = screen.getByRole("treeitem", { name: "INBOX 5" });
    node.focus();
    key(node, "j");
    expect(params().get("m")).toBe("m2");
    key(node, "k");
    expect(params().get("m")).toBe("m1");
  });

  it("clears the search with Escape without leaving the open message", async () => {
    window.history.replaceState(null, "", "/?m=m1");
    await renderApp();
    await screen.findByRole("article");
    const search = searchbox();
    search.focus();
    fireEvent.change(search, { target: { value: "invoice" } });
    expect(search.value).toBe("invoice");
    key(search, "Escape");
    expect(search.value).toBe("");
    expect(document.activeElement).toBe(search);
    expect(params().get("m")).toBe("m1");
    expect(screen.getByRole("article")).toBeTruthy();
    // In the empty field Escape leaves it; then Escape closes the message.
    key(search, "Escape");
    expect(document.activeElement).not.toBe(search);
    expect(params().get("m")).toBe("m1");
    key(document.body, "Escape");
    expect(params().get("m")).toBeNull();
  });

  it("leaves Escape to the SideNav rail and to an open tooltip: the message stays open", async () => {
    window.history.replaceState(null, "", "/?m=m1");
    await renderApp();
    await listReady();
    await screen.findByRole("article");
    const link = within(sideNav()).getByRole("link", { name: t.log });
    link.focus();
    key(link, "Escape");
    expect(params().get("m")).toBe("m1");
    // An icon button shows its tooltip on keyboard focus; the first Escape closes it.
    const pager = screen.getByRole("button", { name: t.nextMessage });
    act(() => pager.focus());
    await waitFor(() => expect(document.querySelector(".cds--popover--open")).not.toBeNull());
    key(pager, "Escape");
    expect(params().get("m")).toBe("m1");
    await waitFor(() => expect(document.querySelector(".cds--popover--open")).toBeNull());
    key(pager, "Escape");
    expect(params().get("m")).toBeNull();
  });

  it("toggles the filter popover with the filter button and shows the set filters as tags", async () => {
    window.history.replaceState(null, "", "/?q=from%3Asam%20invoice");
    await renderApp();
    const button = screen.getByRole("button", { name: t.filters });
    // Closed to start with; the button tells it as expanded, not as pressed (#311).
    expect(button.getAttribute("aria-expanded")).toBe("false");
    expect(button.hasAttribute("aria-pressed")).toBe(false);
    expect(screen.queryByRole("group", { name: t.filters })).toBeNull();
    const tags = screen.getByRole("list", { name: t.activeFilters });
    fireEvent.click(button);
    expect(button.getAttribute("aria-expanded")).toBe("true");
    expect(button.hasAttribute("aria-pressed")).toBe(false);
    const panel = await screen.findByRole("group", { name: t.filters });
    // aria-controls names the popover, which holds the form.
    const content = document.getElementById(button.getAttribute("aria-controls")!);
    expect(content?.className).toBe("cds--popover");
    expect(content?.contains(panel)).toBe(true);
    expect((within(panel).getByRole("textbox", { name: t.from }) as HTMLInputElement).value).toBe("sam");
    fireEvent.click(within(tags).getByRole("button", { name: t.removeFilter(t.from) }));
    expect(searchbox().value).toBe("invoice");
    expect(screen.queryByRole("list", { name: t.activeFilters })).toBeNull();
    fireEvent.click(button);
    expect(screen.queryByRole("group", { name: t.filters })).toBeNull();
  });

  it("speaks German in the list, the search, the filters and the shortcuts", async () => {
    applySettings({ language: "de", locale: "de" });
    window.history.replaceState(null, "", "/?q=has%3Aattachment&group=1");
    try {
      const de = messagesFor("de");
      await renderApp();
      await screen.findByRole("button", { name: /First/ });
      expect(screen.getByRole("searchbox", { name: de.searchMail })).toBeTruthy();
      expect(screen.getByRole("button", { name: de.carbon.clearSearch })).toBeTruthy();
      fireEvent.click(screen.getByRole("button", { name: de.filters }));
      expect(await screen.findByRole("group", { name: de.filters })).toBeTruthy();
      expect(screen.getByRole("button", { name: de.removeFilter(de.hasAttachment) })).toBeTruthy();
      fireEvent.click(screen.getByRole("button", { name: de.userPanelOf("alice") }));
      fireEvent.click(screen.getByRole("button", { name: de.shortcuts }));
      expect(screen.getByRole("dialog", { name: de.shortcuts })).toBeTruthy();
      expect(screen.getByRole("button", { name: de.carbon.closeModal })).toBeTruthy();
      const html = document.body.innerHTML;
      for (const s of ["Clear search input", "Clear the search field", "Dismiss", "Close", "Filters", "Search mail", "Keyboard shortcuts", "Next message", "Messages", "Group by conversation", "Newest first"]) {
        expect(html, s).not.toContain(s);
      }
      expect(html).not.toContain('placeholder="Search"');
    } finally {
      applySettings({ language: "en", locale: "en" });
    }
  });
});

describe("keyboard shortcuts while the SideNav overlay is open (below lg)", () => {
  beforeEach(() => {
    screenWidth(false);
    listed = [summary("m1", "First"), summary("m2", "Second")];
  });

  it("does nothing but close it on Escape", async () => {
    await renderApp();
    await screen.findByRole("button", { name: /First/ });
    menuButton().focus();
    fireEvent.click(menuButton());
    const link = within(sideNav()).getByRole("link", { name: t.allMail });
    expect(document.activeElement).toBe(link);
    for (const k of ["?", "j", "/", "g", "a"]) key(link, k);
    expect(screen.queryByRole("dialog", { name: t.shortcuts })).toBeNull();
    expect(window.location.search).toBe("");
    expect(document.activeElement).toBe(link);
    key(link, "Escape");
    expect(sideNav().className).not.toContain("cds--side-nav--expanded");
  });
});

describe("user panel (#284)", () => {
  beforeEach(() => {
    screenWidth(true);
    listed = [summary("m1", "First"), summary("m2", "Second")];
  });

  const action = () => screen.getByRole("button", { name: t.userPanelOf("alice") });
  const panel = () => screen.queryByRole("list", { name: t.userPanelOf("alice") });
  const items = () => Array.from(panel()!.querySelectorAll<HTMLElement>(".cds--switcher__item-link"));
  const item = (name: string) => items().find((el) => el.textContent === name)!;
  const params = () => new URLSearchParams(window.location.search);
  const open = () => {
    act(() => action().focus());
    fireEvent.click(action());
    expect(panel()).not.toBeNull();
  };

  for (const wide of [false, true]) {
    it(`leaves the header the menu button, the name and one user action (${wide ? "desktop" : "phone"}); closed, the panel holds nothing`, async () => {
      screenWidth(wide);
      await renderApp();
      const header = screen.getByRole("banner");
      // Carbon hides the menu button from lg up with CSS; it is in the DOM at both widths.
      expect(within(header).getAllByRole("button").map((b) => b.getAttribute("aria-label"))).toEqual([t.showMenu, t.userPanelOf("alice")]);
      expect(within(header).getAllByRole("link").map((l) => l.textContent)).toEqual([t.appName]);
      // The name is no text of its own there (the action's tooltip names it).
      expect(within(header).queryByText("alice")).toBeNull();
      expect(panel()).toBeNull();
      expect(header.querySelectorAll(".cds--switcher__item-link")).toHaveLength(0);
      expect(action().getAttribute("aria-expanded")).toBe("false");
    });
  }

  it("opens on a click with the focus on Profile: name, Profile, Keyboard shortcuts, theme, Log out, each named by its text and in the tab order", async () => {
    await renderApp();
    open();
    expect(action().getAttribute("aria-expanded")).toBe("true");
    expect(document.getElementById(action().getAttribute("aria-controls")!)?.contains(panel())).toBe(true);
    expect(document.activeElement).toBe(item(t.profile));
    expect(Array.from(panel()!.children).map((li) => li.textContent)).toEqual(["alice", t.profile, t.shortcuts, t.switchTheme("dark"), "", t.logOut]);
    for (const el of items()) {
      expect(el.getAttribute("aria-label")).toBe(el.textContent);
      expect(el.tabIndex).toBe(0);
    }
    expect(item(t.profile).getAttribute("href")).toBe("/?view=profile");
  });

  it("closes on Escape, also with the action's tooltip open, focuses the action and leaves the message open; the other shortcuts are off meanwhile", async () => {
    window.history.replaceState(null, "", "/?m=m1");
    await renderApp();
    await screen.findByRole("article");
    open();
    const profile = item(t.profile);
    for (const k of ["j", "k", "?", "/", "u"]) key(profile, k);
    expect(params().get("m")).toBe("m1");
    expect(screen.queryByRole("dialog", { name: t.shortcuts })).toBeNull();
    expect(document.activeElement).toBe(profile);
    // The pointer still over the action: its tooltip is open, which would claim the Escape (ownedByWidget).
    fireEvent.mouseEnter(action());
    await waitFor(() => expect(document.querySelector(".cds--popover--open")).not.toBeNull());
    key(profile, "Escape");
    expect(panel()).toBeNull();
    expect(action().getAttribute("aria-expanded")).toBe("false");
    expect(document.activeElement).toBe(action());
    expect(params().get("m")).toBe("m1");
  });

  it("keeps the panel open when a click blurs the focus first (WebKit, Firefox on macOS): Log out still works", async () => {
    const logout = vi.fn();
    await renderApp(false, logout);
    open();
    // Those browsers focus nothing on mousedown: the focus leaves the item to nowhere before the click.
    fireEvent.focusOut(item(t.profile), { relatedTarget: null });
    expect(panel()).not.toBeNull();
    fireEvent.click(item(t.logOut));
    expect(logout).toHaveBeenCalledTimes(1);
  });

  it("stays open when the focus goes to its action, and closes when Tab takes it elsewhere, leaving the focus there", async () => {
    await renderApp();
    open();
    act(() => action().focus());
    expect(panel()).not.toBeNull();
    act(() => item(t.logOut).focus());
    const search = screen.getByRole("searchbox", { name: t.searchMail });
    act(() => search.focus());
    expect(panel()).toBeNull();
    expect(document.activeElement).toBe(search);
  });

  it("closes when Shift+Tab takes the focus back past its action, and the shortcuts work again", async () => {
    await renderApp();
    await screen.findByRole("button", { name: /First/ });
    open();
    // Shift+Tab from Profile: to the action (the panel stays open), then to the name link before it.
    act(() => action().focus());
    expect(panel()).not.toBeNull();
    const name = within(screen.getByRole("banner")).getByRole("link", { name: t.appName });
    act(() => name.focus());
    expect(panel()).toBeNull();
    expect(action().getAttribute("aria-expanded")).toBe("false");
    expect(document.activeElement).toBe(name);
    key(name, "j");
    expect(params().get("m")).toBe("m1");
  });

  it("opens Profile on a plain click and returns the focus to the action; leaves a Ctrl click to the browser", async () => {
    await renderApp();
    open();
    expect(fireEvent.click(item(t.profile), { ctrlKey: true })).toBe(true);
    expect(params().get("view")).toBeNull();
    expect(panel()).not.toBeNull();
    expect(fireEvent.click(item(t.profile))).toBe(false);
    expect(params().get("view")).toBe("profile");
    expect(panel()).toBeNull();
    expect(document.activeElement).toBe(action());
  });

  it("opens the shortcuts; closing them returns the focus to the action", async () => {
    await renderApp();
    open();
    fireEvent.click(item(t.shortcuts));
    expect(panel()).toBeNull();
    const dialog = await screen.findByRole("dialog", { name: t.shortcuts });
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
    key(document.activeElement!, "Escape");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: t.shortcuts })).toBeNull());
    expect(document.activeElement).toBe(action());
  });

  it("switches the theme and stays open; the item's text and name follow", async () => {
    await renderApp();
    open();
    fireEvent.click(item(t.switchTheme("dark")));
    expect(document.documentElement.classList.contains("cds--g100")).toBe(true);
    expect(panel()).not.toBeNull();
    const theme = items()[2]!;
    expect(theme.textContent).toBe(t.switchTheme("light"));
    expect(theme.getAttribute("aria-label")).toBe(t.switchTheme("light"));
    fireEvent.click(theme);
    expect(document.documentElement.classList.contains("cds--white")).toBe(true);
    document.documentElement.className = "";
  });

  it("logs out", async () => {
    const logout = vi.fn();
    await renderApp(false, logout);
    open();
    fireEvent.click(item(t.logOut));
    expect(logout).toHaveBeenCalledTimes(1);
  });

  it("closes on Log out, with the focus back on its action, so a failure alert is not covered (#327)", async () => {
    const logout = vi.fn();
    await renderApp(false, logout);
    open();
    fireEvent.click(item(t.logOut));
    expect(logout).toHaveBeenCalledTimes(1);
    expect(panel()).toBeNull();
    expect(action().getAttribute("aria-expanded")).toBe("false");
    expect(document.activeElement).toBe(action());
  });

  it("shows a failed Log out as an alert in the banner slot under the header (#327)", async () => {
    await renderApp(false, () => {}, t.logoutFailed("500 Internal Server Error"));
    // HealthBanner's own live region is an (empty) alert too.
    const alerts = screen.getAllByRole("alert").filter((el) => el.textContent?.includes(t.logoutFailed("500 Internal Server Error")));
    expect(alerts).toHaveLength(1);
    const alert = alerts[0]!;
    expect(alert.classList.contains("cds--inline-notification")).toBe(true);
    // The slot is the first child of the shell body, above the main view.
    const slot = document.querySelector(".app-shell__body")!.firstElementChild!;
    expect(slot.contains(alert)).toBe(true);
  });

  it("shows no logout alert without a logout error", async () => {
    await renderApp();
    expect(screen.getAllByRole("alert").filter((el) => el.textContent)).toHaveLength(0);
    expect(document.querySelector(".app-shell__body .cds--inline-notification")).toBeNull();
  });

  it("closes on a click outside and toggles with its action", async () => {
    await renderApp();
    open();
    fireEvent.click(screen.getByRole("searchbox", { name: t.searchMail }));
    expect(panel()).toBeNull();
    expect(action().getAttribute("aria-expanded")).toBe("false");
    open();
    fireEvent.click(action());
    expect(panel()).toBeNull();
    expect(document.activeElement).toBe(action());
  });

  it("moves between the items with the arrow keys", async () => {
    await renderApp();
    open();
    key(item(t.profile), "ArrowDown");
    expect(document.activeElement).toBe(item(t.shortcuts));
    key(item(t.shortcuts), "ArrowDown");
    expect(document.activeElement).toBe(item(t.switchTheme("dark")));
    key(item(t.switchTheme("dark")), "ArrowDown");
    expect(document.activeElement).toBe(item(t.logOut));
    key(item(t.logOut), "ArrowUp");
    expect(document.activeElement).toBe(item(t.switchTheme("dark")));
  });

  it("takes over from the SideNav and hands back to it, with the focus in the one that is open (below lg)", async () => {
    screenWidth(false);
    await renderApp();
    menuButton().focus();
    fireEvent.click(menuButton());
    expect(document.activeElement).toBe(within(sideNav()).getByRole("link", { name: t.allMail }));
    fireEvent.click(action());
    expect(screen.getByRole("button", { name: t.showMenu }).getAttribute("aria-expanded")).toBe("false");
    expect(sideNav().className).not.toContain("cds--side-nav--expanded");
    expect(panel()).not.toBeNull();
    expect(document.activeElement).toBe(item(t.profile));
    // As in WebKit, the click focuses nothing: no focus-out from the panel to another element closes it.
    act(() => item(t.profile).blur());
    expect(panel()).not.toBeNull();
    fireEvent.click(menuButton());
    expect(panel()).toBeNull();
    expect(sideNav().className).toContain("cds--side-nav--expanded");
    expect(document.activeElement).toBe(within(sideNav()).getByRole("link", { name: t.allMail }));
  });

  it("closes when the focus moves into the mail frame, not when the window blurs with the focus on a panel item (#349)", async () => {
    await renderApp();
    open();
    act(() => item(t.profile).focus());
    act(() => void window.dispatchEvent(new Event("blur")));
    expect(panel()).not.toBeNull();
    intoFrame();
    expect(panel()).toBeNull();
    expect(action().getAttribute("aria-expanded")).toBe("false");
  });
});

describe("the filter popover (#286)", () => {
  const panel = () => screen.queryByRole("group", { name: t.filters });
  const button = () => screen.getByRole("button", { name: t.filters });
  const open = async () => {
    fireEvent.click(button());
    return screen.findByRole("group", { name: t.filters }, { timeout: 10000 });
  };
  const since = () => screen.getByLabelText(filterTextFor("en").since) as HTMLInputElement & { _flatpickr: { isOpen: boolean } };

  it.each([
    ["a phone", false, false],
    ["two panes below lg", false, true],
    ["a desktop (lg)", true, true],
  ])("starts closed on %s, and the button opens it", async (_, wide, twoPane) => {
    screenWidth(wide, twoPane);
    await renderApp();
    expect(panel()).toBeNull();
    expect(button().getAttribute("aria-expanded")).toBe("false");
    await open();
    expect(button().getAttribute("aria-expanded")).toBe("true");
  });

  it("closes on Escape from the filter button, and on an outside click", async () => {
    screenWidth(true);
    await renderApp();
    await open();
    act(() => button().focus());
    key(button(), "Escape");
    expect(panel()).toBeNull();
    expect(button().getAttribute("aria-expanded")).toBe("false");
    // Escape on the closed popover's button is the app's again (no message open: nothing).
    await open();
    fireEvent.click(document.body);
    expect(panel()).toBeNull();
  });

  it("ignores close requests while the calendar is open: a click into a date field can end on the calendar", async () => {
    screenWidth(true);
    await renderApp();
    await open();
    act(() => since().focus());
    expect(since()._flatpickr.isOpen).toBe(true);
    // A real browser: mousedown on the field, mouseup on the calendar, which
    // overlaps the field's lower edge; the click then targets body.
    fireEvent.mouseDown(since());
    fireEvent.click(document.body);
    expect(since()._flatpickr.isOpen).toBe(true);
    expect(panel()).not.toBeNull();
  });

  it("closes on an outside click after the form was unmounted with its calendar open", async () => {
    screenWidth(true);
    await renderApp();
    await open();
    act(() => since().focus());
    expect(since()._flatpickr.isOpen).toBe(true);
    // Closed with the calendar still open (Carbon keeps it open for a click that started in the field).
    fireEvent.mouseDown(since());
    fireEvent.click(button());
    expect(panel()).toBeNull();
    await open();
    fireEvent.click(document.body);
    expect(panel()).toBeNull();
  });

  it("keeps the focus in a date field on Escape: the first closes the calendar, the next the popover", async () => {
    screenWidth(true);
    await renderApp();
    await open();
    act(() => since().focus());
    key(since(), "Escape");
    expect(since()._flatpickr.isOpen).toBe(false);
    expect(document.activeElement).toBe(since());
    expect(panel()).not.toBeNull();
    key(since(), "Escape");
    expect(panel()).toBeNull();
    expect(document.activeElement).toBe(button());
  });

  it("closes the user panel when Edit filters opens the popover", async () => {
    window.history.replaceState(null, "", "/?q=from%3Asam");
    screenWidth(true);
    await renderApp();
    const userAction = screen.getByRole("button", { name: t.userPanelOf("alice") });
    fireEvent.click(userAction);
    expect(userAction.getAttribute("aria-expanded")).toBe("true");
    fireEvent.click(screen.getByRole("button", { name: t.editFilters }));
    expect(userAction.getAttribute("aria-expanded")).toBe("false");
    const p = await screen.findByRole("group", { name: t.filters });
    await waitFor(() => expect(document.activeElement).toBe(within(p).getByRole("textbox", { name: t.from })));
  });

  it("closes when the focus moves into the mail frame (#349)", async () => {
    screenWidth(true);
    await renderApp();
    await open();
    intoFrame();
    expect(panel()).toBeNull();
    expect(button().getAttribute("aria-expanded")).toBe("false");
  });
});

describe("quick filters on phones and the Edit filters button (#196)", () => {
  beforeEach(() => {
    listed = [summary("m1", "First"), summary("m2", "Second")];
  });

  const panel = () => screen.queryByRole("group", { name: t.filters });
  const filtersButton = () => screen.getByRole("button", { name: t.filters });
  const quickGroup = () => screen.queryByRole("group", { name: t.quickFilters });
  const quick = (name: string) => within(quickGroup()!).getByRole("button", { name });
  const tagList = () => screen.queryByRole("list", { name: t.activeFilters });
  const editButton = () => screen.queryByRole("button", { name: t.editFilters });
  const params = () => new URLSearchParams(window.location.search);
  const listReady = () => screen.findByRole("button", { name: /First/ });

  /** matchMedia whose width can change while the app runs; changed() tells the listeners. */
  function resizable(twoPane: boolean) {
    const state = { twoPane };
    const listeners = new Set<(e: { matches: boolean; media: string }) => void>();
    window.matchMedia = (query: string) =>
      ({
        get matches() {
          return state.twoPane && query.includes("48rem");
        },
        media: query,
        onchange: null,
        // Only the two-pane query changes here.
        addEventListener: (_: string, l: () => void) => query.includes("48rem") && listeners.add(l),
        removeEventListener: (_: string, l: () => void) => listeners.delete(l),
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
      }) as unknown as MediaQueryList;
    return (next: boolean) =>
      act(() => {
        state.twoPane = next;
        listeners.forEach((l) => l({ matches: next, media: twoPaneQuery }));
      });
  }

  it("toggles the attachment and the grouping with one tap on a phone, through the URL, with no history entry", async () => {
    screenWidth(false, false);
    await renderApp();
    await listReady();
    expect(panel()).toBeNull();
    const entries = window.history.length;
    expect(quick(t.hasAttachment).getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(quick(t.hasAttachment));
    expect(quick(t.hasAttachment).getAttribute("aria-pressed")).toBe("true");
    await waitFor(() => expect(params().get("q")).toBe("has:attachment"));
    fireEvent.click(quick(t.groupByConversation));
    expect(params().get("group")).toBe("1");
    expect(quick(t.groupByConversation).getAttribute("aria-pressed")).toBe("true");
    // The pressed quick filters are the state; no tag repeats them.
    expect(tagList()).toBeNull();
    expect(editButton()).toBeNull();
    expect(panel()).toBeNull();
    fireEvent.click(quick(t.hasAttachment));
    await waitFor(() => expect(params().get("q")).toBeNull());
    fireEvent.click(quick(t.groupByConversation));
    expect(params().get("group")).toBeNull();
    expect(window.history.length).toBe(entries);
  });

  it("has no quick filters from 48rem; the attachment and the grouping are tags there", async () => {
    window.history.replaceState(null, "", "/?q=has%3Aattachment&group=1");
    screenWidth(false, true);
    await renderApp();
    expect(quickGroup()).toBeNull();
    expect(within(tagList()!).getByRole("button", { name: t.removeFilter(t.hasAttachment) })).toBeTruthy();
    expect(within(tagList()!).getByRole("button", { name: t.removeFilter(t.conversation) })).toBeTruthy();
  });

  it("swaps the quick filters and the tags when the width changes, without a reload", async () => {
    window.history.replaceState(null, "", "/?q=has%3Aattachment&group=1");
    const resize = resizable(false);
    await renderApp();
    expect(quickGroup()).not.toBeNull();
    expect(tagList()).toBeNull();
    resize(true);
    expect(quickGroup()).toBeNull();
    expect(within(tagList()!).getByRole("button", { name: t.removeFilter(t.hasAttachment) })).toBeTruthy();
    resize(false);
    expect(quickGroup()).not.toBeNull();
    expect(tagList()).toBeNull();
  });

  it("opens the closed popover with Edit filters and focuses the first tag's field once the form loaded; j types there and Escape closes it", async () => {
    window.history.replaceState(null, "", "/?q=from%3Asam%20after%3A2026-01-01");
    screenWidth(false, false);
    await renderApp();
    await listReady();
    // First j works outside a field (the app has the list's ids), and u goes back.
    await waitFor(() => {
      key(document.body, "j");
      expect(params().get("m")).toBe("m1");
    });
    key(document.body, "u");
    expect(params().get("m")).toBeNull();
    expect(panel()).toBeNull();
    fireEvent.click(editButton()!);
    expect(filtersButton().getAttribute("aria-expanded")).toBe("true");
    const from = within(await screen.findByRole("group", { name: t.filters })).getByRole("textbox", { name: t.from });
    await waitFor(() => expect(document.activeElement).toBe(from));
    // A field owns its keys: j does not open a message.
    key(from, "j");
    expect(params().get("m")).toBeNull();
    // Escape in a field of the popover closes it; the focus goes to the filter button.
    key(from, "Escape");
    expect(panel()).toBeNull();
    expect(document.activeElement).toBe(filtersButton());
    // Edit filters again opens and focuses again.
    fireEvent.click(editButton()!);
    const again = within(await screen.findByRole("group", { name: t.filters })).getByRole("textbox", { name: t.from });
    await waitFor(() => expect(document.activeElement).toBe(again));
    // A second press with the popover open focuses again (focus elsewhere in it first: a blur to nowhere closes it).
    const to = within(panel()!).getByRole("textbox", { name: t.toOrCc });
    act(() => to.focus());
    fireEvent.click(editButton()!);
    expect(document.activeElement).toBe(again);
    // Closing and reopening the popover with its button takes no focus.
    fireEvent.click(filtersButton());
    expect(panel()).toBeNull();
    fireEvent.click(filtersButton());
    expect(await screen.findByRole("group", { name: t.filters })).toBeTruthy();
    expect(document.activeElement).toBe(document.body);
  });

  it("focuses Until or the checkbox by the rule, also when the popover is already open", async () => {
    window.history.replaceState(null, "", "/?q=before%3A2026-02-01");
    screenWidth(true);
    await renderApp();
    fireEvent.click(filtersButton());
    const p = await screen.findByRole("group", { name: t.filters });
    fireEvent.click(editButton()!);
    expect(document.activeElement).toBe(within(p).getByLabelText(filterTextFor("en").until));
    expect(filtersButton().getAttribute("aria-expanded")).toBe("true");
    cleanup();
    window.history.replaceState(null, "", "/?q=has%3Aattachment");
    await renderApp();
    fireEvent.click(editButton()!);
    const checkbox = within(await screen.findByRole("group", { name: t.filters })).getByRole("checkbox", { name: t.hasAttachment });
    await waitFor(() => expect(document.activeElement).toBe(checkbox));
  });

  it("still removes exactly one filter with a tag's ×, and leaves the panel as it was", async () => {
    window.history.replaceState(null, "", "/?q=from%3Asam%20to%3Aoffice");
    screenWidth(false, false);
    await renderApp();
    fireEvent.click(within(tagList()!).getByRole("button", { name: t.removeFilter(t.from) }));
    await waitFor(() => expect(params().get("q")).toBe("to:office"));
    expect(panel()).toBeNull();
    expect(within(tagList()!).getByRole("button", { name: t.removeFilter(t.toOrCc) })).toBeTruthy();
  });

  it("keeps j working on a focused quick filter: it opens the first message", async () => {
    screenWidth(false, false);
    await renderApp();
    await listReady();
    const tag = quick(t.hasAttachment);
    act(() => tag.focus());
    // The list hands its ids to the app after it renders; until then j has
    // nothing to open, so it is pressed until the app has them.
    await waitFor(() => {
      key(tag, "j");
      expect(params().get("m")).toBe("m1");
    });
  });

  it("speaks German in the quick filters and the tag row", async () => {
    applySettings({ language: "de", locale: "de" });
    window.history.replaceState(null, "", "/?q=from%3Asam&group=1");
    screenWidth(false, false);
    try {
      const de = messagesFor("de");
      await renderApp();
      const group = screen.getByRole("group", { name: de.quickFilters });
      expect(within(group).getByRole("button", { name: de.hasAttachment })).toBeTruthy();
      expect(within(group).getByRole("button", { name: de.groupByConversation }).getAttribute("aria-pressed")).toBe("true");
      const list = screen.getByRole("list", { name: de.activeFilters });
      expect(within(list).getByRole("button", { name: de.editFilters })).toBeTruthy();
      expect(within(list).getByRole("button", { name: de.removeFilter(de.from) })).toBeTruthy();
      const html = document.body.innerHTML;
      for (const s of ["Quick filters", "Edit filters", "Remove filter", "Has attachment", "Group by conversation"]) expect(html, s).not.toContain(s);
    } finally {
      applySettings({ language: "en", locale: "en" });
    }
  });
});

describe("keyboard shortcuts on the admin pages (#168 part 5)", () => {
  const shortcutsDialog = () => screen.queryByRole("dialog", { name: t.shortcuts });

  beforeEach(() => {
    screenWidth(true);
    const base = fetch as unknown as (path: string) => Promise<Response>;
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { "Content-Type": "application/json" } });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (path: string) => {
        if (path === "/api/users") {
          return json({ users: [{ name: "bob", admin: false, locked: false, mustChangePassword: false, twoFactorEnabled: false, passkeys: 0, apiKeys: 0, accounts: 0, failingAccounts: 0, createdAt: "2026-10-01T00:00:00Z", lastLoginAt: null, self: false }] });
        }
        if (path === "/api/api-keys") return json({ keys: [] });
        if (path === "/api/retention/rules") return json({ rules: [], accounts: [], minYears: 6, defaultTimeZone: "Europe/Berlin" });
        if (path.startsWith("/api/audit?")) return json({ entries: [] });
        return base(path);
      }),
    );
  });

  it("leaves the keys to the actions menu and to the danger Modal on Users", async () => {
    window.history.replaceState(null, "", "/?view=users");
    render(<App user={user(true)} logout={() => {}} logoutError="" />);
    fireEvent.click(await screen.findByRole("button", { name: t.userActions("bob") }));
    const item = screen.getByRole("menuitem", { name: t.removeUser });
    key(item, "?");
    expect(shortcutsDialog()).toBeNull();
    fireEvent.click(item);
    const dialog = screen.getByRole("dialog", { name: t.removeUserTitle("bob") });
    key(dialog, "?");
    key(dialog, "g");
    key(dialog, "a");
    expect(shortcutsDialog()).toBeNull();
    expect(window.location.search).toBe("?view=users");
    // Outside a widget the shortcuts still work on this page.
    fireEvent.click(within(dialog).getByRole("button", { name: t.cancel }));
    key(document.body, "?");
    expect(shortcutsDialog()).not.toBeNull();
  });

  it("leaves the keys to the fields of Retention", async () => {
    window.history.replaceState(null, "", "/?view=retention");
    render(<App user={user(true)} logout={() => {}} logoutError="" />);
    // The rule form is on its own page (#285 part 3), opened from the Rules tab.
    fireEvent.click(await screen.findByRole("button", { name: t.ruleAdd }));
    expect(window.location.search).toBe("?view=retention&add=rule");
    const years = await screen.findByLabelText(t.ruleYears);
    years.focus();
    key(years, "?");
    expect(shortcutsDialog()).toBeNull();
  });
  it("leaves a select on Escape, as a text field (#311)", async () => {
    window.history.replaceState(null, "", "/?view=log");
    render(<App user={user(true)} logout={() => {}} logoutError="" />);
    const select = await screen.findByLabelText(t.logAction);
    expect(select.tagName).toBe("SELECT");
    select.focus();
    // A key of the select stays its own.
    key(select, "?");
    expect(shortcutsDialog()).toBeNull();
    expect(document.activeElement).toBe(select);
    key(select, "Escape");
    expect(document.activeElement).not.toBe(select);
    expect(window.location.search).toBe("?view=log");
  });
});

describe("the focus when a message opens and closes (#311)", () => {
  beforeEach(() => {
    listed = [summary("m1", "First"), summary("m2", "Second")];
  });

  const row = (id: string) => document.querySelector<HTMLElement>(`button[data-id="${id}"]`);
  const subject = (id: string) => screen.findByRole("heading", { level: 1, name: `Subject ${id}` });
  const params = () => new URLSearchParams(window.location.search);
  const placeTitle = () => document.querySelector<HTMLElement>(".mail-place__title");
  const openRow = async (id: string) => {
    await screen.findByRole("button", { name: /First/ });
    const r = row(id)!;
    act(() => r.focus());
    fireEvent.click(r);
    return r;
  };

  describe("on phones", () => {
    beforeEach(() => screenWidth(false));

    it("focuses the subject when a row opens and the row again on Back to the list", async () => {
      await renderApp();
      const r = await openRow("m1");
      const h1 = await subject("m1");
      await waitFor(() => expect(document.activeElement).toBe(h1));
      fireEvent.click(screen.getByRole("button", { name: t.backToList }));
      expect(params().get("m")).toBeNull();
      expect(document.activeElement).toBe(r);
    });

    it("focuses the row again after the browser's Back", async () => {
      await renderApp();
      const r = await openRow("m2");
      const h1 = await subject("m2");
      await waitFor(() => expect(document.activeElement).toBe(h1));
      act(() => window.history.back());
      await waitFor(() => expect(params().get("m")).toBeNull());
      await waitFor(() => expect(document.activeElement).toBe(r));
    });

    it("focuses the list's heading on Back when the message is not in the list", async () => {
      window.history.replaceState(null, "", "/?m=zz");
      await renderApp();
      await screen.findByRole("button", { name: /First/ });
      await subject("zz");
      fireEvent.click(screen.getByRole("button", { name: t.backToList }));
      expect(params().get("m")).toBeNull();
      expect(document.activeElement).toBe(placeTitle());
      expect(placeTitle()!.tabIndex).toBe(-1);
    });

    it("leaves the focus alone when an action closes the message and opens another page (plan review on #311)", async () => {
      window.history.replaceState(null, "", "/?m=m1");
      await renderApp();
      await screen.findByRole("button", { name: /First/ });
      await subject("m1");
      // Accounts from the SideNav: the overlay returns the focus to the menu button, and it stays there.
      menuButton().focus();
      fireEvent.click(menuButton());
      fireEvent.click(within(sideNav()).getByRole("link", { name: t.navAccounts }));
      expect(params().get("view")).toBe("accounts");
      expect(document.activeElement).toBe(menuButton());
    });
  });

  describe("on desktop", () => {
    beforeEach(() => screenWidth(true));

    it("keeps the focus on a clicked row while the message shows", async () => {
      await renderApp();
      const r = await openRow("m1");
      await subject("m1");
      await screen.findByRole("article");
      expect(document.activeElement).toBe(r);
    });

    it("focuses the row when Escape closes the message from the reading pane", async () => {
      await renderApp();
      const r = await openRow("m2");
      await subject("m2");
      const region = screen.getByRole("region", { name: t.readingPane });
      act(() => region.focus());
      key(region, "Escape");
      expect(params().get("m")).toBeNull();
      expect(document.activeElement).toBe(r);
    });

    it("leaves the focus where the user put it when u closes the message", async () => {
      await renderApp();
      await openRow("m1");
      await subject("m1");
      const node = screen.getByRole("treeitem", { name: "INBOX 5" });
      act(() => node.focus());
      key(node, "u");
      expect(params().get("m")).toBeNull();
      expect(document.activeElement).toBe(node);
    });

    it("focuses the first row when the closed message is not in the list, and the search field when the list is empty", async () => {
      window.history.replaceState(null, "", "/?m=zz");
      await renderApp();
      await screen.findByRole("button", { name: /First/ });
      const h1 = await subject("zz");
      // A deep link: the focus was lost, so the subject took it.
      expect(document.activeElement).toBe(h1);
      key(h1, "u");
      expect(document.activeElement).toBe(row("m1"));
      cleanup();
      listed = [];
      window.history.replaceState(null, "", "/?m=zz");
      await renderApp();
      await screen.findByText(t.noMessages, { selector: ".empty-state__title" });
      key(await subject("zz"), "u");
      expect(document.activeElement).toBe(screen.getByRole("searchbox", { name: t.searchMail }));
    });

    it("moves the focus from the subject to the next message's subject with j", async () => {
      window.history.replaceState(null, "", "/?m=m1");
      await renderApp();
      await screen.findByRole("button", { name: /First/ });
      const h1 = await subject("m1");
      expect(document.activeElement).toBe(h1);
      await waitFor(() => {
        key(document.activeElement!, "j");
        expect(params().get("m")).toBe("m2");
      });
      const next = await subject("m2");
      await waitFor(() => expect(document.activeElement).toBe(next));
    });

    it("leaves the focus in the search field when a message opens by a URL change", async () => {
      await renderApp();
      await screen.findByRole("button", { name: /First/ });
      const search = screen.getByRole("searchbox", { name: t.searchMail });
      act(() => search.focus());
      act(() => {
        window.history.pushState(null, "", "/?m=m1");
        window.dispatchEvent(new PopStateEvent("popstate"));
      });
      await screen.findByRole("article");
      await subject("m1");
      expect(document.activeElement).toBe(search);
    });
  });
});
