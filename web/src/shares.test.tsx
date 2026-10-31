// Shared accounts on the Accounts pages (#389, part 3): the owner's "Shared
// with" section on an account's page (share by exact user name, Remove),
// and the "Shared with you" table of the accounts other users shared.
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Account, AccountsResponse, SharedAccount } from "./api";
import { AccountsPage } from "./AccountsPage";
import { day } from "./format";
import { applySettings, messagesFor, t } from "./i18n";

function account(name: string, more: Partial<Account> = {}): Account {
  return {
    name, kind: "imap", enabled: true, removed: false, purging: false, purge: null, host: "imap.example.com", port: 993, tls: "tls",
    username: `${name}@example.com`, includedFolders: [], excludedFolders: [], syncInterval: "default",
    folders: [{ name: "INBOX", messages: 4, lastSyncedAt: null, lastReconciledAt: null, delimiter: "/" }],
    goneMessages: 0, lastReconciledAt: null, addresses: [], lastReceivedAt: null, fill: null, fills: [], firstReceived: null, sharedWith: [],
    sync: { state: "idle", lastRun: null, failureStreak: 0, failingSince: null, health: "ok", syncEvery: "6h0m0s", nextAt: null },
    ...more,
  };
}

const sharedAccount = (owner: string, name: string, more: Partial<SharedAccount> = {}): SharedAccount => ({
  key: `${owner}/${name}`, name, owner, kind: "imap", enabled: true, removed: false,
  folders: [
    { name: "INBOX", messages: 3, lastSyncedAt: "2026-10-30T08:00:00Z", lastReconciledAt: null, delimiter: "/" },
    { name: "Sent", messages: 2, lastSyncedAt: "2026-10-30T09:00:00Z", lastReconciledAt: null, delimiter: "/" },
  ],
  ...more,
});

const data = (accounts: Account[], shared: SharedAccount[] = []): AccountsResponse => ({
  accounts, shared, manage: true, syncInterval: "6h0m0s", syncIntervalMin: "5m0s", reconcileInterval: "", alertAfter: 3, smtp: {},
});

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
let requests: { method: string; url: string }[];
let answer: (url: string, method: string) => Response | undefined;

beforeEach(() => {
  requests = [];
  answer = () => undefined;
  vi.spyOn(globalThis, "fetch").mockImplementation(async (url, init) => {
    const method = init?.method ?? "GET";
    requests.push({ method, url: String(url) });
    return answer(String(url), method) ?? new Response(null, { status: 204 });
  });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  applySettings({ language: "en", locale: "en" });
  window.history.replaceState(null, "", "/");
});

function show(url: string, d: AccountsResponse) {
  window.history.replaceState(null, "", url);
  const reload = vi.fn();
  const showAccount = vi.fn();
  render(<AccountsPage accounts={{ data: d, error: "", reload }} admin={false} renamed={() => {}} showGone={() => {}} showAccount={showAccount} />);
  return { reload, showAccount };
}

const shares = () => within(screen.getByRole("region", { name: t.sharesTitle }));
const changes = () => requests.filter((r) => r.method !== "GET");

describe("Shared with, on the owner's account page", () => {
  it("lists the users with the day they got it, and Remove ends a share", async () => {
    const since = "2026-10-20T10:00:00Z";
    const { reload } = show("/?view=accounts&id=work", data([account("work", { sharedWith: [{ user: "pat", since }, { user: "jo", since }] })]));
    expect(shares().getByText(t.sharesNote)).toBeTruthy();
    expect(shares().getByText(t.sharesSince(day(since)), { selector: "li:first-child span span" })).toBeTruthy();
    const remove = shares().getByRole("button", { name: t.unshareLabel("pat") });
    expect(remove.textContent).toBe(t.remove);
    fireEvent.click(remove);
    await waitFor(() => expect(shares().getByRole("status").textContent).toBe(t.unshareDone("pat")));
    expect(changes()).toEqual([{ method: "DELETE", url: "/api/accounts/work/shares/pat" }]);
    expect(reload).toHaveBeenCalled();
    // The focus goes to the field, not to the body with the row gone.
    expect(document.activeElement).toBe(shares().getByLabelText(t.userName));
  });

  it("shares with the exact user name typed, with Enter or the button", async () => {
    const { reload } = show("/?view=accounts&id=my%20mail", data([account("my mail")]));
    expect(shares().getByText(t.sharesNone)).toBeTruthy();
    const share = shares().getByRole("button", { name: t.shareButton });
    expect(share).toHaveProperty("disabled", true);
    fireEvent.change(shares().getByLabelText(t.userName), { target: { value: " pat " } });
    fireEvent.submit(share.closest("form")!);
    await waitFor(() => expect(shares().getByRole("status").textContent).toBe(t.shareDone("pat")));
    expect(changes()).toEqual([{ method: "PUT", url: "/api/accounts/my%20mail/shares/pat" }]);
    expect(reload).toHaveBeenCalled();
    expect(shares().getByLabelText(t.userName)).toHaveProperty("value", "");
  });

  it("says when there is no such user, or the name is the owner's own", async () => {
    answer = (url, method) => (method === "PUT" ? (url.endsWith("/nobody") ? json({ error: "no such user" }, 404) : json({ error: "self" }, 400)) : undefined);
    show("/?view=accounts&id=work", data([account("work")]));
    const field = shares().getByLabelText(t.userName);
    fireEvent.change(field, { target: { value: "nobody" } });
    fireEvent.click(shares().getByRole("button", { name: t.shareButton }));
    await waitFor(() => expect(shares().getByText(t.shareNoUser("nobody"))).toBeTruthy());
    expect(field.getAttribute("aria-invalid")).toBe("true");
    // The name is kept to correct it.
    expect(field).toHaveProperty("value", "nobody");
    fireEvent.change(field, { target: { value: "me" } });
    fireEvent.click(shares().getByRole("button", { name: t.shareButton }));
    await waitFor(() => expect(shares().getByText(t.shareSelf)).toBeTruthy());
  });

  it("lists a removed account's shares to end them, without the field", () => {
    show("/?view=accounts&id=old", data([account("old", { removed: true, sharedWith: [{ user: "pat", since: "2026-10-20T10:00:00Z" }] })]));
    expect(shares().getByText(t.sharesRemovedNote)).toBeTruthy();
    expect(shares().getByRole("button", { name: t.unshareLabel("pat") })).toBeTruthy();
    expect(shares().queryByLabelText(t.userName)).toBeNull();
  });

  it("is not there for an smtp account, nor for a removed one without shares", () => {
    show("/?view=accounts&id=journal", data([account("journal", { kind: "smtp", host: "", port: 0, username: "", addresses: ["a@archive.local"] }), account("old", { removed: true })]));
    expect(screen.queryByRole("region", { name: t.sharesTitle })).toBeNull();
    cleanup();
    show("/?view=accounts&id=old", data([account("old", { removed: true })]));
    expect(screen.queryByRole("region", { name: t.sharesTitle })).toBeNull();
  });

  it("speaks German", () => {
    applySettings({ language: "de", locale: "de" });
    const de = messagesFor("de");
    show("/?view=accounts&id=work", data([account("work", { sharedWith: [{ user: "pat", since: "2026-10-20T10:00:00Z" }] })]));
    const region = within(screen.getByRole("region", { name: "Geteilt mit" }));
    expect(region.getByRole("button", { name: "pat entfernen" }).textContent).toBe("Entfernen");
    expect(region.getByRole("button", { name: "Teilen" })).toBeTruthy();
    expect(region.getByText(de.sharesNote)).toBeTruthy();
  });
});

describe("Shared with you, on the accounts list", () => {
  it("lists the shared accounts with owner, type, messages and last sync, without a menu", () => {
    show("/?view=accounts", data([account("work")], [sharedAccount("anna", "work"), sharedAccount("ben", "old", { removed: true })]));
    const table = within(screen.getByRole("table", { name: t.sharedWithYou }));
    const rows = table.getAllByRole("row").slice(1);
    expect(rows).toHaveLength(2);
    const anna = within(rows[0]!);
    expect(anna.getByRole("link", { name: "work" }).getAttribute("href")).toBe("?account=anna%2Fwork");
    expect(anna.getByRole("cell", { name: "anna" })).toBeTruthy();
    expect(anna.getByRole("cell", { name: "5" })).toBeTruthy();
    // The newest of the folders' syncs (decision 15).
    expect(anna.getByText(/^Last sync /)).toBeTruthy();
    expect(anna.queryByRole("button", { name: /^Actions/ })).toBeNull();
    expect(within(rows[1]!).getByText(t.removed)).toBeTruthy();
    expect(screen.getByText(t.sharedWithYouNote)).toBeTruthy();
  });

  it("opens a shared account's mail by owner/name", () => {
    const { showAccount } = show("/?view=accounts", data([], [sharedAccount("anna", "work")]));
    // A user without own accounts still sees what was shared.
    expect(screen.getByText(t.noAccounts)).toBeTruthy();
    fireEvent.click(within(screen.getByRole("table", { name: t.sharedWithYou })).getByRole("link", { name: "work" }));
    expect(showAccount).toHaveBeenCalledWith("anna/work");
  });

  it("is not there without shared accounts", () => {
    show("/?view=accounts", data([account("work")]));
    expect(screen.queryByRole("table", { name: t.sharedWithYou })).toBeNull();
    expect(screen.queryByText(t.sharedWithYou)).toBeNull();
  });
});
