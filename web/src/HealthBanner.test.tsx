import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Account, AccountsResponse, Fill } from "./api";
import { fill } from "./fillFixture";
import { bannerState, HealthBanner } from "./HealthBanner";
import { applySettings, messagesFor, t } from "./i18n";

function acc(name: string, health: Account["sync"]["health"]): Account {
  return {
    name, kind: "imap", enabled: true, removed: false, purging: false, purge: null, host: "h", port: 993, tls: "tls", username: "u",
    includedFolders: [], excludedFolders: [], syncInterval: "default", folders: [], goneMessages: 0, lastReconciledAt: null,
    addresses: [], lastReceivedAt: null, fill: null, fills: [], firstReceived: null,
    sync: { state: "idle", lastRun: null, failureStreak: health === "failing" ? 3 : 0, failingSince: null, health, syncEvery: null, nextAt: null },
  };
}

const data = (accounts: Account[], otherFailing?: number): AccountsResponse => ({
  manage: true, syncInterval: "6h0m0s", syncIntervalMin: "5m0s", reconcileInterval: "24h0m0s", alertAfter: 3, smtp: null, accounts, otherFailing,
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
