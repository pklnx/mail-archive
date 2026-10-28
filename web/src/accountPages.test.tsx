// The Accounts page (#168 part 3, #285 part 2): the pages under Accounts in
// the URL, the accounts table with its row menus, the account's page with
// its sections, the danger confirmations, the removed accounts, and the
// German texts of Carbon's components.
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Account, AccountsResponse, PurgePreview } from "./api";
import { AccountsPage } from "./AccountsPage";
import { PostfixPanel } from "./accountParts";
import { PurgeReview } from "./AccountPurge";
import { dialogOpen } from "./dialogs";
import { fill } from "./fillFixture";
import { applySettings, messagesFor, t } from "./i18n";
import { settingsText, settingsTextFor } from "./settingsText";

function account(name: string, more: Partial<Account> = {}): Account {
  return {
    name, kind: "imap", enabled: true, removed: false, purging: false, purge: null, host: "imap.example.com", port: 993, tls: "tls",
    username: `${name}@example.com`, includedFolders: [], excludedFolders: [], syncInterval: "default",
    folders: [{ name: "INBOX", messages: 4, lastSyncedAt: null, lastReconciledAt: null, delimiter: "/" }],
    goneMessages: 0, lastReconciledAt: null, addresses: [], lastReceivedAt: null, fill: null, fills: [], firstReceived: null,
    sync: { state: "idle", lastRun: null, failureStreak: 0, failingSince: null, health: "ok", syncEvery: "6h0m0s", nextAt: null },
    ...more,
  };
}

const smtp = (name: string, more: Partial<Account> = {}) =>
  account(name, { kind: "smtp", host: "", port: 0, username: "", addresses: [`${name}@archive.local`], syncInterval: "default", ...more });
const imported = (name: string, more: Partial<Account> = {}) => account(name, { kind: "import", host: "", port: 0, username: "", ...more });
const busy = (a: Account): Account => ({ ...a, sync: { ...a.sync, state: "running" } });
const purge = { id: 1, expected: 10, locations: 2, deleted: 4, kept: 0 };

const data = (accounts: Account[], more: Partial<AccountsResponse> = {}): AccountsResponse => ({
  accounts, manage: true, syncInterval: "6h0m0s", syncIntervalMin: "5m0s", reconcileInterval: "24h0m0s", alertAfter: 3, smtp: {}, ...more,
});

let requests: { method: string; url: string; body: unknown }[];
/** The requests that change something: everything but the reads. */
const changes = () => requests.filter((r) => r.method !== "GET");
let answer: (url: string, method: string) => Response | undefined;
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });

beforeEach(() => {
  requests = [];
  answer = () => undefined;
  vi.spyOn(globalThis, "fetch").mockImplementation(async (url, init) => {
    const method = init?.method ?? "GET";
    requests.push({ method, url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined });
    const own = answer(String(url), method);
    if (own) return own;
    if (String(url).endsWith("/api/accounts") && method === "POST") return json({ name: "journal" }, 201);
    if (String(url).endsWith("/server-folders")) {
      return json({ folders: [{ name: "INBOX", selected: true }, { name: "Trash", specialUse: "\\Trash", selected: true }] });
    }
    if (String(url).endsWith("/purge") && method === "GET") return json(preview());
    if (String(url).endsWith("/purge") && method === "POST") return json({ purge: 1 }, 202);
    if (method === "DELETE") return json({ result: "removed" });
    return new Response(null, { status: 204 });
  });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  window.history.replaceState(null, "", "/");
});

function preview(more: Partial<PurgePreview> = {}): PurgePreview {
  return {
    account: "old", kind: "imap", locations: 5, messages: 4, deleted: 3, deletedBytes: 2048, kept: 0, keptIn: [], grace: "168h0m0s",
    blockers: { notRemoved: false, syncing: false, purgeOpen: false, rules: [], holds: [], messages: [] }, purge: null, ...more,
  };
}

function show(url: string, d: AccountsResponse | null, admin = true, error = "") {
  window.history.replaceState(null, "", url);
  const reload = vi.fn();
  const renamed = vi.fn();
  const showGone = vi.fn();
  const page = (next: AccountsResponse | null, err = "") => (
    <AccountsPage accounts={{ data: next, error: err, reload }} admin={admin} renamed={renamed} showGone={showGone} />
  );
  const view = render(page(d, error));
  const rerender = (next: AccountsResponse | null, err = "") => view.rerender(page(next, err));
  return { reload, renamed, showGone, rerender };
}

const h1 = (name: string) => screen.getByRole("heading", { level: 1, name });
const row = (name: string) => screen.getByText(name, { exact: true, selector: ".accounts-table__name" }).closest("tr")!;
const section = (name: string) => within(screen.getByRole("region", { name }));
const dialog = () => within(screen.getByRole("dialog"));
const menuButton = (name: string) => screen.getByRole("button", { name: t.userActions(name) });

/** Opens the menu of an account (its row, or its page) and returns its items by label, with whether each is disabled. */
function menu(name: string): [string, boolean][] {
  fireEvent.click(menuButton(name));
  return screen.getAllByRole("menuitem").map((i) => [i.textContent ?? "", i.getAttribute("aria-disabled") === "true"]);
}

/** Opens an account's menu and picks an item. */
function pick(name: string, item: string) {
  fireEvent.click(menuButton(name));
  fireEvent.click(screen.getByRole("menuitem", { name: item }));
}

describe("the pages under Accounts", () => {
  it("open from the list as history entries, with a breadcrumb back, and follow Back; the focus goes to the new h1", async () => {
    show("/?view=accounts", data([account("work")]));
    expect(h1(t.accountsTitle)).toBeTruthy();
    const length = window.history.length;
    const link = within(row("work")).getByRole("link", { name: "work" });
    expect(link.getAttribute("href")).toBe("?view=accounts&id=work");
    // Ctrl-click is left to the browser (a new tab).
    expect(fireEvent.click(link, { ctrlKey: true })).toBe(true);
    expect(window.location.search).toBe("?view=accounts");
    fireEvent.click(link);
    expect(window.location.search).toBe("?view=accounts&id=work");
    expect(window.history.length).toBe(length + 1);
    expect(document.activeElement).toBe(h1("work"));
    const crumbs = screen.getByRole("navigation", { name: t.carbon.breadcrumb });
    fireEvent.click(within(crumbs).getByRole("link", { name: t.accountsTitle }));
    expect(window.location.search).toBe("?view=accounts");
    expect(document.activeElement).toBe(h1(t.accountsTitle));

    fireEvent.click(screen.getByRole("button", { name: t.addAccount }));
    expect(window.location.search).toBe("?view=accounts&add=1");
    expect(document.activeElement).toBe(h1(t.addAccount));
    // Back returns to the list: the page was a history entry.
    act(() => window.history.back());
    await waitFor(() => expect(h1(t.accountsTitle)).toBeTruthy());
    expect(window.location.search).toBe("?view=accounts");
    expect(document.activeElement).toBe(h1(t.accountsTitle));
  });

  it("open from the URL without moving the focus, so a reload keeps them, for every kind of account", () => {
    const d = data([account("work"), smtp("journal"), imported("files"), account("old", { removed: true })]);
    for (const name of ["work", "journal", "files", "old"]) {
      show(`/?view=accounts&id=${name}`, d);
      expect(h1(name)).toBeTruthy();
      expect(document.activeElement, name).toBe(document.body);
      cleanup();
    }
    show("/?view=accounts&import=journal", d);
    expect(h1(t.importFromIMAP)).toBeTruthy();
    expect(screen.getByRole("button", { name: t.loadFillFolders })).toBeTruthy();
  });

  it("return to the list for an account that does not exist or does not fit, replacing the history entry", () => {
    const d = data([account("work"), smtp("journal")]);
    for (const url of ["?view=accounts&id=nope", "?view=accounts&edit=nope", "?view=accounts&import=work", "?view=accounts&import=nope"]) {
      const length = window.history.length;
      show(`/${url}`, d);
      expect(window.location.search, url).toBe("?view=accounts");
      expect(window.history.length, url).toBe(length);
      expect(h1(t.accountsTitle)).toBeTruthy();
      cleanup();
    }
    // Without management (no secret key) there is nothing to add or import.
    for (const url of ["?view=accounts&add=1", "?view=accounts&import=journal"]) {
      show(`/${url}`, data([account("work"), smtp("journal")], { manage: false }));
      expect(window.location.search, url).toBe("?view=accounts");
      cleanup();
    }
  });

  it("wait while the list loads, then decide", () => {
    window.history.replaceState(null, "", "/?view=accounts&edit=work");
    const before = window.history.length;
    const { rerender } = show("/?view=accounts&edit=work", null);
    expect(screen.getByText(t.loading)).toBeTruthy();
    // An old edit= link opens the same page; the URL now says id= (history replace).
    expect(window.location.search).toBe("?view=accounts&id=work");
    expect(window.history.length).toBe(before);
    rerender(data([account("work")]));
    expect(h1("work")).toBeTruthy();
    // The first page shown takes no focus.
    expect(document.activeElement).toBe(document.body);
  });

  it("show the error, not Loading…, when the list cannot be loaded (#310)", () => {
    for (const url of ["?view=accounts&id=work", "?view=accounts&add=1", "?view=accounts&import=journal"]) {
      show(`/${url}`, null, true, "connection refused");
      expect(screen.getByRole("alert").textContent, url).toBe(t.loadAccountsFailed("connection refused"));
      expect(screen.queryByText(t.loading), url).toBeNull();
      cleanup();
    }
  });

  it("open the import page of a new smtp account once the list has it", async () => {
    const { rerender, reload } = show("/?view=accounts&add=1", data([]));
    fireEvent.click(screen.getByLabelText(new RegExp(t.sourceSMTP)));
    fireEvent.change(screen.getByLabelText(t.fieldName), { target: { value: "journal" } });
    fireEvent.change(screen.getByLabelText(`${t.fieldAddresses} 1`), { target: { value: "journal@archive.local" } });
    fireEvent.click(screen.getByRole("button", { name: t.save }));
    fireEvent.click(await screen.findByRole("button", { name: t.importFromIMAP }));
    expect(window.location.search).toBe("?view=accounts&import=journal");
    // The list is still the old one: the page waits instead of leaving.
    expect(screen.getByText(t.loading)).toBeTruthy();
    expect(reload).toHaveBeenCalled();
    rerender(data([smtp("journal")]));
    expect(h1(t.importFromIMAP)).toBeTruthy();
    expect(window.location.search).toBe("?view=accounts&import=journal");
  });
});

describe("the accounts table", () => {
  it("has one row per account, with the name as its row header and a link to the account's page", () => {
    show("/?view=accounts", data([account("work"), smtp("journal"), imported("files"), account("old", { removed: true })]));
    const table = within(screen.getByRole("table", { name: t.accountsTitle }));
    const headers = table.getAllByRole("rowheader");
    expect(headers.map((h) => within(h).getByRole("link").textContent)).toEqual(["work", "journal", "files"]);
    expect(headers.every((h) => h.tagName === "TH" && h.getAttribute("scope") === "row")).toBe(true);
    expect(within(row("journal")).getByRole("link", { name: "journal" }).getAttribute("href")).toBe("?view=accounts&id=journal");
  });

  it("shows each account's type, state, messages and last activity, and a second line for phones", () => {
    const failing = account("work", {
      sync: {
        state: "idle", failureStreak: 3, failingSince: "2026-10-01T12:00:00Z", health: "failing", syncEvery: "6h0m0s", nextAt: null,
        lastRun: { startedAt: "2026-10-06T10:00:00Z", finishedAt: "2026-10-06T10:01:00Z", fetched: 1, new: 0, status: "failed", error: "login: denied", reconciledFolders: 0, gone: 0, back: 0, flagsChanged: 0 },
      },
    });
    show("/?view=accounts", data([failing, smtp("journal", { enabled: false }), smtp("inbox"), imported("files")]));
    const work = within(row("work"));
    expect(work.getByRole("button", { name: t.sourceSnapshot })).toBeTruthy();
    expect(work.getByText(t.tagFailing)).toBeTruthy();
    expect(work.getByText(/failed$/, { selector: "td" }).className).toContain("app-text-error");
    expect(work.getByText(/^4 messages · Last sync .* failed$/).closest(".app-narrow-only")).toBeTruthy();
    const journal = within(row("journal"));
    expect(journal.getByRole("button", { name: t.sourceComplete })).toBeTruthy();
    expect(journal.getByText(t.disabled)).toBeTruthy();
    expect(journal.queryByText(t.receiving)).toBeNull();
    expect(within(row("inbox")).getByText(t.receiving)).toBeTruthy();
    expect(within(row("files")).getByText(t.imported)).toBeTruthy();
    const s = settingsText();
    const wide = screen.getAllByRole("columnheader").filter((h) => h.classList.contains("app-wide-only")).map((h) => h.textContent);
    expect(wide).toEqual([s.colType, t.messages, s.colLastActivity]);
  });

  it("keeps Sync visible on IMAP rows only, disabled while the account syncs", async () => {
    show("/?view=accounts", data([account("work"), busy(account("busy")), smtp("journal"), imported("files")]));
    const sync = within(row("work")).getByRole("button", { name: t.syncNow }) as HTMLButtonElement;
    expect((within(row("busy")).getByRole("button", { name: t.syncNow }) as HTMLButtonElement).disabled).toBe(true);
    expect(within(row("busy")).getByText(t.syncing)).toBeTruthy();
    expect(within(row("journal")).queryByRole("button", { name: t.syncNow })).toBeNull();
    expect(within(row("files")).queryByRole("button", { name: t.syncNow })).toBeNull();
    expect(sync.className).toContain("cds--btn--ghost");
    fireEvent.click(sync);
    await waitFor(() => expect(changes()).toEqual([{ method: "POST", url: "/api/accounts/work/sync", body: undefined }]));
  });

  it("holds exactly the actions of each kind in the row menu, Remove last", () => {
    show("/?view=accounts", data([account("work"), account("off", { enabled: false }), smtp("journal"), imported("files")]));
    expect(menu("work")).toEqual([[t.checkServer, false], [t.edit, false], [t.disable, false], [t.remove, false]]);
    fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
    expect(menu("off").map(([label]) => label)).toEqual([t.checkServer, t.edit, t.enable, t.remove]);
    fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
    expect(menu("journal")).toEqual([[t.mailServerSetup, false], [t.importFromIMAP, false], [t.edit, false], [t.disable, false], [t.remove, false]]);
    expect(screen.getByRole("menuitem", { name: t.remove }).className).toContain("danger");
    fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
    expect(menu("files")).toEqual([[t.remove, false]]);
  });

  it("keeps today's disabled states: Remove and Check server while busy, Import from IMAP while busy or while a fill runs", () => {
    const filling = smtp("filling", { fills: [fill()], fill: fill() });
    show("/?view=accounts", data([busy(account("work")), busy(smtp("journal")), filling]));
    expect(menu("work")).toEqual([[t.checkServer, true], [t.edit, false], [t.disable, false], [t.remove, true]]);
    fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
    expect(menu("journal")).toEqual([[t.mailServerSetup, false], [t.importFromIMAP, true], [t.edit, false], [t.disable, false], [t.remove, true]]);
    fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
    // The fill runs while the account itself is idle.
    expect(menu("filling")).toEqual([[t.mailServerSetup, false], [t.importFromIMAP, true], [t.edit, false], [t.disable, false], [t.remove, false]]);
  });

  it("checks the server and opens the account's page and the import from the menu", async () => {
    show("/?view=accounts", data([account("work"), smtp("journal")]));
    pick("work", t.checkServer);
    await waitFor(() => expect(changes()).toEqual([{ method: "POST", url: "/api/accounts/work/sync", body: { reconcile: true } }]));
    pick("journal", t.importFromIMAP);
    expect(window.location.search).toBe("?view=accounts&import=journal");
    expect(document.activeElement).toBe(h1(t.importFromIMAP));
    act(() => window.history.back());
    await waitFor(() => expect(h1(t.accountsTitle)).toBeTruthy());
    pick("work", t.edit);
    expect(window.location.search).toBe("?view=accounts&id=work");
    expect(document.activeElement).toBe(h1("work"));
  });

  it("offers smtp's Mail server setup without management, and it opens the page with the focus on that section (#285)", async () => {
    show("/?view=accounts", data([account("work"), smtp("journal")], { manage: false }), false);
    // Without management an IMAP row has no actions at all.
    expect(screen.queryByRole("button", { name: t.userActions("work") })).toBeNull();
    expect(within(row("work")).queryByRole("button", { name: t.syncNow })).toBeNull();
    expect(menu("journal")).toEqual([[t.mailServerSetup, false]]);
    fireEvent.click(screen.getByRole("menuitem", { name: t.mailServerSetup }));
    expect(window.location.search).toBe("?view=accounts&id=journal");
    expect(document.activeElement).toBe(screen.getByRole("heading", { level: 2, name: t.mailServerSetup }));
    // A one-shot request: Back and forth again lands on the h1.
    act(() => window.history.back());
    await waitFor(() => expect(document.activeElement).toBe(h1(t.accountsTitle)));
    act(() => window.history.forward());
    await waitFor(() => expect(document.activeElement).toBe(h1("journal")));
  });
});

describe("confirmations", () => {
  it("removes from a row only after the danger confirmation, then puts the focus on the list's h1 (#311)", async () => {
    const { rerender } = show("/?view=accounts", data([account("work"), account("old", { removed: true })]));
    pick("work", t.remove);
    expect(dialog().getByRole("heading", { name: t.confirmRemoveTitle("work") })).toBeTruthy();
    expect(dialog().getByText(t.confirmRemoveText)).toBeTruthy();
    // Danger dialogs start on the safe button.
    expect(document.activeElement).toBe(dialog().getByRole("button", { name: t.cancel }));
    expect(dialogOpen()).toBe(true);
    fireEvent.click(dialog().getByRole("button", { name: t.cancel }));
    expect(screen.queryByRole("dialog")).toBeNull();
    // Back on the menu's button.
    expect(document.activeElement).toBe(menuButton("work"));
    pick("work", t.remove);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(changes()).toEqual([]);

    const removedAccounts = screen.getByRole("button", { name: t.removedAccountsCount(1) });
    expect(removedAccounts.getAttribute("aria-expanded")).toBe("false");
    pick("work", t.remove);
    fireEvent.click(dialog().getByRole("button", { name: t.remove }));
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() => expect(changes()).toEqual([{ method: "DELETE", url: "/api/accounts/work", body: undefined }]));
    await waitFor(() => expect(document.activeElement).toBe(h1(t.accountsTitle)));
    // The removed accounts open, to show where it went.
    expect(removedAccounts.getAttribute("aria-expanded")).toBe("true");
    rerender(data([account("work", { removed: true }), account("old", { removed: true })]));
    expect(document.activeElement).toBe(h1(t.accountsTitle));
  });

  it("asks before disabling, not before enabling, and returns the focus to the menu's button", async () => {
    show("/?view=accounts", data([account("work"), smtp("journal"), account("off", { enabled: false })]));
    pick("journal", t.disable);
    expect(dialog().getByText(t.confirmDisableSMTPText)).toBeTruthy();
    fireEvent.click(dialog().getByRole("button", { name: t.cancel }));
    pick("work", t.disable);
    expect(dialog().getByText(t.confirmDisableText)).toBeTruthy();
    expect(changes()).toEqual([]);
    fireEvent.click(dialog().getByRole("button", { name: t.disable }));
    expect(document.activeElement).toBe(menuButton("work"));
    await waitFor(() => expect(changes()).toHaveLength(1));
    expect(changes()[0]).toEqual({ method: "PATCH", url: "/api/accounts/work", body: { enabled: false } });
    pick("off", t.enable);
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() => expect(changes()).toHaveLength(2));
    expect(changes()[1]).toEqual({ method: "PATCH", url: "/api/accounts/off", body: { enabled: true } });
  });

  it("does not send a Remove once the account started to sync while the dialog was open (#310)", () => {
    const { rerender } = show("/?view=accounts", data([account("work")]));
    pick("work", t.remove);
    rerender(data([busy(account("work"))]));
    const confirm = dialog().getByRole("button", { name: t.remove }) as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
    fireEvent.click(confirm);
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Enter" });
    expect(changes()).toEqual([]);
    // Once the sync is done, it can.
    rerender(data([account("work")]));
    expect((dialog().getByRole("button", { name: t.remove }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("removes from the account's page after confirming, then shows the list in its place", async () => {
    window.history.replaceState(null, "", "/?view=accounts");
    window.history.pushState(null, "", "/?view=accounts&id=work");
    const length = window.history.length;
    show("/?view=accounts&id=work", data([account("work")]));
    pick("work", t.remove);
    expect(changes()).toEqual([]);
    fireEvent.click(dialog().getByRole("button", { name: t.remove }));
    await waitFor(() => expect(window.location.search).toBe("?view=accounts"));
    expect(changes()).toEqual([{ method: "DELETE", url: "/api/accounts/work", body: undefined }]);
    // Replaced: Back does not lead to the removed account's page.
    expect(window.history.length).toBe(length);
    expect(document.activeElement).toBe(h1(t.accountsTitle));
  });

  it("stays on the page with the reason when the removal fails", async () => {
    answer = (_url, method) => (method === "DELETE" ? json({ error: "account is syncing" }, 409) : undefined);
    show("/?view=accounts&id=work", data([account("work")]));
    pick("work", t.remove);
    fireEvent.click(dialog().getByRole("button", { name: t.remove }));
    expect((await screen.findByRole("alert")).textContent).toBe(t.failed("account is syncing"));
    expect(window.location.search).toBe("?view=accounts&id=work");
  });
});

describe("the removed accounts", () => {
  it("are folded away, in a table with their note and messages", () => {
    show("/?view=accounts", data([account("work"), account("old", { removed: true })]));
    const toggle = screen.getByRole("button", { name: t.removedAccountsCount(1) });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByText(t.removedNote)).toBeTruthy();
    const table = within(screen.getByRole("table", { name: t.removedAccounts }));
    expect(table.getAllByRole("rowheader").map((h) => h.textContent)).toEqual(["old4 messages"]);
    expect(within(row("old")).getByRole("link", { name: "old" }).getAttribute("href")).toBe("?view=accounts&id=old");
  });

  it("open when a purge shows up after the first load (#310)", () => {
    const { rerender } = show("/?view=accounts", null);
    rerender(data([account("old", { removed: true, purging: true, purge })]));
    expect(screen.getByRole("button", { name: t.removedAccountsCount(1) }).getAttribute("aria-expanded")).toBe("true");
    expect(within(row("old")).getByRole("status").textContent).toBe(t.purgeRunning);
  });

  it("offer Delete completely to admins only", () => {
    show("/?view=accounts", data([account("old", { removed: true })]), false);
    expect(screen.queryByRole("button", { name: t.userActions("old") })).toBeNull();
    cleanup();
    show("/?view=accounts", data([account("old", { removed: true })]));
    expect(menu("old")).toEqual([[t.purgeStart, false]]);
  });

  it("put the focus back on the menu's button when the purge dialog closes without confirming", async () => {
    show("/?view=accounts", data([account("old", { removed: true })]));
    pick("old", t.purgeStart);
    const field = await screen.findByLabelText(t.purgeTypeCount(3));
    expect(document.activeElement).toBe(field);
    fireEvent.click(dialog().getByRole("button", { name: t.cancel }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(document.activeElement).toBe(menuButton("old"));
    expect(changes()).toEqual([]);
  });

  it("put the focus on the row's name after confirming, then on the accordion, then on the h1 as the rows go (#311)", async () => {
    const { rerender, reload } = show("/?view=accounts", data([account("old", { removed: true }), account("older", { removed: true })]));
    fireEvent.click(screen.getByRole("button", { name: t.removedAccountsCount(2) }));
    pick("old", t.purgeStart);
    fireEvent.change(await screen.findByLabelText(t.purgeTypeCount(3)), { target: { value: "3" } });
    fireEvent.click(dialog().getByRole("button", { name: t.purgeConfirm }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(changes()).toEqual([{ method: "POST", url: "/api/accounts/old/purge", body: { count: 3 } }]);
    expect(reload).toHaveBeenCalled();
    const link = within(row("old")).getByRole("link", { name: "old" });
    expect(document.activeElement).toBe(link);
    // The list says it is being deleted: the row keeps the focus and has no menu any more.
    rerender(data([account("old", { removed: true, purging: true, purge }), account("older", { removed: true })]));
    expect(document.activeElement).toBe(link);
    expect(screen.queryByRole("button", { name: t.userActions("old") })).toBeNull();
    // It is done: the row goes, the accordion's button gets the focus.
    rerender(data([account("older", { removed: true })]));
    expect(document.activeElement).toBe(screen.getByRole("button", { name: t.removedAccountsCount(1) }));
    // The last removed account goes while a row has the focus: the h1 gets it.
    within(row("older")).getByRole("link", { name: "older" }).focus();
    rerender(data([account("work")]));
    expect(document.activeElement).toBe(h1(t.accountsTitle));
  });

  it("say when a purge was cancelled", () => {
    const { rerender } = show("/?view=accounts", data([account("old", { removed: true, purging: true, purge })]));
    expect(screen.queryByText(t.purgeCancelled)).toBeNull();
    rerender(data([account("old", { removed: true })]));
    expect(within(row("old")).getByRole("status").textContent).toBe(t.purgeCancelled);
    expect(menu("old")).toEqual([[t.purgeStart, false]]);
  });
});

describe("the account's page", () => {
  it("shows the title row, the tiles with the last error text, and Check server with its hint", () => {
    const work = account("work", {
      goneMessages: 2,
      lastReconciledAt: "2026-10-06T10:01:00Z",
      folders: [
        { name: "INBOX", messages: 4, lastSyncedAt: null, lastReconciledAt: null, delimiter: "/" },
        { name: "Sent", messages: 1, lastSyncedAt: null, lastReconciledAt: null, delimiter: "/" },
      ],
      excludedFolders: ["Trash"],
      sync: {
        state: "idle", failureStreak: 3, failingSince: "2026-10-01T12:00:00Z", health: "failing", syncEvery: "6h0m0s", nextAt: null,
        lastRun: { startedAt: "2026-10-06T10:00:00Z", finishedAt: "2026-10-06T10:01:00Z", fetched: 1, new: 0, status: "failed", error: "login: denied", reconciledFolders: 0, gone: 0, back: 0, flagsChanged: 0 },
      },
    });
    const { showGone } = show("/?view=accounts&id=work", data([work]));
    const s = settingsText();
    expect(screen.getByRole("button", { name: t.sourceSnapshot })).toBeTruthy();
    expect(screen.getByText(t.tagFailing)).toBeTruthy();
    const tiles = [...document.querySelectorAll(".app-summary dt")].map((d) => d.textContent);
    expect(tiles).toEqual([t.messages, t.onlyInArchive, s.foldersArchived, s.lastSync]);
    const tile = (label: string) => within(screen.getByText(label, { selector: "dt" }).closest(".app-summary__tile") as HTMLElement);
    expect(tile(t.messages).getByText("5")).toBeTruthy();
    expect(tile(s.foldersArchived).getByText("2")).toBeTruthy();
    expect(tile(s.lastSync).getByText(/failed$/)).toBeTruthy();
    // The last error text is under the Last sync tile's value.
    expect(tile(s.lastSync).getByText("login: denied")).toBeTruthy();
    const gone = tile(t.onlyInArchive).getByRole("link", { name: "2" });
    expect(gone.getAttribute("href")).toBe("?account=work&gone=1");
    fireEvent.click(gone);
    expect(showGone).toHaveBeenCalledWith("work");
    expect(screen.getByText(/^Failed 3 times in a row since /)).toBeTruthy();
    expect(screen.getByText(/^Last compared with the server /)).toBeTruthy();
    const check = screen.getByRole("button", { name: t.checkServer });
    expect(check.getAttribute("title")).toBe(t.checkServerHint);
    expect(check.className).toContain("cds--btn--tertiary");
    // Sync is the page's one primary.
    expect([...document.querySelectorAll(".cds--btn--primary")].map((b) => b.textContent)).toEqual([t.syncNow]);
    expect(menu("work")).toEqual([[t.disable, false], [t.remove, false]]);
    fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
    // The read views of the sections.
    expect(section(s.connection).getByText("work@example.com")).toBeTruthy();
    expect(section(t.scheduleTitle).getByText(/^Every 6 hours \(default\)/)).toBeTruthy();
    expect(section(t.folders).getByText("INBOX")).toBeTruthy();
    expect(section(t.folders).getByText(s.notArchived("Trash"))).toBeTruthy();
  });

  it("syncs and checks the server from the title row, disabled while busy", async () => {
    const { rerender } = show("/?view=accounts&id=work", data([account("work")]));
    fireEvent.click(screen.getByRole("button", { name: t.syncNow }));
    await waitFor(() => expect(changes()).toEqual([{ method: "POST", url: "/api/accounts/work/sync", body: undefined }]));
    fireEvent.click(screen.getByRole("button", { name: t.checkServer }));
    await waitFor(() => expect(changes()).toHaveLength(2));
    expect(changes()[1]!.body).toEqual({ reconcile: true });
    rerender(data([busy(account("work"))]));
    expect((screen.getByRole("button", { name: t.syncNow }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: t.checkServer }) as HTMLButtonElement).disabled).toBe(true);
    expect(menu("work")).toEqual([[t.disable, false], [t.remove, true]]);
  });

  it("is read only without management", () => {
    show("/?view=accounts&id=work", data([account("work")], { manage: false }), false);
    expect(h1("work")).toBeTruthy();
    expect(screen.queryByRole("button", { name: t.syncNow })).toBeNull();
    expect(screen.queryByRole("button", { name: t.userActions("work") })).toBeNull();
    expect(screen.queryByRole("button", { name: t.edit })).toBeNull();
  });

  it("opens a section's form in place with the focus on its first field, and Cancel returns it to Edit", () => {
    show("/?view=accounts&id=work", data([account("work")]));
    const s = settingsText();
    const edit = section(s.connection).getByRole("button", { name: t.edit });
    fireEvent.click(edit);
    expect(document.activeElement).toBe(section(s.connection).getByLabelText(t.fieldName));
    expect(section(s.connection).getByRole("button", { name: t.save }).className).toContain("cds--btn--tertiary");
    const cancel = section(s.connection).getByRole("button", { name: t.cancel });
    expect(cancel.className).toContain("cds--btn--ghost");
    fireEvent.click(cancel);
    expect(section(s.connection).queryByLabelText(t.fieldName)).toBeNull();
    expect(document.activeElement).toBe(section(s.connection).getByRole("button", { name: t.edit }));
    fireEvent.click(section(t.scheduleTitle).getByRole("button", { name: t.edit }));
    expect(document.activeElement).toBe(section(t.scheduleTitle).getByLabelText(t.scheduleTitle));
    expect(changes()).toEqual([]);
  });

  // The #188 follow-up: saving one section used to leave the page and drop
  // the unsaved choices of the others.
  it("saves Connection alone and keeps the unsaved schedule and folder choices", async () => {
    show("/?view=accounts&id=work", data([account("work")]));
    const s = settingsText();
    fireEvent.click(section(t.scheduleTitle).getByRole("button", { name: t.edit }));
    fireEvent.change(section(t.scheduleTitle).getByLabelText(t.scheduleTitle), { target: { value: "30m0s" } });
    fireEvent.click(section(t.folders).getByRole("button", { name: t.edit }));
    const trash = await section(t.folders).findByRole("checkbox", { name: /Trash/ });
    fireEvent.click(trash);
    fireEvent.click(section(s.connection).getByRole("button", { name: t.edit }));
    fireEvent.change(section(s.connection).getByLabelText(t.fieldHost), { target: { value: "imap.other.example" } });
    fireEvent.click(section(s.connection).getByRole("button", { name: t.save }));
    await waitFor(() => expect(section(s.connection).getByText(t.saved)).toBeTruthy());
    // Only the connection was sent, and the page stays.
    expect(changes()).toEqual([
      { method: "PATCH", url: "/api/accounts/work", body: { host: "imap.other.example", tls: "tls", port: 993, username: "work@example.com" } },
    ]);
    expect(window.location.search).toBe("?view=accounts&id=work");
    expect((section(t.scheduleTitle).getByLabelText(t.scheduleTitle) as HTMLSelectElement).value).toBe("30m0s");
    expect((section(t.scheduleTitle).getByRole("button", { name: t.saveSchedule }) as HTMLButtonElement).disabled).toBe(false);
    expect((section(t.folders).getByRole("checkbox", { name: /Trash/ }) as HTMLInputElement).checked).toBe(false);
  });

  it("saves Schedule alone while Connection is being edited, and Folders alone while Schedule is", async () => {
    show("/?view=accounts&id=work", data([account("work")]));
    const s = settingsText();
    fireEvent.click(section(s.connection).getByRole("button", { name: t.edit }));
    fireEvent.change(section(s.connection).getByLabelText(t.fieldUsername), { target: { value: "other@example.com" } });
    fireEvent.click(section(t.scheduleTitle).getByRole("button", { name: t.edit }));
    fireEvent.change(section(t.scheduleTitle).getByLabelText(t.scheduleTitle), { target: { value: "manual" } });
    fireEvent.click(section(t.scheduleTitle).getByRole("button", { name: t.saveSchedule }));
    await waitFor(() => expect(section(t.scheduleTitle).getByText(t.saved)).toBeTruthy());
    expect(changes()).toEqual([{ method: "PATCH", url: "/api/accounts/work", body: { syncInterval: "manual" } }]);
    expect((section(s.connection).getByLabelText(t.fieldUsername) as HTMLInputElement).value).toBe("other@example.com");

    fireEvent.click(section(t.scheduleTitle).getByRole("button", { name: t.edit }));
    fireEvent.change(section(t.scheduleTitle).getByLabelText(t.scheduleTitle), { target: { value: "1h0m0s" } });
    fireEvent.click(section(t.folders).getByRole("button", { name: t.edit }));
    fireEvent.click(await section(t.folders).findByRole("checkbox", { name: /Trash/ }));
    fireEvent.click(section(t.folders).getByRole("button", { name: t.saveFolders }));
    await waitFor(() => expect(section(t.folders).getByText(t.saved)).toBeTruthy());
    expect(changes()[1]).toEqual({ method: "PATCH", url: "/api/accounts/work", body: { excludedFolders: ["Trash"] } });
    expect(changes()).toHaveLength(2);
    expect((section(t.scheduleTitle).getByLabelText(t.scheduleTitle) as HTMLSelectElement).value).toBe("1h0m0s");
    expect((section(s.connection).getByLabelText(t.fieldUsername) as HTMLInputElement).value).toBe("other@example.com");
  });

  it("keeps an open draft when the account list reloads", () => {
    const { rerender } = show("/?view=accounts&id=work", data([account("work")]));
    const s = settingsText();
    fireEvent.click(section(s.connection).getByRole("button", { name: t.edit }));
    fireEvent.change(section(s.connection).getByLabelText(t.fieldHost), { target: { value: "imap.typed.example" } });
    fireEvent.click(section(t.scheduleTitle).getByRole("button", { name: t.edit }));
    fireEvent.change(section(t.scheduleTitle).getByLabelText(t.scheduleTitle), { target: { value: "12h0m0s" } });
    rerender(data([account("work", { host: "imap.changed.example", sync: { ...account("w").sync, state: "running" } })]));
    rerender(data([account("work", { host: "imap.changed.example" })]));
    expect((section(s.connection).getByLabelText(t.fieldHost) as HTMLInputElement).value).toBe("imap.typed.example");
    expect((section(t.scheduleTitle).getByLabelText(t.scheduleTitle) as HTMLSelectElement).value).toBe("12h0m0s");
  });

  it("keeps the page and the other drafts after a rename, and saves them once the list has the new name", async () => {
    const { rerender, renamed } = show("/?view=accounts&id=work", data([account("work")]));
    const s = settingsText();
    fireEvent.click(section(t.scheduleTitle).getByRole("button", { name: t.edit }));
    fireEvent.change(section(t.scheduleTitle).getByLabelText(t.scheduleTitle), { target: { value: "30m0s" } });
    const length = window.history.length;
    fireEvent.click(section(s.connection).getByRole("button", { name: t.edit }));
    fireEvent.change(section(s.connection).getByLabelText(t.fieldName), { target: { value: "office" } });
    fireEvent.click(section(s.connection).getByRole("button", { name: t.save }));
    await waitFor(() => expect(window.location.search).toBe("?view=accounts&id=office"));
    expect(changes()[0]).toMatchObject({ method: "PATCH", url: "/api/accounts/work", body: { name: "office" } });
    expect(renamed).toHaveBeenCalledWith("work", "office");
    // The new id replaced the old one; the page and its drafts stay while the list is the old one.
    expect(window.history.length).toBe(length);
    expect(h1("work")).toBeTruthy();
    const save = section(t.scheduleTitle).getByRole("button", { name: t.saveSchedule }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    expect((section(t.scheduleTitle).getByLabelText(t.scheduleTitle) as HTMLSelectElement).value).toBe("30m0s");
    rerender(data([account("office")]));
    expect(h1("office")).toBeTruthy();
    // Nothing moved the focus: the same page.
    expect(document.activeElement).not.toBe(h1("office"));
    expect((section(t.scheduleTitle).getByLabelText(t.scheduleTitle) as HTMLSelectElement).value).toBe("30m0s");
    fireEvent.click(section(t.scheduleTitle).getByRole("button", { name: t.saveSchedule }));
    await waitFor(() => expect(changes()).toHaveLength(2));
    expect(changes()[1]).toEqual({ method: "PATCH", url: "/api/accounts/office", body: { syncInterval: "30m0s" } });
  });

  it("tells a failed save from a saved one by its state, not by the text (#310)", async () => {
    // A server whose error reads like the success text.
    answer = (_url, method) => (method === "PATCH" ? json({ error: "Saved." }, 500) : undefined);
    show("/?view=accounts&id=work", data([account("work")]));
    fireEvent.click(section(t.scheduleTitle).getByRole("button", { name: t.edit }));
    fireEvent.change(section(t.scheduleTitle).getByLabelText(t.scheduleTitle), { target: { value: "30m0s" } });
    fireEvent.click(section(t.scheduleTitle).getByRole("button", { name: t.saveSchedule }));
    const error = await section(t.scheduleTitle).findByText(t.failed("Saved."));
    expect(error.className).toContain("app-text-error");
    // The form stays open with the choice.
    expect((section(t.scheduleTitle).getByLabelText(t.scheduleTitle) as HTMLSelectElement).value).toBe("30m0s");
    fireEvent.click(section(t.folders).getByRole("button", { name: t.edit }));
    fireEvent.click(await section(t.folders).findByRole("button", { name: t.saveFolders }));
    expect((await section(t.folders).findByText(t.failed("Saved."))).className).toContain("app-text-error");
    expect(section(t.folders).getByRole("checkbox", { name: "INBOX" })).toBeTruthy();
  });

  it("shows a failed folder load in the error colour, with Cancel", async () => {
    answer = (url) => (url.endsWith("/server-folders") ? json({ error: "login failed" }, 502) : undefined);
    show("/?view=accounts&id=work", data([account("work")]));
    fireEvent.click(section(t.folders).getByRole("button", { name: t.edit }));
    expect(document.activeElement).toBe(screen.getByRole("heading", { level: 2, name: t.folders }));
    const message = await section(t.folders).findByText(/login failed/);
    expect(message.className).toContain("app-text-error");
    expect(section(t.folders).queryByRole("button", { name: t.saveFolders })).toBeNull();
    fireEvent.click(section(t.folders).getByRole("button", { name: t.cancel }));
    expect(section(t.folders).getByRole("button", { name: t.edit })).toBeTruthy();
  });

  it("asks about trash and spam without a second primary button", async () => {
    answer = (_url, method) => (method === "PATCH" ? json({ error: "confirm", suggestedExclusions: ["Trash"] }, 409) : undefined);
    show("/?view=accounts&id=work", data([account("work")]));
    const s = settingsText();
    fireEvent.click(section(s.connection).getByRole("button", { name: t.edit }));
    fireEvent.click(section(s.connection).getByRole("button", { name: t.save }));
    await section(s.connection).findByRole("button", { name: t.saveWithout });
    expect([...document.querySelectorAll(".cds--btn--primary")].map((b) => b.textContent)).toEqual([t.syncNow]);
  });

  it("shows an smtp account's connection, its mail server setup without management, and its imports", () => {
    show("/?view=accounts&id=journal", data([smtp("journal", { lastReceivedAt: null })], { manage: false }), false);
    const s = settingsText();
    expect(section(s.connection).getByText("journal@archive.local")).toBeTruthy();
    expect(section(s.connection).queryByRole("button", { name: t.edit })).toBeNull();
    expect(section(t.mailServerSetup).getByText(t.setupIntro)).toBeTruthy();
    expect(screen.queryByRole("region", { name: s.imports })).toBeNull();
    const tiles = [...document.querySelectorAll(".app-summary dt")].map((d) => d.textContent);
    expect(tiles).toEqual([t.messages, s.foldersArchived, s.lastReceived]);
    expect(screen.getByText(t.neverReceived)).toBeTruthy();
    expect(screen.queryByRole("button", { name: t.syncNow })).toBeNull();
    expect(screen.queryByRole("button", { name: t.checkServer })).toBeNull();
  });

  it("lets an smtp account's owner rename it but not change its addresses; the imports link to the import page", async () => {
    show("/?view=accounts&id=journal", data([smtp("journal")]), false);
    const s = settingsText();
    expect(menu("journal")).toEqual([[t.disable, false], [t.remove, false]]);
    fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
    fireEvent.click(section(s.connection).getByRole("button", { name: t.edit }));
    expect((section(s.connection).getByLabelText(`${t.fieldAddresses} 1`) as HTMLInputElement).disabled).toBe(true);
    expect(section(s.connection).getByRole("button", { name: t.save }).className).toContain("cds--btn--tertiary");
    fireEvent.click(section(s.imports).getByRole("button", { name: t.importFromIMAP }));
    expect(window.location.search).toBe("?view=accounts&import=journal");
  });

  it("shows an import account's tiles and folders, read only, with Remove in the menu", () => {
    show("/?view=accounts&id=files", data([imported("files")]));
    expect(screen.getByText(t.imported)).toBeTruthy();
    expect(section(t.folders).getByText("INBOX")).toBeTruthy();
    expect(screen.queryByRole("button", { name: t.edit })).toBeNull();
    expect(screen.queryByRole("region", { name: settingsText().connection })).toBeNull();
    expect(menu("files")).toEqual([[t.remove, false]]);
  });

  it("shows a removed account with its note; after confirming a purge the focus is on the h1, and the list opens when it is gone", async () => {
    const { rerender } = show("/?view=accounts&id=old", data([account("old", { removed: true })]));
    expect(screen.getByText(t.removed)).toBeTruthy();
    expect(screen.getByText(t.removedNote)).toBeTruthy();
    expect(screen.queryByRole("button", { name: t.syncNow })).toBeNull();
    expect(screen.queryByRole("region")).toBeNull();
    pick("old", t.purgeStart);
    await screen.findByLabelText(t.purgeTypeCount(3));
    fireEvent.click(dialog().getByRole("button", { name: t.cancel }));
    expect(document.activeElement).toBe(menuButton("old"));
    pick("old", t.purgeStart);
    fireEvent.change(await screen.findByLabelText(t.purgeTypeCount(3)), { target: { value: "3" } });
    fireEvent.click(dialog().getByRole("button", { name: t.purgeConfirm }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(document.activeElement).toBe(h1("old"));
    rerender(data([account("old", { removed: true, purging: true, purge })]));
    expect(screen.getByRole("status").textContent).toBe(t.purgeRunning);
    expect(screen.queryByRole("button", { name: t.userActions("old") })).toBeNull();
    rerender(data([account("old", { removed: true })]));
    expect(screen.getByText(t.purgeCancelled)).toBeTruthy();
    rerender(data([account("old", { removed: true, purging: true, purge })]));
    rerender(data([account("work")]));
    expect(window.location.search).toBe("?view=accounts");
    expect(document.activeElement).toBe(h1(t.accountsTitle));
  });
});

describe("page details", () => {
  it("describe the cut-off with its hint and, once set, the gap warning", () => {
    show("/?view=accounts&import=journal", data([smtp("journal")]));
    const cutoff = screen.getByLabelText(t.fieldCutoff);
    const described = () => (cutoff.getAttribute("aria-describedby") ?? "").split(" ").map((id) => document.getElementById(id)?.textContent);
    expect(described()).toEqual([t.cutoffHint]);
    fireEvent.change(cutoff, { target: { value: "2026-10-01T08:30" } });
    expect(described()).toEqual([t.cutoffHint, t.cutoffGap]);
  });

  it("keep one primary button while the trash/spam question is open (#211)", async () => {
    answer = () => json({ error: "confirm", suggestedExclusions: ["Trash"] }, 409);
    show("/?view=accounts&add=1", data([]), false);
    const kind = (name: string) => screen.getByRole("button", { name }).className;
    expect(kind(t.save)).toContain("cds--btn--primary");
    fireEvent.change(screen.getByLabelText(t.fieldHost), { target: { value: "imap.example.com" } });
    fireEvent.change(screen.getByLabelText(t.fieldUsername), { target: { value: "alice@example.com" } });
    fireEvent.change(screen.getByLabelText(t.fieldPassword), { target: { value: "secret" } });
    fireEvent.click(screen.getByRole("button", { name: t.save }));
    await screen.findByRole("button", { name: t.saveWithout });
    expect(kind(t.saveWithout)).toContain("cds--btn--primary");
    expect(kind(t.save)).toContain("cds--btn--tertiary");
    expect(document.querySelectorAll(".cds--btn--primary")).toHaveLength(1);
  });
});

describe("German", () => {
  // Carbon's own English defaults that must not show in German.
  const english = [/\bClose\b/, /Copy to clipboard/, /Copied/, /Show more/, /Show less/, /Breadcrumb/, /\bLoading\b/, /\bloading\b/,
    /Show password/, /Hide password/, /Focus sentinel/, /\bdanger\b/, /close notification/i, /\bnotification\b/, /Options/];

  // What a person or a screen reader gets: the text, and the attributes
  // that carry text (class names say "danger" too, but nobody reads them).
  function readable(): string {
    const attrs = ["aria-label", "title", "placeholder", "alt", "aria-valuetext", "aria-description"];
    const values = [...document.body.querySelectorAll("*")].flatMap((el) => attrs.map((a) => el.getAttribute(a) ?? ""));
    return [document.body.textContent ?? "", ...values].join("\n");
  }

  function noEnglish() {
    const text = readable();
    for (const e of english) expect(text, String(e)).not.toMatch(e);
  }

  it("shows the table, the menus, the pages and the dialogs in German", async () => {
    applySettings({ language: "de", locale: "de" });
    try {
      const de = messagesFor("de");
      const s = settingsTextFor("de");
      const running = smtp("journal", { fills: [fill()], fill: fill(), sync: { ...account("j").sync, state: "running" } });
      const d = data([
        account("work", {
          goneMessages: 1,
          excludedFolders: ["Spam"],
          sync: { ...account("w").sync, failureStreak: 3, failingSince: "2026-10-01T12:00:00Z", health: "failing" },
        }),
        running,
        account("old", { removed: true, purging: true, purge }),
        account("older", { removed: true }),
      ]);
      show("/?view=accounts", d);
      expect(h1(de.accountsTitle)).toBeTruthy();
      expect(screen.getByRole("columnheader", { name: s.colLastActivity })).toBeTruthy();
      fireEvent.click(screen.getByRole("button", { name: de.removedAccountsCount(2) }));
      fireEvent.click(screen.getByRole("button", { name: de.userActions("work") }));
      expect(screen.getByRole("menuitem", { name: de.checkServer })).toBeTruthy();
      fireEvent.click(screen.getByRole("menuitem", { name: de.remove }));
      expect(screen.getByRole("button", { name: de.carbon.closeModal })).toBeTruthy();
      noEnglish();
      cleanup();

      show("/?view=accounts&id=work", d);
      expect(screen.getByRole("navigation", { name: de.carbon.breadcrumb })).toBeTruthy();
      expect(screen.getByText(s.foldersArchived)).toBeTruthy();
      expect(screen.getByText(s.notArchived("Spam"))).toBeTruthy();
      fireEvent.click(section(s.connection).getByRole("button", { name: de.edit }));
      expect(screen.getByRole("button", { name: de.carbon.showPassword })).toBeTruthy();
      fireEvent.click(section(de.folders).getByRole("button", { name: de.edit }));
      await section(de.folders).findByRole("checkbox", { name: "INBOX" });
      noEnglish();
      cleanup();

      show("/?view=accounts&id=journal", d);
      expect(screen.getByRole("progressbar", { name: de.fillProgress })).toBeTruthy();
      expect(section(de.mailServerSetup).getByRole("button", { name: de.carbon.copy })).toBeTruthy();
      expect(screen.getByRole("region", { name: s.imports })).toBeTruthy();
      noEnglish();
      cleanup();

      show("/?view=accounts&id=old", d);
      expect(screen.getByRole("status").textContent).toBe(de.purgeRunning);
      noEnglish();
      cleanup();

      show("/?view=accounts&add=1", d);
      fireEvent.click(screen.getByLabelText(new RegExp(de.sourceSMTP)));
      fireEvent.change(screen.getByLabelText(de.fieldName), { target: { value: "neu" } });
      fireEvent.change(screen.getByLabelText(`${de.fieldAddresses} 1`), { target: { value: "neu@archive.local" } });
      fireEvent.click(screen.getByRole("button", { name: de.addAddress }));
      expect(screen.getByRole("button", { name: de.removeAddress("neu@archive.local") })).toBeTruthy();
      noEnglish();
      cleanup();

      render(<PostfixPanel addresses={["journal@example.com"]} />);
      noEnglish();
      cleanup();

      render(<PurgeReview preview={preview()} busy error="" onConfirm={() => {}} onCancel={() => {}} />);
      expect(screen.getByRole("button", { name: de.cancel })).toBeTruthy();
      noEnglish();
    } finally {
      applySettings({ language: "en", locale: "en" });
    }
  });
});
