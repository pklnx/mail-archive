import { describe, expect, it } from "vitest";
import type { Account, Fill } from "./api";
import { fill } from "./fillFixture";
import { cutoffFromInput, fillStatus, fillTotals, resumable } from "./fill";
import { t } from "./i18n";

function account(f: Fill | null, state: "idle" | "queued" | "running" = "idle"): Account {
  return {
    name: "journal", kind: "smtp", enabled: true, removed: false, host: "", port: 0, tls: "tls", username: "",
    includedFolders: [], excludedFolders: [], syncInterval: "default", folders: [], goneMessages: 0, lastReconciledAt: null,
    addresses: ["archive@archive.local"], lastReceivedAt: null, fill: f, firstReceived: null,
    sync: { state, lastRun: null, failureStreak: 0, failingSince: null, health: "ok", syncEvery: null, nextAt: null },
  };
}

describe("fillStatus", () => {
  const now = new Date("2026-10-13T10:00:00Z");
  it("adds up the folders while it runs", () => {
    expect(fillTotals(fill())).toMatchObject({ scanned: 15, imported: 12, afterCutoff: 1, journaled: 2 });
    expect(fillStatus(account(fill(), "running"), now)).toEqual({ text: t.fillRunning(15, 12, 1, 2) });
    expect(fillStatus(account(fill(), "queued"), now)).toEqual({ text: t.fillQueued });
  });
  it("names the end and its error", () => {
    const done = { finishedAt: "2026-10-13T09:00:00Z" };
    expect(fillStatus(account(fill({ ...done, status: "ok" })), now)!.text).toContain("12");
    expect(fillStatus(account(fill({ ...done, status: "failed", error: "login failed" })), now)!.error).toBe("login failed");
    const partial = fill({ ...done, status: "partial" });
    partial.folders[1]!.error = "Sent: connection reset";
    expect(fillStatus(account(partial), now)!.error).toBe("Sent: connection reset");
    expect(fillStatus(account(null), now)).toBeNull();
  });
  it("resumes only what did not end well", () => {
    expect(resumable(fill({ status: "failed" }))).toBe(true);
    expect(resumable(fill({ status: "partial" }))).toBe(true);
    expect(resumable(fill({ status: "cancelled" }))).toBe(true);
    expect(resumable(fill({ status: "ok" }))).toBe(false);
    expect(resumable(fill())).toBe(false);
    expect(resumable(null)).toBe(false);
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
