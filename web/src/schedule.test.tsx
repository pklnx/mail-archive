import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { accountsApi, type Account, type AccountsResponse } from "./api";
import { ScheduleForm, scheduleNote, scheduleOptions } from "./accountParts";
import { durationMinutes, formatEvery } from "./format";
import { messagesFor, t } from "./i18n";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function account(syncInterval: string, sync: Partial<Account["sync"]> = {}, more: Partial<Account> = {}): Account {
  return {
    name: "work", kind: "imap", enabled: true, removed: false, purging: false, purge: null, host: "h", port: 993, tls: "tls", username: "u",
    includedFolders: [], excludedFolders: [], syncInterval, folders: [], goneMessages: 0, lastReconciledAt: null,
    addresses: [], lastReceivedAt: null, fill: null, fills: [], firstReceived: null, sharedWith: [],
    sync: { state: "idle", lastRun: null, failureStreak: 0, failingSince: null, health: "ok", syncEvery: null, nextAt: null, ...sync },
    ...more,
  };
}

const data = (more: Partial<AccountsResponse> = {}): AccountsResponse => ({
  accounts: [], manage: true, shared: [], syncInterval: "6h0m0s", syncIntervalMin: "5m0s", reconcileInterval: "", alertAfter: 3, smtp: null, ...more,
});

describe("formatEvery", () => {
  it("names the frequency", () => {
    const en = messagesFor("en");
    expect(formatEvery("1h0m0s", en)).toBe("every hour");
    expect(formatEvery("6h0m0s", en)).toBe("every 6 hours");
    expect(formatEvery("24h0m0s", en)).toBe("every day");
    expect(formatEvery("720h0m0s", en)).toBe("every 30 days");
    expect(formatEvery("45m0s", en)).toBe("every 45 minutes");
    expect(formatEvery("1h30m0s", en)).toBe("every 90 minutes");
    expect(formatEvery("1h0m0s", messagesFor("de"))).toBe("stündlich");
    expect(formatEvery("30m0s", messagesFor("de"))).toBe("alle 30 Minuten");
    expect(formatEvery("nonsense", en)).toBe("nonsense");
  });
  it("reads whole minutes", () => {
    expect(durationMinutes("1h30m0s")).toBe(90);
    expect(durationMinutes("15m0s")).toBe(15);
    expect(durationMinutes("720h0m0s")).toBe(43200);
    expect(durationMinutes("")).toBeNull();
    expect(durationMinutes("default")).toBeNull();
    expect(durationMinutes("10s")).toBeNull();
  });
});

describe("scheduleNote", () => {
  const now = new Date("2026-10-12T12:00:00Z");
  const soon = "2026-10-12T12:12:00Z";
  const past = "2026-10-12T11:00:00Z";
  it("says how often and when next", () => {
    expect(scheduleNote(account("30m0s", { syncEvery: "30m0s", nextAt: soon }), data(), now)).toBe(
      `${"Every 30 minutes"} · ${t.nextSync("in 12 minutes")}`,
    );
    expect(scheduleNote(account("default", { syncEvery: "6h0m0s", nextAt: past }), data(), now)).toBe(
      `Every 6 hours ${t.defaultMark} · ${t.dueNow}`,
    );
    expect(scheduleNote(account("30m0s", { syncEvery: "1h0m0s", nextAt: soon }), data({ syncIntervalMin: "1h0m0s" }), now)).toBe(
      `Every hour ${t.raisedTo("30 minutes")} · ${t.nextSync("in 12 minutes")}`,
    );
    // While it syncs or waits, the card already says so.
    expect(scheduleNote(account("30m0s", { syncEvery: "30m0s", nextAt: past, state: "queued" }), data(), now)).toBe("Every 30 minutes");
    expect(scheduleNote(account("manual"), data(), now)).toBe(t.intervalManual);
  });
  it("says nothing when the schedule does not apply", () => {
    expect(scheduleNote(account("30m0s", { syncEvery: "30m0s" }, { enabled: false }), data(), now)).toBeNull();
    expect(scheduleNote(account("30m0s"), data({ syncInterval: "" }), now)).toBeNull();
    expect(scheduleNote(account("default", {}, { kind: "import" }), data(), now)).toBeNull();
    expect(scheduleNote(account("default", {}, { removed: true }), data(), now)).toBeNull();
  });
});

describe("scheduleOptions", () => {
  it("leaves out presets below the floor and keeps a stored other value", () => {
    const values = (a: Account, d: AccountsResponse) => scheduleOptions(a, d).map((o) => o.value);
    expect(values(account("default"), data())).toEqual(["default", "15m0s", "30m0s", "1h0m0s", "6h0m0s", "12h0m0s", "24h0m0s", "manual"]);
    expect(values(account("default"), data({ syncIntervalMin: "1h0m0s" }))).toEqual(["default", "1h0m0s", "6h0m0s", "12h0m0s", "24h0m0s", "manual"]);
    const other = scheduleOptions(account("45m0s"), data());
    expect(other.find((o) => o.value === "45m0s")?.label).toBe(t.intervalOther(formatEvery("45m0s")));
    expect(scheduleOptions(account("default"), data())[0]!.label).toBe(t.intervalDefault(formatEvery("6h0m0s")));
  });
});

describe("ScheduleForm", () => {
  it("sends only a changed choice", async () => {
    const update = vi.spyOn(accountsApi, "update").mockResolvedValue(undefined);
    const saved = vi.fn();
    render(<ScheduleForm account={account("default")} data={data()} onCancel={() => {}} onSaved={saved} />);
    const save = screen.getByText(t.saveSchedule).closest("button") as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    fireEvent.click(save);
    expect(update).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText(t.scheduleTitle), { target: { value: "30m0s" } });
    expect(save.disabled).toBe(false);
    fireEvent.click(save);
    expect(update).toHaveBeenCalledWith("work", { syncInterval: "30m0s" });
    await waitFor(() => expect(saved).toHaveBeenCalled());
  });
  it("keeps a value below a raised floor without sending it", () => {
    const update = vi.spyOn(accountsApi, "update");
    render(<ScheduleForm account={account("10m0s")} data={data({ syncIntervalMin: "15m0s" })} onCancel={() => {}} onSaved={() => {}} />);
    const select = screen.getByLabelText(t.scheduleTitle) as HTMLSelectElement;
    expect(select.value).toBe("10m0s");
    fireEvent.click(screen.getByText(t.saveSchedule));
    expect(update).not.toHaveBeenCalled();
  });
  it("is disabled while the schedule is off", () => {
    render(<ScheduleForm account={account("default")} data={data({ syncInterval: "" })} onCancel={() => {}} onSaved={() => {}} />);
    expect((screen.getByLabelText(t.scheduleTitle) as HTMLSelectElement).disabled).toBe(true);
    expect(screen.getByText(t.scheduleOffHint)).toBeTruthy();
  });
  it("does not save while an earlier rename is not in the list yet", () => {
    const update = vi.spyOn(accountsApi, "update");
    render(<ScheduleForm account={account("default")} data={data()} locked onCancel={() => {}} onSaved={() => {}} />);
    fireEvent.change(screen.getByLabelText(t.scheduleTitle), { target: { value: "30m0s" } });
    const save = screen.getByText(t.saveSchedule).closest("button") as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    fireEvent.submit(save.closest("form")!);
    expect(update).not.toHaveBeenCalled();
  });
});
