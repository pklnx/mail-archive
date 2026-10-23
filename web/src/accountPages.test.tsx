// The Carbon Accounts page (#168 part 3): the pages under Accounts in the
// URL, the danger confirmations of the list, and the German texts of
// Carbon's components.
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Account, AccountsResponse, PurgePreview } from "./api";
import { AccountsPage, PostfixPanel } from "./AccountsPage";
import { PurgeReview } from "./AccountPurge";
import { dialogOpen } from "./dialogs";
import { fill } from "./fillFixture";
import { applySettings, messagesFor, t } from "./i18n";

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

const data = (accounts: Account[], more: Partial<AccountsResponse> = {}): AccountsResponse => ({
  accounts, manage: true, syncInterval: "6h0m0s", syncIntervalMin: "5m0s", reconcileInterval: "24h0m0s", alertAfter: 3, smtp: {}, ...more,
});

let requests: { method: string; url: string; body: unknown }[];

beforeEach(() => {
  requests = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (url, init) => {
    requests.push({ method: init?.method ?? "GET", url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined });
    if (String(url).endsWith("/api/accounts") && init?.method === "POST") return new Response(JSON.stringify({ name: "journal" }), { status: 201 });
    return new Response(null, { status: 204 });
  });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  window.history.replaceState(null, "", "/");
});

function show(url: string, d: AccountsResponse, admin = true) {
  window.history.replaceState(null, "", url);
  const reload = vi.fn();
  const renamed = vi.fn();
  const view = render(<AccountsPage accounts={{ data: d, error: "", reload }} admin={admin} renamed={renamed} showGone={() => {}} />);
  const rerender = (next: AccountsResponse) =>
    view.rerender(<AccountsPage accounts={{ data: next, error: "", reload }} admin={admin} renamed={renamed} showGone={() => {}} />);
  return { reload, renamed, rerender };
}

const heading = (name: string) => screen.getByRole("heading", { level: 1, name });

describe("the pages under Accounts", () => {
  it("open from the list, with a breadcrumb back, and follow Back", async () => {
    show("/?view=accounts", data([account("work")]));
    expect(heading(t.accountsTitle)).toBeTruthy();
    fireEvent.click(within(screen.getByRole("region", { name: "work" })).getByRole("button", { name: t.edit }));
    expect(window.location.search).toBe("?view=accounts&edit=work");
    expect(heading(t.editAccountTitle("work"))).toBeTruthy();
    expect((screen.getByLabelText(t.fieldHost) as HTMLInputElement).value).toBe("imap.example.com");
    const crumbs = screen.getByRole("navigation", { name: t.carbon.breadcrumb });
    fireEvent.click(within(crumbs).getByRole("link", { name: t.accountsTitle }));
    expect(window.location.search).toBe("?view=accounts");
    expect(heading(t.accountsTitle)).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: t.addAccount }));
    expect(window.location.search).toBe("?view=accounts&add=1");
    expect(heading(t.addAccount)).toBeTruthy();
    // Back returns to the list: the page was a history entry.
    act(() => window.history.back());
    await waitFor(() => expect(heading(t.accountsTitle)).toBeTruthy());
    expect(window.location.search).toBe("?view=accounts");
  });

  it("open from the URL, so a reload keeps them", () => {
    show("/?view=accounts&import=journal", data([smtp("journal")]));
    expect(heading(t.importFromIMAP)).toBeTruthy();
    expect(screen.getByRole("button", { name: t.loadFillFolders })).toBeTruthy();
  });

  it("return to the list for an account that does not exist or does not fit", () => {
    const d = data([account("work"), smtp("journal"), account("files", { kind: "import" }), account("old", { removed: true })]);
    for (const url of ["?view=accounts&edit=nope", "?view=accounts&import=work", "?view=accounts&edit=files", "?view=accounts&edit=old"]) {
      show(`/${url}`, d);
      expect(window.location.search, url).toBe("?view=accounts");
      expect(heading(t.accountsTitle)).toBeTruthy();
      cleanup();
    }
    // Without management (no secret key) there is nothing to add or edit.
    show("/?view=accounts&add=1", data([account("work")], { manage: false }));
    expect(window.location.search).toBe("?view=accounts");
  });

  it("wait while the list loads, then decide", () => {
    window.history.replaceState(null, "", "/?view=accounts&edit=work");
    const view = render(<AccountsPage accounts={{ data: null, error: "", reload: () => {} }} admin renamed={() => {}} showGone={() => {}} />);
    expect(screen.getByText(t.loading)).toBeTruthy();
    expect(window.location.search).toBe("?view=accounts&edit=work");
    view.rerender(<AccountsPage accounts={{ data: data([account("work")]), error: "", reload: () => {} }} admin renamed={() => {}} showGone={() => {}} />);
    expect(heading(t.editAccountTitle("work"))).toBeTruthy();
  });

  it("open the import page of a new smtp account once the list has it", async () => {
    const before = data([]);
    const { rerender, reload } = show("/?view=accounts&add=1", before);
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
    expect(heading(t.importFromIMAP)).toBeTruthy();
    expect(window.location.search).toBe("?view=accounts&import=journal");
  });

  it("go back to the list after saving, and report a new name", async () => {
    const { renamed } = show("/?view=accounts&edit=work", data([account("work")]));
    fireEvent.change(screen.getByLabelText(t.fieldName), { target: { value: "office" } });
    fireEvent.click(screen.getByRole("button", { name: t.save }));
    await waitFor(() => expect(window.location.search).toBe("?view=accounts"));
    expect(requests[0]).toMatchObject({ method: "PATCH", url: "/api/accounts/work", body: { name: "office" } });
    expect(renamed).toHaveBeenCalledWith("work", "office");
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

  it("show a failed folder load in the error colour", async () => {
    vi.mocked(globalThis.fetch).mockResolvedValue(new Response(JSON.stringify({ error: "login failed" }), { status: 502 }));
    show("/?view=accounts&edit=work", data([account("work")]));
    fireEvent.click(screen.getByRole("button", { name: t.loadFolders }));
    const message = await screen.findByText(/login failed/);
    expect(message.className).toBe("app-text-error");
  });
});

describe("confirmations on the list", () => {
  const card = (name: string) => within(screen.getByRole("region", { name }));
  const dialog = () => within(screen.getByRole("dialog"));

  it("removes only after the danger confirmation", async () => {
    show("/?view=accounts", data([account("work")]));
    const open = card("work").getByRole("button", { name: t.remove });
    fireEvent.click(open);
    expect(dialog().getByRole("heading", { name: t.confirmRemoveTitle("work") })).toBeTruthy();
    expect(dialog().getByText(t.confirmRemoveText)).toBeTruthy();
    // Danger dialogs start on the safe button.
    expect(document.activeElement).toBe(dialog().getByRole("button", { name: t.cancel }));
    expect(dialogOpen()).toBe(true);
    fireEvent.click(dialog().getByRole("button", { name: t.cancel }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(requests).toEqual([]);

    fireEvent.click(open);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(requests).toEqual([]);

    fireEvent.click(open);
    fireEvent.click(dialog().getByRole("button", { name: t.remove }));
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() => expect(requests).toEqual([{ method: "DELETE", url: "/api/accounts/work", body: undefined }]));
  });

  it("asks before disabling, not before enabling", async () => {
    show("/?view=accounts", data([account("work"), smtp("journal"), account("off", { enabled: false })]));
    fireEvent.click(card("journal").getByRole("button", { name: t.disable }));
    expect(dialog().getByText(t.confirmDisableSMTPText)).toBeTruthy();
    fireEvent.click(dialog().getByRole("button", { name: t.cancel }));
    fireEvent.click(card("work").getByRole("button", { name: t.disable }));
    expect(dialog().getByText(t.confirmDisableText)).toBeTruthy();
    expect(requests).toEqual([]);
    fireEvent.click(dialog().getByRole("button", { name: t.disable }));
    await waitFor(() => expect(requests).toHaveLength(1));
    expect(requests[0]).toEqual({ method: "PATCH", url: "/api/accounts/work", body: { enabled: false } });
    fireEvent.click(card("off").getByRole("button", { name: t.enable }));
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() => expect(requests).toHaveLength(2));
    expect(requests[1]).toEqual({ method: "PATCH", url: "/api/accounts/off", body: { enabled: true } });
  });

  it("keeps Remove disabled while the account syncs", () => {
    show("/?view=accounts", data([account("work", { sync: { ...account("w").sync, state: "running" } })]));
    expect((card("work").getByRole("button", { name: t.remove }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("folds the removed accounts away", () => {
    show("/?view=accounts", data([account("work"), account("old", { removed: true })]));
    const toggle = screen.getByRole("button", { name: t.removedAccountsCount(1) });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(within(screen.getByRole("region", { name: "old" })).getByText(t.removedNote)).toBeTruthy();
  });
});

describe("the account tiles", () => {
  it("show the source, the state and the failure streak", () => {
    const failing = account("work", {
      sync: { state: "idle", lastRun: null, failureStreak: 3, failingSince: "2026-10-01T12:00:00Z", health: "failing", syncEvery: "6h0m0s", nextAt: null },
    });
    show("/?view=accounts", data([failing, smtp("journal", { enabled: false })]));
    const work = within(screen.getByRole("region", { name: "work" }));
    expect(work.getByRole("button", { name: t.sourceSnapshot })).toBeTruthy();
    expect(work.getByText(t.tagFailing)).toBeTruthy();
    expect(work.getByText(/^Failed 3 times in a row since /)).toBeTruthy();
    expect(work.getByText("work@example.com · imap.example.com")).toBeTruthy();
    expect(work.getByText(t.messageCount(4, "4"))).toBeTruthy();
    const journal = within(screen.getByRole("region", { name: "journal" }));
    expect(journal.getByRole("button", { name: t.sourceComplete })).toBeTruthy();
    expect(journal.getByText(t.disabled)).toBeTruthy();
    expect(journal.queryByText(t.receiving)).toBeNull();
  });
});

describe("German", () => {
  // Carbon's own English defaults that must not show in German.
  const english = [/\bClose\b/, /Copy to clipboard/, /Copied/, /Show more/, /Show less/, /Breadcrumb/, /\bLoading\b/, /\bloading\b/,
    /Show password/, /Hide password/, /Focus sentinel/, /\bdanger\b/, /close notification/i, /\bnotification\b/];

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

  it("shows Carbon's texts on the list, the pages and the dialogs in German", () => {
    applySettings({ language: "de", locale: "de" });
    try {
      const de = messagesFor("de");
      const running = smtp("journal", { fills: [fill()], fill: fill(), sync: { ...account("j").sync, state: "running" } });
      const d = data([
        account("work", { sync: { ...account("w").sync, failureStreak: 3, failingSince: "2026-10-01T12:00:00Z", health: "failing" } }),
        running,
        account("old", { removed: true, purging: true, purge: { id: 1, expected: 10, locations: 2, deleted: 4, kept: 0 } }),
      ]);
      show("/?view=accounts", d);
      expect(heading(de.accountsTitle)).toBeTruthy();
      expect(screen.getByRole("progressbar", { name: de.fillProgress })).toBeTruthy();
      expect(document.body.innerHTML).toContain(de.carbon.status.error);
      fireEvent.click(screen.getByRole("button", { name: de.removedAccountsCount(1) }));
      fireEvent.click(within(screen.getByRole("region", { name: "journal" })).getByRole("button", { name: de.mailServerSetup }));
      expect(screen.getByRole("button", { name: de.carbon.copy })).toBeTruthy();
      fireEvent.click(within(screen.getByRole("region", { name: "work" })).getByRole("button", { name: de.remove }));
      expect(screen.getByRole("button", { name: de.carbon.closeModal })).toBeTruthy();
      noEnglish();
      cleanup();

      show("/?view=accounts&edit=work", d);
      expect(screen.getByRole("navigation", { name: de.carbon.breadcrumb })).toBeTruthy();
      expect(screen.getByRole("button", { name: de.carbon.showPassword })).toBeTruthy();
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

      const preview: PurgePreview = {
        account: "old", kind: "imap", locations: 5, messages: 4, deleted: 3, deletedBytes: 2048, kept: 0, keptIn: [], grace: "168h0m0s",
        blockers: { notRemoved: false, syncing: false, purgeOpen: false, rules: [], holds: [], messages: [] }, purge: null,
      };
      render(<PurgeReview preview={preview} busy error="" onConfirm={() => {}} onCancel={() => {}} />);
      expect(screen.getByRole("button", { name: de.cancel })).toBeTruthy();
      noEnglish();
    } finally {
      applySettings({ language: "en", locale: "en" });
    }
  });
});
