import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { Account, AccountsResponse } from "./api";
import { HealthBanner } from "./HealthBanner";

function acc(name: string, health: Account["sync"]["health"]): Account {
  return {
    name, kind: "imap", enabled: true, removed: false, host: "h", port: 993, tls: "tls", username: "u",
    includedFolders: [], excludedFolders: [], folders: [],
    sync: { state: "idle", lastRun: null, failureStreak: health === "failing" ? 3 : 0, failingSince: null, health },
  };
}

const data = (accounts: Account[], otherFailing?: number): AccountsResponse => ({
  manage: true, syncInterval: "6h0m0s", alertAfter: 3, accounts, otherFailing,
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
  it("shows admins a count of other users' failing accounts", () => {
    const d = data([], 2);
    const { rerender } = render(<HealthBanner data={d} admin openAccounts={noop} openUsers={noop} />);
    expect(screen.getByText(/2 accounts of other users keep failing/)).toBeTruthy();
    rerender(<HealthBanner data={d} admin={false} openAccounts={noop} openUsers={noop} />);
    expect(screen.queryByText(/other users/)).toBeNull();
  });
});
