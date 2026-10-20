import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { Account, AccountsResponse, Fill } from "./api";
import { fill } from "./fillFixture";
import { bannerState, HealthBanner } from "./HealthBanner";

function acc(name: string, health: Account["sync"]["health"]): Account {
  return {
    name, kind: "imap", enabled: true, removed: false, host: "h", port: 993, tls: "tls", username: "u",
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
  it("shows admins a count of other users' failing accounts", () => {
    const d = data([], 2);
    const { rerender } = render(<HealthBanner data={d} admin openAccounts={noop} openUsers={noop} />);
    expect(screen.getByText(/2 accounts of other users keep failing/)).toBeTruthy();
    rerender(<HealthBanner data={d} admin={false} openAccounts={noop} openUsers={noop} />);
    expect(screen.queryByText(/other users/)).toBeNull();
  });
});
