import { describe, expect, it } from "vitest";
import type { Account, Fill } from "./api";
import { fill } from "./fillFixture";
import { canKeepRunning, cutoffFromInput, fillIntervalOptions, fillStatus, fillTotals, recurringStatus, resumable, stoppable } from "./fill";
import { t } from "./i18n";

function account(f: Fill | null, state: "idle" | "queued" | "running" = "idle"): Account {
  return {
    name: "journal", kind: "smtp", enabled: true, removed: false, host: "", port: 0, tls: "tls", username: "",
    includedFolders: [], excludedFolders: [], syncInterval: "default", folders: [], goneMessages: 0, lastReconciledAt: null,
    addresses: ["archive@archive.local"], lastReceivedAt: null, fill: f, fills: f ? [f] : [], firstReceived: null,
    sync: { state, lastRun: null, failureStreak: 0, failingSince: null, health: "ok", syncEvery: null, nextAt: null },
  };
}

describe("fillStatus", () => {
  const now = new Date("2026-10-13T10:00:00Z");
  it("adds up the folders while it runs", () => {
    expect(fillTotals(fill())).toMatchObject({ scanned: 15, imported: 12, afterCutoff: 1, journaled: 2 });
    expect(fillStatus(account(fill(), "running"), fill(), now)).toEqual({ text: t.fillRunning(15, 12, 1, 2) });
    expect(fillStatus(account(fill(), "queued"), fill(), now)).toEqual({ text: t.fillQueued });
  });
  it("names the end and its error", () => {
    const done = { finishedAt: "2026-10-13T09:00:00Z" };
    const status = (f: Fill) => fillStatus(account(f), f, now)!;
    expect(status(fill({ ...done, status: "ok" })).text).toContain("12");
    expect(status(fill({ ...done, status: "failed", error: "login failed" })).error).toBe("login failed");
    const partial = fill({ ...done, status: "partial" });
    partial.folders[1]!.error = "Sent: connection reset";
    expect(status(partial).error).toBe("Sent: connection reset");
    expect(fillStatus(account(null), null, now)).toBeNull();
  });
  it("resumes only what did not end well", () => {
    expect(resumable(fill({ status: "failed" }))).toBe(true);
    expect(resumable(fill({ status: "partial" }))).toBe(true);
    expect(resumable(fill({ status: "cancelled" }))).toBe(true);
    expect(resumable(fill({ status: "ok" }))).toBe(false);
    expect(resumable(fill())).toBe(false);
    expect(resumable(null)).toBe(false);
    // A recurring fill between runs keeps running: it is stopped, not resumed.
    expect(resumable(fill({ status: "failed", keepRunning: true, recurringSince: "2026-10-01T08:00:00Z" }))).toBe(false);
    expect(resumable(fill({ status: "cancelled", recurringSince: "2026-10-01T08:00:00Z" }))).toBe(true);
  });
});

describe("recurring fills", () => {
  const now = new Date("2026-10-13T10:00:00Z");
  const recurring = (more: Partial<Fill> = {}) =>
    fill({
      status: "ok", keepRunning: true, recurringSince: "2026-10-01T08:00:00Z", runStartedAt: "2026-10-13T07:55:00Z",
      finishedAt: "2026-10-13T08:00:00Z", before: null, interval: "default", every: "6h0m0s", nextAt: "2026-10-13T13:55:00Z",
      health: { state: "ok", failureStreak: 0, failingSince: null, lastSuccessAt: "2026-10-13T08:00:00Z" },
      ...more,
    });
  it("tells the schedule, the last run and its counts", () => {
    const f = recurring();
    f.folders[0]!.waiting = 3;
    const text = recurringStatus(f, now);
    expect(text).toContain(t.recurringEvery("every 6 hours"));
    expect(text).toContain(t.nextSync("in 4 hours"));
    expect(text).toContain(t.recurringLastRun("2 hours ago", t.runOutcome("ok")));
    expect(text).toContain(t.recurringCounts(12, 3));
    expect(fillStatus(account(f), f, now)).toEqual({ text, error: undefined });
    expect(recurringStatus(recurring({ nextAt: "2026-10-13T09:00:00Z" }), now)).toContain(t.dueNow);
    expect(recurringStatus(recurring({ every: null, nextAt: null }), now)).toContain(t.recurringOff);
  });
  it("shows a run, and a run that waits after the server's start", () => {
    const running = recurring({ status: "running", finishedAt: null, nextAt: "2026-10-13T11:00:00Z" });
    expect(fillStatus(account(running, "running"), running, now)).toEqual({ text: t.recurringRunning(15, 12, 0, 2) });
    expect(fillStatus(account(running, "idle"), running, now)!.text).toBe(t.fillWaitsUntil("in 1 hour"));
  });
  it("names a stopped fill and the error of the last run", () => {
    const stopped = recurring({ status: "cancelled", keepRunning: false, finishedAt: "2026-10-12T10:00:00Z", every: null, nextAt: null, health: null });
    expect(fillStatus(account(stopped), stopped, now)!.text).toBe(t.fillStopped("yesterday"));
    const failed = recurring({ status: "failed", error: "login failed" });
    expect(fillStatus(account(failed), failed, now)!.error).toBe("login failed");
  });
  it("says what can be done", () => {
    expect(stoppable(fill())).toBe(true);
    expect(stoppable(recurring())).toBe(true);
    expect(stoppable(fill({ status: "ok" }))).toBe(false);
    expect(canKeepRunning(fill({ status: "ok" }))).toBe(true);
    expect(canKeepRunning(fill({ status: "cancelled" }))).toBe(true);
    expect(canKeepRunning(fill())).toBe(false);
    expect(canKeepRunning(recurring({ status: "cancelled", keepRunning: false }))).toBe(false);
  });
  it("offers intervals at or above the floor", () => {
    const opts = fillIntervalOptions("90m0s", { syncInterval: "6h0m0s", syncIntervalMin: "2h0m0s" }).map((o) => o.value);
    expect(opts).toEqual(["default", "6h0m0s", "12h0m0s", "24h0m0s", "90m0s"]);
    expect(fillIntervalOptions(null, { syncInterval: "6h0m0s", syncIntervalMin: "5m0s" }).map((o) => o.value)).toContain("1h0m0s");
  });
});

describe("cutoffFromInput", () => {
  it("sends the local time with its offset", () => {
    const got = cutoffFromInput("2026-10-01T08:30")!;
    expect(got).toMatch(/^2026-10-01T08:30:00[+-]\d{2}:\d{2}$/);
    expect(Date.parse(got)).toBe(new Date("2026-10-01T08:30").getTime());
  });
  it("leaves an empty or broken value to the server", () => {
    expect(cutoffFromInput("")).toBeUndefined();
    expect(cutoffFromInput("tomorrow")).toBeUndefined();
  });
});
