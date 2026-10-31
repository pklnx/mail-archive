import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { accountsApi, ApiError, type Account, type PurgePreview } from "./api";
import { PurgeDialog, PurgeReview, purgeBlockerLines } from "./AccountPurge";
import { AccountsPage } from "./AccountsPage";
import { messagesFor, t } from "./i18n";
import { findAlert, theAlert } from "./test/alerts";
import { isActive } from "./useAccounts";

const preview = (p: Partial<PurgePreview> = {}): PurgePreview => ({
  account: "old",
  kind: "import",
  locations: 5,
  messages: 4,
  deleted: 3,
  deletedBytes: 2048,
  kept: 1,
  keptIn: [{ owner: "alice", account: "work", messages: 1 }],
  grace: "168h0m0s",
  blockers: { notRemoved: false, syncing: false, purgeOpen: false, rules: [], holds: [], messages: [] },
  purge: null,
  ...p,
});

const removed: Account = {
  name: "old", kind: "import", enabled: false, removed: true, purging: false, purge: null, host: "", port: 0, tls: "tls", username: "",
  includedFolders: [], excludedFolders: [], syncInterval: "default", folders: [{ name: "INBOX", messages: 4, lastSyncedAt: null, lastReconciledAt: null, delimiter: "/" }],
  goneMessages: 0, lastReconciledAt: null, addresses: [], lastReceivedAt: null, fill: null, fills: [], firstReceived: null, sharedWith: [],
  sync: { state: "idle", lastRun: null, failureStreak: 0, failingSince: null, health: "ok", syncEvery: null, nextAt: null },
};

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  window.history.replaceState(null, "", "/");
});

const confirmButton = () => screen.getByRole("button", { name: t.purgeConfirm }) as HTMLButtonElement;

describe("PurgeReview", () => {
  it("shows the counts and enables the button only for the exact count", () => {
    const onConfirm = vi.fn();
    render(<PurgeReview preview={preview()} busy={false} error="" onConfirm={onConfirm} onCancel={() => {}} />);
    expect(screen.getByText(t.purgeTitle("old"))).toBeTruthy();
    expect(screen.getByText(t.purgeLocations(5, "5"))).toBeTruthy();
    const deleted = t.purgeDeleted(3, "3", "2.0 KB");
    expect(screen.getByText((_, el) => el?.tagName === "LI" && (el.textContent ?? "").startsWith(deleted))).toBeTruthy();
    expect(screen.getByText((_, el) => el?.tagName === "LI" && (el.textContent ?? "").endsWith(t.purgeFiles(3, "7 days")))).toBeTruthy();
    expect(screen.getByText(t.purgeKept(1, "1"))).toBeTruthy();
    expect(screen.getByText("alice / work")).toBeTruthy();

    const field = screen.getByLabelText(t.purgeTypeCount(3));
    expect(confirmButton().disabled).toBe(true);
    for (const wrong of ["", "2", "4", "3x", "03"]) {
      fireEvent.change(field, { target: { value: wrong } });
      expect(confirmButton().disabled, wrong).toBe(true);
      fireEvent.submit(field.closest("form")!);
    }
    expect(onConfirm).not.toHaveBeenCalled();
    fireEvent.change(field, { target: { value: " 3 " } });
    expect(confirmButton().disabled).toBe(false);
    fireEvent.click(confirmButton());
    expect(onConfirm).toHaveBeenCalledWith(3);
  });

  it("needs a typed 0 when nothing is deleted", () => {
    render(<PurgeReview preview={preview({ deleted: 0, deletedBytes: 0 })} busy={false} error="" onConfirm={() => {}} onCancel={() => {}} />);
    expect(confirmButton().disabled).toBe(true);
    fireEvent.change(screen.getByLabelText(t.purgeTypeCount(0)), { target: { value: "0" } });
    expect(confirmButton().disabled).toBe(false);
  });

  it("names the blockers and offers no confirmation", () => {
    const blockers = {
      notRemoved: false, syncing: false, purgeOpen: false, messages: [],
      rules: [{ id: 12, folder: "", years: 10 }, { id: 13, folder: "INBOX", years: 6 }],
      holds: [{ id: 7, name: "Case A", released: false, namesAccount: false }, { id: 4, name: "Old case", released: true, namesAccount: true }],
    };
    render(<PurgeReview preview={preview({ blockers })} busy={false} error="" onConfirm={() => {}} onCancel={() => {}} />);
    const alert = screen.getByRole("alert");
    for (const line of [
      t.purgeRule(12, "", 10), t.purgeRule(13, "INBOX", 6), t.purgeHoldCovers(7, "Case A"), t.purgeHoldReleased(4, "Old case"),
      t.purgeFixRules, t.purgeFixHolds,
    ]) {
      expect(alert.textContent).toContain(line);
    }
    expect(alert.textContent).toContain("#12");
    expect(screen.queryByRole("button", { name: t.purgeConfirm })).toBeNull();
    expect(screen.queryByLabelText(t.purgeTypeCount(3))).toBeNull();
  });

  it("is a danger dialog that cannot be left or sent twice while it sends", () => {
    const onCancel = vi.fn();
    const onConfirm = vi.fn();
    const { rerender } = render(<PurgeReview preview={preview()} busy={false} error="" onConfirm={onConfirm} onCancel={onCancel} />);
    const dialog = screen.getByRole("dialog");
    expect(dialog.closest(".cds--modal")?.classList.contains("cds--modal--danger")).toBe(true);
    fireEvent.change(screen.getByLabelText(t.purgeTypeCount(3)), { target: { value: "3" } });
    rerender(<PurgeReview preview={preview()} busy={true} error="" onConfirm={onConfirm} onCancel={onCancel} />);
    const [cancel, confirm] = within(dialog).getAllByRole("button").slice(-2) as HTMLButtonElement[];
    expect(cancel!.textContent).toBe(t.cancel);
    expect(cancel!.disabled).toBe(true);
    expect(confirm!.disabled).toBe(true);
    fireEvent.submit(screen.getByLabelText(t.purgeTypeCount(3)).closest("form")!);
    fireEvent.keyDown(document, { key: "Escape" });
    fireEvent.click(screen.getByRole("button", { name: t.carbon.closeModal }));
    expect(onConfirm).not.toHaveBeenCalled();
    expect(onCancel).not.toHaveBeenCalled();
    rerender(<PurgeReview preview={preview()} busy={false} error="" onConfirm={onConfirm} onCancel={onCancel} />);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it("shows an error", () => {
    render(<PurgeReview preview={preview()} busy={false} error="Failed: nope" onConfirm={() => {}} onCancel={() => {}} />);
    expect(theAlert().textContent).toBe("Failed: nope");
  });
});

describe("purgeBlockerLines", () => {
  it("asks to release only active holds", () => {
    const b = { notRemoved: false, syncing: true, purgeOpen: true, rules: [], messages: [], holds: [{ id: 4, name: "x", released: true, namesAccount: true }] };
    expect(purgeBlockerLines(b)).toEqual([t.purgeSyncing, t.purgeOtherOpen, t.purgeHoldReleased(4, "x")]);
  });
});

describe("PurgeDialog", () => {
  it("sends the typed count and reports the confirmation", async () => {
    vi.spyOn(accountsApi, "purgePreview").mockResolvedValue(preview());
    const purge = vi.spyOn(accountsApi, "purge").mockResolvedValue(9);
    const onConfirmed = vi.fn();
    render(<PurgeDialog name="old" onClose={() => {}} onConfirmed={onConfirmed} />);
    fireEvent.change(await screen.findByLabelText(t.purgeTypeCount(3)), { target: { value: "3" } });
    fireEvent.click(confirmButton());
    await waitFor(() => expect(onConfirmed).toHaveBeenCalled());
    expect(purge).toHaveBeenCalledWith("old", 3);
  });

  it("shows the server's refusal and the preview as it is now", async () => {
    const load = vi.spyOn(accountsApi, "purgePreview").mockResolvedValueOnce(preview()).mockResolvedValueOnce(preview({ deleted: 2 }));
    vi.spyOn(accountsApi, "purge").mockRejectedValue(new ApiError(409, "the number of messages to delete is now 2, not 3; review again"));
    const onConfirmed = vi.fn();
    render(<PurgeDialog name="old" onClose={() => {}} onConfirmed={onConfirmed} />);
    fireEvent.change(await screen.findByLabelText(t.purgeTypeCount(3)), { target: { value: "3" } });
    fireEvent.click(confirmButton());
    expect((await findAlert()).textContent).toBe(t.failed("the number of messages to delete is now 2, not 3; review again"));
    expect(await screen.findByLabelText(t.purgeTypeCount(2))).toBeTruthy();
    expect(load).toHaveBeenCalledTimes(2);
    expect(confirmButton().disabled).toBe(true);
    expect(onConfirmed).not.toHaveBeenCalled();
  });
});

describe("the removed accounts' table", () => {
  const list = (accounts: Account[]) => ({ accounts, shared: [], manage: true, syncInterval: "", syncIntervalMin: "", reconcileInterval: "", alertAfter: 3, smtp: null });
  /** The Accounts page with a removed account; its accordion is open while it is being deleted. */
  function page(account: Account, admin: boolean, onChanged: () => void = () => {}) {
    window.history.replaceState(null, "", "/?view=accounts");
    const ui = (a: Account) => <AccountsPage accounts={{ data: list([a]), error: "", reload: onChanged }} admin={admin} renamed={() => {}} showGone={() => {}} showAccount={() => {}} />;
    const view = render(ui(account));
    return (a: Account) => view.rerender(ui(a));
  }
  const menuButton = () => screen.queryByRole("button", { name: t.userActions("old") });
  function start() {
    fireEvent.click(menuButton()!);
    fireEvent.click(screen.getByRole("menuitem", { name: t.purgeStart }));
  }

  it("offers deleting completely to admins only", async () => {
    vi.spyOn(accountsApi, "purgePreview").mockResolvedValue(preview());
    page(removed, false);
    expect(menuButton()).toBeNull();
    cleanup();
    page(removed, true);
    start();
    expect(await screen.findByText(t.purgeTitle("old"))).toBeTruthy();
    fireEvent.click(screen.getByText(t.cancel));
    expect(screen.queryByText(t.purgeTitle("old"))).toBeNull();
  });

  it("shows the progress from the account list, without loading the preview", () => {
    const load = vi.spyOn(accountsApi, "purgePreview");
    const purge = { id: 1, expected: 1234, locations: 2, deleted: 1000, kept: 1 };
    page({ ...removed, purging: true, purge }, true);
    // Only the unchanging line is live; the counts are formatted.
    expect(screen.getByRole("status").textContent).toBe(t.purgeRunning);
    expect(screen.getByText(t.purgeProgress("1,000", "1,234", "1"))).toBeTruthy();
    expect(menuButton()).toBeNull();
    expect(load).not.toHaveBeenCalled();
    cleanup();
    page({ ...removed, purging: true }, false);
    expect(screen.getByRole("status").textContent).toBe(t.purgeRunning);
  });

  it("drops the dialog when the account's purge is open elsewhere", async () => {
    vi.spyOn(accountsApi, "purgePreview").mockResolvedValue(preview());
    const rerender = page(removed, true);
    start();
    expect(await screen.findByLabelText(t.purgeTypeCount(3))).toBeTruthy();
    const purge = { id: 1, expected: 3, locations: 0, deleted: 0, kept: 0 };
    rerender({ ...removed, purging: true, purge });
    expect(screen.queryByLabelText(t.purgeTypeCount(3))).toBeNull();
    expect(screen.getByRole("status").textContent).toBe(t.purgeRunning);
    // It ended but the account is still there: it was cancelled.
    rerender(removed);
    expect(screen.getByRole("status").textContent).toBe(t.purgeCancelled);
    expect(screen.queryByLabelText(t.purgeTypeCount(3))).toBeNull();
    expect(menuButton()).toBeTruthy();
  });

  it("moves the focus into the dialog and back to the menu's button", async () => {
    vi.spyOn(accountsApi, "purgePreview").mockResolvedValue(preview());
    page(removed, true);
    start();
    const field = await screen.findByLabelText(t.purgeTypeCount(3));
    expect(document.activeElement).toBe(field);
    fireEvent.click(screen.getByText(t.cancel));
    expect(document.activeElement).toBe(menuButton());
  });

  it("focuses the heading when something blocks the deletion", async () => {
    const blockers = { notRemoved: false, syncing: false, purgeOpen: false, messages: [], rules: [{ id: 1, folder: "", years: 6 }], holds: [] };
    vi.spyOn(accountsApi, "purgePreview").mockResolvedValue(preview({ blockers }));
    page(removed, true);
    start();
    // The title shows while the preview loads; the reasons come with it.
    await screen.findByText(t.purgeBlocked);
    expect(screen.getByText(t.purgeTitle("old"))).toBe(document.activeElement);
  });

  it("reloads the account list at once after a conflict", async () => {
    vi.spyOn(accountsApi, "purgePreview").mockResolvedValue(preview());
    const purge = vi
      .spyOn(accountsApi, "purge")
      .mockRejectedValueOnce(new ApiError(409, 'purge 4 of account "old" is open already: an account purge is open'))
      .mockRejectedValueOnce(new ApiError(500, "internal error"));
    const onChanged = vi.fn();
    page(removed, true, onChanged);
    start();
    fireEvent.change(await screen.findByLabelText(t.purgeTypeCount(3)), { target: { value: "3" } });
    fireEvent.click(confirmButton());
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1));
    expect(await screen.findByText(/is open already/)).toBeTruthy();
    // Other errors leave the list alone.
    fireEvent.change(await screen.findByLabelText(t.purgeTypeCount(3)), { target: { value: "3" } });
    fireEvent.click(confirmButton());
    expect(await screen.findByText(t.failed("internal error"))).toBeTruthy();
    expect(purge).toHaveBeenCalledTimes(2);
    expect(onChanged).toHaveBeenCalledTimes(1);
  });

  it("polls fast while an account is being deleted", () => {
    const data = { manage: true, shared: [], syncInterval: "", syncIntervalMin: "", reconcileInterval: "", alertAfter: 3, smtp: null, accounts: [removed] };
    expect(isActive(data)).toBe(false);
    expect(isActive({ ...data, accounts: [{ ...removed, purging: true }] })).toBe(true);
  });
});

describe("purge texts", () => {
  it("are in English and German", () => {
    const en = messagesFor("en");
    const de = messagesFor("de");
    expect(en.purgeStart).toBe("Delete completely…");
    expect(de.purgeStart).toBe("Vollständig löschen…");
    expect(en.purgeTypeCount(42)).toBe("Type 42 to confirm");
    expect(de.purgeTypeCount(42)).toBe("Zur Bestätigung 42 eingeben");
    expect(de.purgeRule(3, "", 10)).toBe("Aufbewahrungsregel #3 (alle Ordner, 10 Jahre)");
    expect(de.purgeHoldReleased(4, "Fall A")).toContain("#4 „Fall A“");
    expect(de.purgeTitle("x")).toBe("„x“ vollständig löschen?");
    expect(en.purgeFiles(1, "7 days")).toMatch(/^Its file is removed/);
    expect(en.purgeFiles(2, "7 days")).toMatch(/^Their files are removed/);
    expect(de.purgeFiles(1, "7 Tagen")).toMatch(/^Ihre Datei wird nach 7 Tagen/);
    expect(de.purgeFilesLater(2)).toMatch(/^Ihre Dateien werden/);
  });
});
