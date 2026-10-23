// The Users page on Carbon (#168 part 5): the table, the actions menu, and
// the danger confirmations, each of which sends its request only after it
// was confirmed, and the same request as before.
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ApiKey, ManagedUser } from "./api";
import { applySettings, messagesFor, t } from "./i18n";
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

let requests: { method: string; url: string; body: unknown }[];
/** The requests that change something: everything but the list reloads. */
const changes = () => requests.filter((r) => r.method !== "GET");

beforeEach(() => {
  requests = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (url, init) => {
    const method = init?.method ?? "GET";
    requests.push({ method, url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined });
    const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
    if (method === "GET" && url === "/api/users") return json({ users });
    if (method === "GET" && url === "/api/api-keys") return json({ keys: [bobKey] });
    if (method === "POST" && url === "/api/users") return json({ name: "erin", password: "Erin-Generated-1" }, 201);
    if (method === "POST" && String(url).endsWith("/password")) return json({ name: "bob", password: "Bob-Generated-2" });
    return new Response(null, { status: 204 });
  });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

async function show() {
  render(<UsersPage />);
  await screen.findByText("bob");
}

const row = (name: string) => screen.getByText(name, { exact: true, selector: ".users-table__name" }).closest("tr")!;

/** Opens the actions menu of a user and picks an item. */
function pick(name: string, item: string) {
  fireEvent.click(screen.getByRole("button", { name: t.userActions(name) }));
  fireEvent.click(screen.getByRole("menuitem", { name: item }));
}

const dialog = () => screen.getByRole("dialog");

describe("UsersPage", () => {
  it("shows each user's role, state and sign-in; the own row has the hint and no actions", async () => {
    await show();
    const bob = within(row("bob"));
    expect(bob.getByText(t.roleUser)).toBeTruthy();
    expect(bob.getByText(t.stateActive)).toBeTruthy();
    expect(bob.getByText(t.twoFactorShortOn)).toBeTruthy();
    expect(bob.getByText(t.passkeyCount(2))).toBeTruthy();
    expect(bob.getByText(t.apiKeyCount(1))).toBeTruthy();
    expect(bob.getByText(t.failingCount(1))).toBeTruthy();
    expect(within(row("carol")).getByText(t.stateLocked)).toBeTruthy();
    expect(within(row("dave")).getByText(t.stateMustChange)).toBeTruthy();
    const alice = within(row("alice"));
    expect(alice.getByText(t.roleAdmin)).toBeTruthy();
    expect(alice.getByText(t.selfHint)).toBeTruthy();
    expect(screen.queryByRole("button", { name: t.userActions("alice") })).toBeNull();
    expect(screen.getByRole("button", { name: t.userActions("bob") })).toBeTruthy();
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

  it("shows a user's API keys and revokes one only after confirming", async () => {
    await show();
    expect(screen.queryByText("backup")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: t.apiKeysOf("bob") }));
    expect(screen.getByText("backup")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: t.apiKeyRevoke }));
    expect(changes()).toEqual([]);
    fireEvent.click(within(dialog()).getByRole("button", { name: t.apiKeyRevoke }));
    await waitFor(() => expect(changes()).toEqual([{ method: "DELETE", url: "/api/api-keys/7", body: undefined }]));
    // Users without keys have nothing to expand.
    expect(screen.queryByRole("button", { name: t.apiKeysOf("carol") })).toBeNull();
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
      fireEvent.click(screen.getByRole("button", { name: de.apiKeysOf("bob") }));
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
});
