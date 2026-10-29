// The Users page on Carbon (#168 part 5): the table, the actions menu, and
// the danger confirmations, each of which sends its request only after it
// was confirmed, and the same request as before. Each user has a detail page
// (#285 part 1) with the same actions and the user's API keys.
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ApiKey, ManagedUser } from "./api";
import { shortDate } from "./format";
import { applySettings, messagesFor, t } from "./i18n";
import { settingsTextFor } from "./settingsText";
import { expectLoaded, expectLoading } from "./test/loading";
import { UsersPage } from "./UsersPage";

function user(name: string, more: Partial<ManagedUser> = {}): ManagedUser {
  return {
    name, admin: false, locked: false, mustChangePassword: false, twoFactorEnabled: false, passkeys: 0, apiKeys: 0, accounts: 1,
    failingAccounts: 0, createdAt: "2026-10-01T00:00:00Z", lastLoginAt: null, self: false, ...more,
  };
}

const users = [
  user("alice", { admin: true, self: true, twoFactorEnabled: true, lastLoginAt: "2026-10-20T10:00:00Z" }),
  user("bob", { twoFactorEnabled: true, passkeys: 2, apiKeys: 1, failingAccounts: 1 }),
  user("carol", { locked: true }),
  user("dave", { mustChangePassword: true }),
];

const bobKey: ApiKey = {
  id: 7, name: "backup", hint: "wxyz", scopes: ["read"], createdAt: "2026-10-01T00:00:00Z", expiresAt: null, lastUsedAt: null,
  revokedAt: null, state: "active", user: "bob",
} as ApiKey;
const aliceKey: ApiKey = { ...bobKey, id: 9, name: "laptop", user: "alice" };
/** The keys whose DELETE was answered: listed as revoked from then on. */
let revoked: Set<number>;

let requests: { method: string; url: string; body: unknown }[];
/** The requests that change something: everything but the list reloads. */
const changes = () => requests.filter((r) => r.method !== "GET");

beforeEach(() => {
  window.history.replaceState(null, "", "/?view=users");
  requests = [];
  revoked = new Set();
  vi.spyOn(globalThis, "fetch").mockImplementation(async (url, init) => {
    const method = init?.method ?? "GET";
    requests.push({ method, url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined });
    const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
    if (method === "GET" && url === "/api/users") return json({ users });
    if (method === "GET" && url === "/api/api-keys") {
      return json({ keys: [bobKey, aliceKey].map((k) => (revoked.has(k.id) ? { ...k, state: "revoked" } : k)) });
    }
    const key = /^\/api\/api-keys\/(\d+)$/.exec(String(url));
    if (method === "DELETE" && key) revoked.add(Number(key[1]));
    if (method === "POST" && url === "/api/users") return json({ name: "erin", password: "Erin-Generated-1" }, 201);
    if (method === "POST" && String(url).endsWith("/password")) return json({ name: "bob", password: "Bob-Generated-2" });
    return new Response(null, { status: 204 });
  });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  window.history.replaceState(null, "", "/");
});

/** matchMedia that matches the queries in `on`. */
function stubMedia(on: string[]) {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: on.includes(query),
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  }));
}

/** Leaves GET /api/users pending; resolve answers it with the users. */
function pendingList() {
  let resolve!: () => void;
  const answered = new Promise<void>((r) => {
    resolve = r;
  });
  const fetch = vi.mocked(globalThis.fetch).getMockImplementation()!;
  vi.mocked(globalThis.fetch).mockImplementation(async (url, init) => {
    if ((init?.method ?? "GET") === "GET" && url === "/api/users") await answered;
    return fetch(url, init);
  });
  return resolve;
}

async function show() {
  render(<UsersPage />);
  await screen.findByText("bob");
}

/** Opens a user's page from the URL, as a reload or a bookmark does. */
async function showUser(name: string) {
  window.history.replaceState(null, "", `/?view=users&id=${name}`);
  render(<UsersPage />);
  await screen.findByRole("heading", { level: 1, name });
}

const h1 = (name: string) => screen.getByRole("heading", { level: 1, name });

const row = (name: string) => screen.getByText(name, { exact: true, selector: ".users-table__name" }).closest("tr")!;

/** Opens the actions menu of a user and picks an item. */
function pick(name: string, item: string) {
  fireEvent.click(screen.getByRole("button", { name: t.userActions(name) }));
  fireEvent.click(screen.getByRole("menuitem", { name: item }));
}

const dialog = () => screen.getByRole("dialog");

describe("UsersPage", () => {
  it("shows a table skeleton with the table's columns while the list loads, then the table (#287)", async () => {
    stubMedia(["(min-width: 42rem)"]);
    const resolve = pendingList();
    render(<UsersPage />);
    expect(h1(t.usersTitle)).toBeTruthy();
    const table = expectLoading()!.querySelector(".cds--data-table.cds--skeleton")!;
    expect([...table.querySelectorAll("th")].map((th) => th.textContent)).toEqual([
      t.colUser, t.colRole, t.colState, t.colSignIn, t.colAccounts, t.colLastLogin, "",
    ]);
    expect(table.querySelectorAll("tbody tr:first-child td")).toHaveLength(7);
    expect(table.className).toContain("cds--data-table--lg");
    // No toolbar: Carbon's has an English aria-label.
    expect(document.querySelector(".cds--table-toolbar")).toBeNull();
    await act(async () => resolve());
    await screen.findByText("bob");
    expectLoaded();
  });

  it("has only the columns a phone shows in the skeleton below 42rem", () => {
    stubMedia([]);
    pendingList();
    render(<UsersPage />);
    const table = expectLoading()!.querySelector(".cds--data-table.cds--skeleton")!;
    expect([...table.querySelectorAll("th")].map((th) => th.textContent)).toEqual([t.colUser, t.colState, ""]);
  });

  it("shows each user's role, state and sign-in; the own row has the hint and no actions", async () => {
    await show();
    const bob = within(row("bob"));
    expect(bob.getByText(t.roleUser)).toBeTruthy();
    expect(bob.getByText(t.stateActive)).toBeTruthy();
    expect(bob.getByText(t.twoFactorShortOn)).toBeTruthy();
    expect(bob.getByText(t.passkeyCount(2))).toBeTruthy();
    expect(bob.getByText(t.apiKeyCount(1))).toBeTruthy();
    // In the Accounts column, and in the second line under the name that
    // replaces the secondary columns below 42rem (layout.css shows one).
    expect(bob.getAllByText(t.failingCount(1))).toHaveLength(2);
    expect(bob.getByText(`${t.roleUser} · ${t.never}`).closest(".app-narrow-only")).toBeTruthy();
    const wide = screen.getAllByRole("columnheader").filter((h) => h.classList.contains("app-wide-only")).map((h) => h.textContent);
    expect(wide).toEqual([t.colRole, t.colSignIn, t.colAccounts, t.colLastLogin]);
    expect(within(row("carol")).getByText(t.stateLocked)).toBeTruthy();
    expect(within(row("dave")).getByText(t.stateMustChange)).toBeTruthy();
    const alice = within(row("alice"));
    expect(alice.getByText(t.roleAdmin)).toBeTruthy();
    expect(alice.getByText(t.selfHint)).toBeTruthy();
    expect(screen.queryByRole("button", { name: t.userActions("alice") })).toBeNull();
    expect(screen.getByRole("button", { name: t.userActions("bob") })).toBeTruthy();
  });

  it("has one row per user, with the name as its row header and a link to the user's page", async () => {
    await show();
    const headers = screen.getAllByRole("rowheader");
    expect(headers.map((h) => within(h).getByRole("link").textContent)).toEqual(["alice", "bob", "carol", "dave"]);
    expect(headers.every((h) => h.tagName === "TH" && h.getAttribute("scope") === "row")).toBe(true);
    const link = within(row("bob")).getByRole("link", { name: "bob" });
    expect(link.getAttribute("href")).toBe("?view=users&id=bob");
    // The row has no visible action: only the link and the menu are controls.
    expect(within(row("bob")).getAllByRole("button")).toEqual([screen.getByRole("button", { name: t.userActions("bob") })]);
    // Ctrl-click is left to the browser (a new tab).
    expect(fireEvent.click(link, { ctrlKey: true })).toBe(true);
    expect(window.location.search).toBe("?view=users");
  });

  it("opens a user's page from the row as a history entry; the breadcrumb and Back return", async () => {
    await show();
    const length = window.history.length;
    expect(fireEvent.click(within(row("bob")).getByRole("link", { name: "bob" }))).toBe(false);
    expect(window.location.search).toBe("?view=users&id=bob");
    expect(window.history.length).toBe(length + 1);
    expect(h1("bob")).toBeTruthy();
    // An in-app page change moves the focus to the new page's h1.
    expect(document.activeElement).toBe(h1("bob"));
    const crumbs = screen.getByRole("navigation", { name: t.carbon.breadcrumb });
    expect(within(crumbs).getByRole("link", { name: t.usersTitle }).getAttribute("href")).toBe("?view=users");
    fireEvent.click(within(crumbs).getByRole("link", { name: t.usersTitle }));
    expect(window.location.search).toBe("?view=users");
    expect(document.activeElement).toBe(h1(t.usersTitle));
    act(() => window.history.back());
    await waitFor(() => expect(window.location.search).toBe("?view=users&id=bob"));
    expect(document.activeElement).toBe(h1("bob"));
  });

  it("opens a user's page from the URL without moving the focus; an unknown user returns to the list", async () => {
    await showUser("bob");
    expect(document.activeElement).toBe(document.body);
    cleanup();
    window.history.replaceState(null, "", "/?view=log");
    window.history.pushState(null, "", "/?view=users&id=nobody");
    render(<UsersPage />);
    await waitFor(() => expect(window.location.search).toBe("?view=users"));
    expect(h1(t.usersTitle)).toBeTruthy();
    expect(document.activeElement).toBe(document.body);
    // The list replaced the URL of the unknown user.
    act(() => window.history.back());
    await waitFor(() => expect(window.location.search).toBe("?view=log"));
  });

  it("shows a user's page: tags, Reset password, the menu, the tiles and the API keys", async () => {
    await showUser("bob");
    const header = h1("bob").closest("header")!;
    expect(within(header).getByText(t.roleUser)).toBeTruthy();
    expect(within(header).getByText(t.stateActive)).toBeTruthy();
    expect(within(header).getByRole("button", { name: t.resetPassword }).className).toContain("cds--btn--tertiary");
    fireEvent.click(within(header).getByRole("button", { name: t.userActions("bob") }));
    expect(screen.getAllByRole("menuitem").map((i) => i.textContent)).toEqual([t.lock, t.makeAdmin, t.twoFactorReset, t.removePasskeys, t.removeUser]);
    expect(screen.getByRole("menuitem", { name: t.removeUser }).className).toContain("danger");
    fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
    const tiles = Object.fromEntries([...document.querySelectorAll(".app-summary__tile")].map((el) => [el.querySelector("dt")!.textContent, el.querySelector("dd")!.textContent]));
    expect(tiles).toEqual({
      [t.colAccounts]: `1${t.failingCount(1)}`,
      [t.colLastLogin]: t.never,
      [t.twoFactor]: t.twoFactorStateOn,
      [t.passkeys]: "2",
      [settingsTextFor("en").created]: shortDate("2026-10-01T00:00:00Z"),
    });
    const keys = screen.getByRole("region", { name: t.apiKeys });
    expect(within(keys).getByText("backup")).toBeTruthy();
    // Only bob's keys.
    expect(within(keys).queryByText("laptop")).toBeNull();
    // No other page lists keys: the table has no expandable rows any more.
    cleanup();
    window.history.replaceState(null, "", "/?view=users");
    await show();
    expect(screen.queryByText("backup")).toBeNull();
    expect(document.querySelector(".cds--table-expand")).toBeNull();
  });

  it("says when a user has no API keys", async () => {
    await showUser("carol");
    expect(within(screen.getByRole("region", { name: t.apiKeys })).getByText(t.apiKeyNone)).toBeTruthy();
  });

  it("removes a user from the page only after confirming, then shows the list in its place", async () => {
    // The entry before the user's page, to see where Back leads afterwards.
    window.history.replaceState(null, "", "/?view=log");
    window.history.pushState(null, "", "/?view=users&id=bob");
    render(<UsersPage />);
    await screen.findByRole("heading", { level: 1, name: "bob" });
    fireEvent.click(screen.getByRole("button", { name: t.userActions("bob") }));
    fireEvent.click(screen.getByRole("menuitem", { name: t.removeUser }));
    expect(within(dialog()).getByText(t.removeUserTitle("bob"))).toBeTruthy();
    fireEvent.click(within(dialog()).getByRole("button", { name: t.cancel }));
    expect(changes()).toEqual([]);
    expect(document.activeElement).toBe(screen.getByRole("button", { name: t.userActions("bob") }));

    fireEvent.click(screen.getByRole("button", { name: t.userActions("bob") }));
    fireEvent.click(screen.getByRole("menuitem", { name: t.removeUser }));
    fireEvent.click(within(dialog()).getByRole("button", { name: t.removeUser }));
    await waitFor(() => expect(window.location.search).toBe("?view=users"));
    expect(changes()).toEqual([{ method: "DELETE", url: "/api/users/bob", body: undefined }]);
    expect(document.activeElement).toBe(h1(t.usersTitle));
    // The list replaced the page: Back does not lead to the removed user.
    act(() => window.history.back());
    await waitFor(() => expect(window.location.search).toBe("?view=log"));
  });

  it("stays on the page when the removal is refused, with the reason", async () => {
    const fallback = vi.mocked(globalThis.fetch).getMockImplementation()!;
    vi.mocked(globalThis.fetch).mockImplementation(async (url, init) =>
      init?.method === "DELETE" && url === "/api/users/bob"
        ? new Response(JSON.stringify({ error: "x", code: "on_hold", holdIds: [3] }), { status: 409, headers: { "Content-Type": "application/json" } })
        : fallback(url, init),
    );
    await showUser("bob");
    fireEvent.click(screen.getByRole("button", { name: t.userActions("bob") }));
    fireEvent.click(screen.getByRole("menuitem", { name: t.removeUser }));
    fireEvent.click(within(dialog()).getByRole("button", { name: t.removeUser }));
    expect(await screen.findByText(t.userOnHold([3]))).toBeTruthy();
    expect(window.location.search).toBe("?view=users&id=bob");
    expect(h1("bob")).toBeTruthy();
  });

  it("runs the other actions from the page with the same confirmations", async () => {
    await showUser("bob");
    fireEvent.click(screen.getByRole("button", { name: t.resetPassword }));
    expect(within(dialog()).getByText(t.resetPasswordTitle("bob"))).toBeTruthy();
    expect(changes()).toEqual([]);
    fireEvent.click(within(dialog()).getByRole("button", { name: t.confirmReset }));
    await waitFor(() => expect(changes()).toEqual([{ method: "POST", url: "/api/users/bob/password", body: { removePasskeys: true, revokeApiKeys: true } }]));
    // The generated password shows on the user's page too.
    const panel = await screen.findByRole("status");
    expect(within(panel).getByText("Bob-Generated-2")).toBeTruthy();
    expect(h1("bob")).toBeTruthy();

    const menu = screen.getByRole("button", { name: t.userActions("bob") });
    fireEvent.click(menu);
    fireEvent.click(screen.getByRole("menuitem", { name: t.lock }));
    expect(within(dialog()).getByText(t.lockTitle("bob"))).toBeTruthy();
    expect(changes()).toHaveLength(1);
    fireEvent.click(within(dialog()).getByRole("button", { name: t.lock }));
    await waitFor(() => expect(changes()[1]).toEqual({ method: "PATCH", url: "/api/users/bob", body: { locked: true } }));
    expect(document.activeElement).toBe(menu);

    fireEvent.click(menu);
    fireEvent.click(screen.getByRole("menuitem", { name: t.makeAdmin }));
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() => expect(changes()[2]).toEqual({ method: "PATCH", url: "/api/users/bob", body: { admin: true } }));
  });

  it("revokes a key on the page only after confirming, and puts the focus on the API keys heading", async () => {
    await showUser("bob");
    const keys = screen.getByRole("region", { name: t.apiKeys });
    fireEvent.click(within(keys).getByRole("button", { name: t.apiKeyRevoke }));
    expect(changes()).toEqual([]);
    fireEvent.click(within(dialog()).getByRole("button", { name: t.apiKeyRevoke }));
    await waitFor(() => expect(changes()).toEqual([{ method: "DELETE", url: "/api/api-keys/7", body: undefined }]));
    // The Revoke button went with the key's state; the focus did not fall to the body.
    await waitFor(() => expect(within(keys).queryByRole("button", { name: t.apiKeyRevoke })).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(within(keys).getByRole("heading", { level: 2, name: t.apiKeys })));
  });

  it("shows the own user's page without user actions, but with Revoke for the own keys", async () => {
    await showUser("alice");
    expect(screen.queryByRole("button", { name: t.resetPassword })).toBeNull();
    expect(screen.queryByRole("button", { name: t.userActions("alice") })).toBeNull();
    expect(screen.getByText(t.selfHint, { exact: false })).toBeTruthy();
    const profile = screen.getByRole("link", { name: settingsTextFor("en").openProfile });
    expect(profile.getAttribute("href")).toBe("?view=profile");
    fireEvent.click(profile);
    expect(window.location.search).toBe("?view=profile");
    cleanup();
    await showUser("alice");
    const keys = screen.getByRole("region", { name: t.apiKeys });
    fireEvent.click(within(keys).getByRole("button", { name: t.apiKeyRevoke }));
    fireEvent.click(within(dialog()).getByRole("button", { name: t.apiKeyRevoke }));
    await waitFor(() => expect(changes()).toEqual([{ method: "DELETE", url: "/api/api-keys/9", body: undefined }]));
  });

  it("offers Reset 2FA and Remove passkeys only where there is something to reset", async () => {
    await show();
    fireEvent.click(screen.getByRole("button", { name: t.userActions("carol") }));
    const items = screen.getAllByRole("menuitem").map((i) => i.textContent);
    expect(items).toEqual([t.resetPassword, t.unlock, t.makeAdmin, t.removeUser]);
    fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
    fireEvent.click(screen.getByRole("button", { name: t.userActions("bob") }));
    expect(screen.getAllByRole("menuitem").map((i) => i.textContent)).toEqual([
      t.resetPassword, t.lock, t.makeAdmin, t.twoFactorReset, t.removePasskeys, t.removeUser,
    ]);
  });

  it("removes a user only after the danger confirmation", async () => {
    await show();
    pick("bob", t.removeUser);
    expect(dialog().closest(".cds--modal")!.className).toContain("cds--modal--danger");
    expect(within(dialog()).getByText(t.removeUserTitle("bob"))).toBeTruthy();
    expect(within(dialog()).getByText(t.removeUserText)).toBeTruthy();
    expect(changes()).toEqual([]);
    // Cancel sends nothing.
    fireEvent.click(within(dialog()).getByRole("button", { name: t.cancel }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(changes()).toEqual([]);

    pick("bob", t.removeUser);
    fireEvent.click(within(dialog()).getByRole("button", { name: t.removeUser }));
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() => expect(changes()).toEqual([{ method: "DELETE", url: "/api/users/bob", body: undefined }]));
  });

  it("puts the focus on the list's h1 when a removed user's row goes", async () => {
    // The server forgets bob once he is removed, so his row goes on the reload.
    const fallback = vi.mocked(globalThis.fetch).getMockImplementation()!;
    let gone = false;
    vi.mocked(globalThis.fetch).mockImplementation(async (url, init) => {
      if (init?.method === "DELETE" && url === "/api/users/bob") gone = true;
      if ((init?.method ?? "GET") === "GET" && url === "/api/users" && gone) {
        return new Response(JSON.stringify({ users: users.filter((u) => u.name !== "bob") }), { headers: { "Content-Type": "application/json" } });
      }
      return fallback(url, init);
    });
    await show();
    pick("bob", t.removeUser);
    fireEvent.click(within(dialog()).getByRole("button", { name: t.removeUser }));
    await waitFor(() => expect(screen.queryByRole("link", { name: "bob" })).toBeNull());
    expect(changes()).toEqual([{ method: "DELETE", url: "/api/users/bob", body: undefined }]);
    expect(window.location.search).toBe("?view=users");
    await waitFor(() => expect(document.activeElement).toBe(h1(t.usersTitle)));
  });

  describe("a user named by legal holds", () => {
    /** Answers the removal of bob with 409 and the given body. */
    function refuseRemoval(body: Record<string, unknown>) {
      const fallback = vi.mocked(globalThis.fetch).getMockImplementation()!;
      vi.mocked(globalThis.fetch).mockImplementation(async (url, init) => {
        if (init?.method === "DELETE" && url === "/api/users/bob") {
          return new Response(JSON.stringify(body), { status: 409, headers: { "Content-Type": "application/json" } });
        }
        return fallback(url, init);
      });
    }
    async function remove() {
      await show();
      pick("bob", t.removeUser);
      fireEvent.click(within(dialog()).getByRole("button", { name: t.removeUser }));
    }

    it("names the holds from holdIds, not the server's text", async () => {
      refuseRemoval({ error: "named by a legal hold", code: "on_hold", holdIds: [3, 5] });
      await remove();
      expect(await screen.findByText("Legal holds #3, #5 name this user; lock the user instead")).toBeTruthy();
      expect(screen.queryByText(/named by a legal hold/)).toBeNull();
    });

    it("names one hold in the singular", () => {
      expect(messagesFor("en").userOnHold([3])).toBe("Legal hold #3 names this user; lock the user instead");
      expect(messagesFor("de").userOnHold([3])).toBe("Legal Hold #3 nennt diesen Benutzer; sperren Sie den Benutzer stattdessen");
    });

    it("says it without numbers when the answer has no holdIds", async () => {
      refuseRemoval({ error: "named by a legal hold", code: "on_hold" });
      await remove();
      expect(await screen.findByText("A legal hold names this user; lock the user instead")).toBeTruthy();
    });

    it("says it in German", async () => {
      applySettings({ language: "de", locale: "de" });
      try {
        refuseRemoval({ error: "named by a legal hold", code: "on_hold", holdIds: [3, 5] });
        const de = messagesFor("de");
        render(<UsersPage />);
        await screen.findByText("bob");
        fireEvent.click(screen.getByRole("button", { name: de.userActions("bob") }));
        fireEvent.click(screen.getByRole("menuitem", { name: de.removeUser }));
        fireEvent.click(within(dialog()).getByRole("button", { name: de.removeUser }));
        expect(await screen.findByText("Legal Holds #3, #5 nennen diesen Benutzer; sperren Sie den Benutzer stattdessen")).toBeTruthy();
      } finally {
        applySettings({ language: "en", locale: "en" });
      }
    });

    it("shows the server's text for other conflicts", async () => {
      refuseRemoval({ error: "\"bob\" still owns 1 account(s)" });
      await remove();
      expect(await screen.findByText(t.failed('"bob" still owns 1 account(s)'))).toBeTruthy();
    });
  });

  it("resets a password with the chosen options and shows the new one once", async () => {
    await show();
    pick("bob", t.resetPassword);
    expect(within(dialog()).getByText(t.resetPasswordTitle("bob"))).toBeTruthy();
    const passkeys = within(dialog()).getByLabelText(t.resetAlsoPasskeys(2)) as HTMLInputElement;
    const apiKeys = within(dialog()).getByLabelText(t.resetAlsoApiKeys(1)) as HTMLInputElement;
    // Both are on by default: after a compromise a new password alone is not enough.
    expect(passkeys.checked).toBe(true);
    expect(apiKeys.checked).toBe(true);
    fireEvent.click(passkeys);
    expect(passkeys.checked).toBe(false);
    expect(changes()).toEqual([]);
    fireEvent.click(within(dialog()).getByRole("button", { name: t.confirmReset }));
    await waitFor(() =>
      expect(changes()).toEqual([{ method: "POST", url: "/api/users/bob/password", body: { removePasskeys: false, revokeApiKeys: true } }]),
    );
    const panel = await screen.findByRole("status");
    expect(panel.textContent).toContain(t.generatedFor("bob"));
    expect(within(panel).getByText("Bob-Generated-2")).toBeTruthy();
    fireEvent.click(within(panel).getByRole("button", { name: t.done }));
    expect(screen.queryByText("Bob-Generated-2")).toBeNull();
  });

  it("never asks to remove passkeys a user does not have", async () => {
    await show();
    pick("dave", t.resetPassword);
    expect(within(dialog()).queryByRole("checkbox")).toBeNull();
    fireEvent.click(within(dialog()).getByRole("button", { name: t.confirmReset }));
    await waitFor(() =>
      expect(changes()).toEqual([{ method: "POST", url: "/api/users/dave/password", body: { removePasskeys: false, revokeApiKeys: true } }]),
    );
  });

  it("resets 2FA and removes passkeys only after confirming", async () => {
    await show();
    pick("bob", t.twoFactorReset);
    expect(within(dialog()).getByText(t.twoFactorResetTitle("bob"))).toBeTruthy();
    expect(changes()).toEqual([]);
    fireEvent.click(within(dialog()).getByRole("button", { name: t.twoFactorReset }));
    await waitFor(() => expect(changes()).toEqual([{ method: "POST", url: "/api/users/bob/2fa/reset", body: undefined }]));

    pick("bob", t.removePasskeys);
    expect(within(dialog()).getByText(t.removePasskeysTitle("bob", 2))).toBeTruthy();
    expect(changes()).toHaveLength(1);
    fireEvent.click(within(dialog()).getByRole("button", { name: t.removePasskeys }));
    await waitFor(() => expect(changes()[1]).toEqual({ method: "DELETE", url: "/api/users/bob/passkeys", body: undefined }));
  });

  it("asks before locking, not before unlocking or changing the role", async () => {
    await show();
    pick("bob", t.lock);
    expect(within(dialog()).getByText(t.lockTitle("bob"))).toBeTruthy();
    expect(changes()).toEqual([]);
    fireEvent.click(within(dialog()).getByRole("button", { name: t.lock }));
    await waitFor(() => expect(changes()).toEqual([{ method: "PATCH", url: "/api/users/bob", body: { locked: true } }]));

    pick("carol", t.unlock);
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() => expect(changes()[1]).toEqual({ method: "PATCH", url: "/api/users/carol", body: { locked: false } }));

    pick("dave", t.makeAdmin);
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() => expect(changes()[2]).toEqual({ method: "PATCH", url: "/api/users/dave", body: { admin: true } }));
  });

  it("adds a user in a Modal and shows the generated password once", async () => {
    await show();
    fireEvent.click(screen.getByRole("button", { name: t.addUser }));
    const modal = within(dialog());
    // An empty name sends nothing.
    fireEvent.click(modal.getByRole("button", { name: t.addUser }));
    expect(changes()).toEqual([]);
    expect(modal.getByText(t.userNameRequired)).toBeTruthy();
    fireEvent.change(modal.getByLabelText(t.userName), { target: { value: "erin" } });
    const admin = modal.getByLabelText(t.userIsAdmin) as HTMLInputElement;
    expect(admin.checked).toBe(false);
    fireEvent.click(admin);
    fireEvent.click(modal.getByRole("button", { name: t.addUser }));
    await waitFor(() => expect(changes()).toEqual([{ method: "POST", url: "/api/users", body: { name: "erin", admin: true } }]));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    const panel = screen.getByRole("status");
    expect(panel.textContent).toContain(t.generatedFor("erin"));
    expect(within(panel).getByText("Erin-Generated-1")).toBeTruthy();
    expect(within(panel).getByRole("button", { name: t.carbon.copy })).toBeTruthy();
  });

  it("returns the focus to the user's menu button after a confirmation", async () => {
    await show();
    const menu = screen.getByRole("button", { name: t.userActions("bob") });
    pick("bob", t.lock);
    fireEvent.click(within(dialog()).getByRole("button", { name: t.cancel }));
    expect(document.activeElement).toBe(menu);
  });
});

describe("German", () => {
  const english = [/\bClose\b/, /Copy to clipboard/, /Copied/, /\bOptions\b/, /\bLoading\b/, /Focus sentinel/, /\bdanger\b/, /\bexpand\b/i,
    /\bcollapse\b/i, /Expand current row/, /Increment/, /Decrement/];

  function readable(): string {
    const attrs = ["aria-label", "title", "placeholder", "alt", "aria-valuetext", "aria-description"];
    const values = [...document.body.querySelectorAll("*")].flatMap((el) => attrs.map((a) => el.getAttribute(a) ?? ""));
    return [document.body.textContent ?? "", ...values].join("\n");
  }

  function noEnglish() {
    const text = readable();
    for (const e of english) expect(text, String(e)).not.toMatch(e);
  }

  it("shows the table, the menu and the Modals in German", async () => {
    applySettings({ language: "de", locale: "de" });
    try {
      const de = messagesFor("de");
      render(<UsersPage />);
      await screen.findByText("bob");
      expect(screen.getByRole("heading", { level: 1, name: de.usersTitle })).toBeTruthy();
      expect(screen.getByRole("columnheader", { name: de.colSignIn })).toBeTruthy();
      fireEvent.click(screen.getByRole("button", { name: de.userActions("bob") }));
      expect(screen.getByRole("menuitem", { name: de.twoFactorReset })).toBeTruthy();
      noEnglish();
      fireEvent.click(screen.getByRole("menuitem", { name: de.resetPassword }));
      expect(screen.getByRole("button", { name: de.carbon.closeModal })).toBeTruthy();
      noEnglish();
      fireEvent.click(within(dialog()).getByRole("button", { name: de.confirmReset }));
      await screen.findByText("Bob-Generated-2");
      expect(screen.getByRole("button", { name: de.carbon.copy })).toBeTruthy();
      noEnglish();
      fireEvent.click(screen.getByRole("button", { name: de.addUser }));
      noEnglish();
      await act(async () => {});
    } finally {
      applySettings({ language: "en", locale: "en" });
    }
  });

  it("shows a user's page in German", async () => {
    applySettings({ language: "de", locale: "de" });
    try {
      const de = messagesFor("de");
      const sde = settingsTextFor("de");
      await showUser("bob");
      expect(screen.getByRole("navigation", { name: de.carbon.breadcrumb })).toBeTruthy();
      expect(screen.getByText(sde.created)).toBeTruthy();
      expect(screen.getByRole("region", { name: de.apiKeys })).toBeTruthy();
      fireEvent.click(screen.getByRole("button", { name: de.userActions("bob") }));
      expect(screen.getByRole("menuitem", { name: de.removeUser })).toBeTruthy();
      noEnglish();
      fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
      cleanup();
      await showUser("alice");
      expect(screen.getByRole("link", { name: sde.openProfile })).toBeTruthy();
      noEnglish();
      expect(document.body.textContent).not.toMatch(/Created|Open your profile/);
    } finally {
      applySettings({ language: "en", locale: "en" });
    }
  });
});
