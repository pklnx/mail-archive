// The shell's navigation in the app: the SideNav overlay and the folder
// overlay below lg, the rail and the folder column from lg up, and the
// keyboard shortcuts next to them. The API is a stub; no network.
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import type { Account, AccountsResponse, Folder, User } from "./api";
import { applySettings, messagesFor, t } from "./i18n";

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

/** matchMedia that answers the lg query (Carbon's 66rem) with wide. */
function screenWidth(wide: boolean) {
  window.matchMedia = (query: string) =>
    ({
      matches: wide && query.includes("66rem"),
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }) as MediaQueryList;
}

const original = window.matchMedia;
beforeEach(() => {
  window.history.replaceState(null, "", "/");
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      const body = path.startsWith("/api/accounts") ? accounts : path.startsWith("/api/messages") ? { messages: [], nextCursor: null } : {};
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

async function renderApp(admin = false) {
  render(<App user={user(admin)} logout={() => {}} />);
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
    expect(document.querySelector("dialog[open]")).toBeNull();
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
      expect(screen.getByRole("button", { name: de.profileOf("alice") })).toBeTruthy();
      expect(screen.getByRole("button", { name: de.logOut })).toBeTruthy();
      expect(screen.getByRole("button", { name: de.shortcuts })).toBeTruthy();
      fireEvent.click(screen.getByRole("button", { name: de.navFolders }));
      expect(screen.getByRole("dialog", { name: de.navFolders })).toBeTruthy();
      expect(screen.getByRole("button", { name: de.closeFolders })).toBeTruthy();
      const html = document.body.innerHTML;
      for (const s of ["Open navigation", "All folders", "Main navigation", "Log out", "Folders", "Profile"]) expect(html, s).not.toContain(s);
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
