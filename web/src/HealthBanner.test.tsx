import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Account, AccountsResponse, Fill } from "./api";
import { fill } from "./fillFixture";
import { bannerState, HealthBanner, storageBanner } from "./HealthBanner";
import { applySettings, messagesFor, t } from "./i18n";
import { adminUsage, groupState, memberUsage } from "./test/usage";

function acc(name: string, health: Account["sync"]["health"]): Account {
  return {
    name, kind: "imap", enabled: true, removed: false, purging: false, purge: null, host: "h", port: 993, tls: "tls", username: "u",
    includedFolders: [], excludedFolders: [], syncInterval: "default", folders: [], goneMessages: 0, lastReconciledAt: null,
    addresses: [], lastReceivedAt: null, fill: null, fills: [], firstReceived: null, sharedWith: [],
    sync: { state: "idle", lastRun: null, failureStreak: health === "failing" ? 3 : 0, failingSince: null, health, syncEvery: null, nextAt: null },
  };
}

const data = (accounts: Account[], otherFailing?: number): AccountsResponse => ({
  manage: true, shared: [], syncInterval: "6h0m0s", syncIntervalMin: "5m0s", reconcileInterval: "24h0m0s", alertAfter: 3, smtp: null, accounts, otherFailing,
});

const noop = () => {};

afterEach(cleanup);

describe("HealthBanner", () => {
  it("shows nothing while all is well", () => {
    const { container } = render(<HealthBanner data={data([acc("work", "ok")])} admin={false} openAccounts={noop} openUsers={noop} />);
    expect(container.textContent).toBe("");
  });
  it("names own failing and stale accounts and announces changes once", () => {
    const props = { admin: false, openAccounts: noop, openUsers: noop };
    const { rerender } = render(<HealthBanner data={data([acc("work", "failing"), acc("old", "stale")])} {...props} />);
    expect(screen.getByRole("alert").textContent).toMatch(/failing for work/);
    expect(screen.getByText(/Not synced successfully for a while: old/)).toBeTruthy();
    rerender(<HealthBanner data={data([acc("work", "failing"), acc("old", "ok")])} {...props} />);
    expect(screen.getByRole("alert").textContent).toMatch(/failing for work/);
    rerender(<HealthBanner data={data([acc("work", "ok")])} {...props} />);
    expect(screen.getByRole("alert").textContent).toBe("");
  });
  it("names smtp accounts whose recurring fill fails or is stale, with the mailbox", () => {
    const recurring = (username: string, state: "ok" | "failing" | "stale", more: Partial<Fill> = {}) =>
      fill({ username, status: "failed", keepRunning: true, recurringSince: "2026-10-01T08:00:00Z", health: { state, failureStreak: 3, failingSince: null, lastSuccessAt: null }, ...more });
    const journal: Account = {
      ...acc("journal", "ok"), kind: "smtp",
      fills: [recurring("ak@example.com", "failing"), recurring("bo@example.com", "stale"), recurring("ok@example.com", "ok"),
        recurring("old@example.com", "failing", { keepRunning: false })],
    };
    expect(bannerState(data([acc("work", "failing"), journal]))).toEqual({
      failing: ["work", "journal (ak@example.com)"], stale: ["journal (bo@example.com)"], otherFailing: 0,
    });
    expect(bannerState(data([{ ...journal, enabled: false }]))).toEqual({ failing: [], stale: [], otherFailing: 0 });
    render(<HealthBanner data={data([journal])} admin={false} openAccounts={noop} openUsers={noop} />);
    expect(screen.getByRole("alert").textContent).toMatch(/journal \(ak@example.com\)/);
  });
  it("offers one way to the accounts and one to the users, without taking focus", () => {
    const openAccounts = vi.fn();
    const openUsers = vi.fn();
    const before = document.createElement("button");
    document.body.append(before);
    before.focus();
    const { container } = render(
      <HealthBanner data={data([acc("work", "failing"), acc("old", "stale")], 1)} admin openAccounts={openAccounts} openUsers={openUsers} />,
    );
    // A banner that appears with a poll is no dialog: focus stays where it was.
    expect(document.activeElement).toBe(before);
    expect(screen.queryByRole("alertdialog")).toBeNull();
    // Failing and stale share one button to the accounts.
    expect(screen.getAllByRole("button", { name: t.bannerOpenAccounts })).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: t.bannerOpenAccounts }));
    expect(openAccounts).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: t.bannerOpenUsers }));
    expect(openUsers).toHaveBeenCalledTimes(1);
    // Not closable, and no hidden focus traps.
    expect(container.querySelectorAll("button")).toHaveLength(2);
    expect(container.querySelectorAll("[tabindex]")).toHaveLength(0);
    expect(container.innerHTML).not.toContain("Focus sentinel");
    before.remove();
  });

  it("shows Carbon's texts in German", () => {
    applySettings({ language: "de", locale: "de" });
    try {
      const de = messagesFor("de");
      render(<HealthBanner data={data([acc("work", "failing"), acc("old", "stale")], 1)} admin openAccounts={noop} openUsers={noop} />);
      expect(screen.getByRole("button", { name: de.bannerOpenAccounts })).toBeTruthy();
      const html = document.body.innerHTML;
      for (const s of [de.carbon.status.error, de.carbon.status.warning, de.carbon.status.info]) expect(html).toContain(s);
      for (const s of ["error icon", "warning icon", "info icon", "close notification", "Focus sentinel"]) expect(html).not.toContain(s);
    } finally {
      applySettings({ language: "en", locale: "en" });
    }
  });

  it("shows admins a count of other users' failing accounts", () => {
    const d = data([], 2);
    const { rerender } = render(<HealthBanner data={d} admin openAccounts={noop} openUsers={noop} />);
    expect(screen.getByText(/2 accounts of other users keep failing/)).toBeTruthy();
    rerender(<HealthBanner data={d} admin={false} openAccounts={noop} openUsers={noop} />);
    expect(screen.queryByText(/other users/)).toBeNull();
  });
});

describe("storage banner (#397, part 4)", () => {
  const props = { data: data([]), openAccounts: noop, openUsers: noop };

  it("tells a member only that new mail is not archived (Q7), the same for full and overflow, without an action", () => {
    const { container, rerender } = render(<HealthBanner {...props} usage={memberUsage("full")} admin={false} />);
    // The line, and the live region that announces it.
    expect(screen.getAllByText(t.bannerStorageFull)).toHaveLength(2);
    expect(container.querySelectorAll("button")).toHaveLength(0);
    const full = container.innerHTML;
    rerender(<HealthBanner {...props} usage={memberUsage("overflow")} admin={false} />);
    expect(container.innerHTML).toBe(full);
    rerender(<HealthBanner {...props} usage={memberUsage("ok")} admin={false} />);
    expect(container.querySelector(".health-banner")).toBeNull();
    expect(storageBanner(memberUsage("ok"))).toEqual({ full: "", warn: "" });
  });

  it("announces a full storage once", () => {
    render(<HealthBanner {...props} usage={memberUsage("full")} admin={false} />);
    expect(screen.getAllByRole("alert").some((el) => el.textContent === t.bannerStorageFull)).toBe(true);
  });

  it("names every scope in warn, full, overflow or disk_low for admins, with a way to the Storage page", () => {
    const openStorage = vi.fn();
    const u = adminUsage();
    expect(storageBanner(u)).toEqual({ full: "", warn: "" });
    expect(storageBanner({ ...u, instance: { ...u.instance, state: "warn" } }).warn).toBe("Storage nearly full (instance).");
    expect(storageBanner(groupState(u, "Meier", "warn")).warn).toBe("Storage nearly full (Meier).");
    for (const s of ["full", "overflow"] as const) {
      expect(storageBanner(groupState(u, "Meier", s)).full).toBe(t.bannerStorageFullAdmin("Meier"));
    }
    expect(storageBanner({ ...u, instance: { ...u.instance, state: "disk_low" } }).full).toBe(t.bannerStorageFullAdmin("disk reserve"));
    expect(storageBanner({ ...u, instance: { ...u.instance, state: "overflow" } }).full).toBe(t.bannerStorageFullAdmin("instance"));
    const both = storageBanner(groupState(groupState(u, "Meier", "full"), "Default", "warn"));
    expect(both).toEqual({ full: t.bannerStorageFullAdmin("Meier"), warn: "Storage nearly full (Default)." });

    render(<HealthBanner {...props} usage={groupState(u, "Meier", "full")} admin openStorage={openStorage} />);
    fireEvent.click(screen.getByRole("button", { name: t.bannerOpenStorage }));
    expect(openStorage).toHaveBeenCalledTimes(1);
  });

  it("does not name the only group (#470 decision 10)", () => {
    const u = adminUsage({ groups: [{ id: 1, name: "Default", messages: 1, bytes: 1, quotaBytes: 10, state: "full", stateSince: null }] });
    expect(storageBanner(u)).toEqual({ full: t.bannerStorageFullAdmin("instance"), warn: "" });
  });

  it("is in German", () => {
    applySettings({ language: "de", locale: "de" });
    try {
      render(<HealthBanner {...props} usage={memberUsage("full")} admin={false} />);
      expect(screen.getAllByText("Neue Mails werden nicht archiviert; auf dem Server inzwischen gelöschte Mails werden nicht archiviert.")).toHaveLength(2);
      expect(storageBanner({ ...adminUsage(), instance: { ...adminUsage().instance, state: "warn" } }).warn).toBe("Speicher fast voll (Instanz).");
    } finally {
      applySettings({ language: "en", locale: "en" });
    }
  });
});
