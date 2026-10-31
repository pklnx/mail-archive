import { describe, expect, it } from "vitest";
import type { Account, SyncInfo } from "./api";
import { receiveStatus, reconcileNote, syncableCount, syncHealthNote, syncStatus } from "./accountParts";
import { bannerState } from "./HealthBanner";
import { isActive } from "./useAccounts";

function account(sync: Pick<SyncInfo, "state" | "lastRun"> & Partial<SyncInfo>, kind: Account["kind"] = "imap"): Account {
  return {
    name: "a", kind, enabled: kind !== "import", removed: false, purging: false, purge: null, host: "h", port: 993, tls: "tls", username: "u",
    includedFolders: [], excludedFolders: [], syncInterval: "default", folders: [], goneMessages: 0, lastReconciledAt: null,
    addresses: [], lastReceivedAt: null, fill: null, fills: [], firstReceived: null, sharedWith: [],
    sync: { failureStreak: 0, failingSince: null, health: "ok", syncEvery: null, nextAt: null, ...sync },
  };
}

const run = {
  startedAt: "2026-10-06T10:00:00Z", finishedAt: "2026-10-06T10:01:00Z", fetched: 7, new: 3,
  reconciledFolders: 0, gone: 0, back: 0, flagsChanged: 0,
};

describe("syncStatus", () => {
  it("describes each state", () => {
    expect(syncStatus(account({ state: "idle", lastRun: null })).text).toBe("Not synced yet");
    expect(syncStatus(account({ state: "queued", lastRun: null })).text).toBe("Waiting to sync…");
    expect(syncStatus(account({ state: "running", lastRun: { ...run, finishedAt: null, status: "running" } })).text).toBe(
      "Syncing: 7 fetched, 3 new",
    );
    expect(syncStatus(account({ state: "idle", lastRun: { ...run, status: "ok" } })).text).toMatch(/^Last sync .*: 3 new$/);
    const failed = syncStatus(account({ state: "idle", lastRun: { ...run, status: "failed", error: "login: denied" } }));
    // A finished lastRun is the previous sync: its counts do not show (#310).
    expect(syncStatus(account({ state: "running", lastRun: { ...run, status: "ok" } })).text).toBe("Syncing: 0 fetched, 0 new");
    expect(failed.text).toMatch(/failed$/);
    expect(failed.error).toBe("login: denied");
  });
  it("describes imports", () => {
    expect(syncStatus(account({ state: "idle", lastRun: null }, "import")).text).toMatch(/never synced/);
    expect(syncStatus(account({ state: "running", lastRun: { ...run, finishedAt: null, status: "running" } }, "import")).text).toBe(
      "Importing: 7 read, 3 new",
    );
    expect(syncStatus(account({ state: "idle", lastRun: { ...run, status: "ok" } }, "import")).text).toMatch(/^Last import .*: 3 new$/);
    // A finished lastRun is the previous import: its counts do not show.
    expect(syncStatus(account({ state: "running", lastRun: { ...run, status: "ok" } }, "import")).text).toBe("Importing: 0 read, 0 new");
    const partial = syncStatus(account({ state: "idle", lastRun: { ...run, status: "partial", error: "1 message(s) skipped" } }, "import"));
    expect(partial.text).toMatch(/skipped messages$/);
    expect(partial.error).toBe("1 message(s) skipped");
  });
  it("leaves import and smtp accounts out of Sync all", () => {
    const idle = { state: "idle" as const, lastRun: null };
    expect(syncableCount([account(idle, "import")])).toBe(0);
    expect(syncableCount([account(idle, "smtp")])).toBe(0);
    expect(syncableCount([account(idle, "import"), account(idle)])).toBe(1);
    expect(syncableCount([{ ...account(idle), removed: true }])).toBe(0);
  });
  it("tells when an smtp account last received", () => {
    const idle = { state: "idle" as const, lastRun: null };
    expect(receiveStatus(account(idle, "smtp"))).toBe("No message received yet.");
    expect(receiveStatus({ ...account(idle, "smtp"), lastReceivedAt: "2026-10-06T10:00:00Z" })).toMatch(/^Last message received /);
    expect(syncHealthNote(account(idle, "smtp"))).toBeNull();
    expect(reconcileNote(account(idle, "smtp"))).toBeNull();
  });
  it("polls fast only while a sync is pending", () => {
    const data = { manage: true, shared: [], syncInterval: "", syncIntervalMin: "5m0s", reconcileInterval: "", alertAfter: 3, smtp: null, accounts: [account({ state: "idle", lastRun: null })] };
    expect(isActive(data)).toBe(false);
    expect(isActive({ ...data, accounts: [account({ state: "queued", lastRun: null })] })).toBe(true);
    expect(isActive(null)).toBe(false);
  });
  it("shows the failure streak and staleness", () => {
    const idle = { state: "idle" as const, lastRun: null };
    expect(syncHealthNote(account(idle))).toBeNull();
    expect(syncHealthNote(account({ ...idle, failureStreak: 1, failingSince: "2026-10-01T12:00:00Z" }))).toBeNull();
    const failing = syncHealthNote(account({ ...idle, failureStreak: 4, failingSince: "2026-10-01T12:00:00Z", health: "failing" }));
    expect(failing?.text).toMatch(/^Failed 4 times in a row since /);
    expect(failing?.failing).toBe(true);
    expect(syncHealthNote(account({ ...idle, failureStreak: 2, failingSince: "2026-10-01T12:00:00Z" }))?.failing).toBe(false);
    expect(syncHealthNote(account({ ...idle, health: "stale" }))?.text).toMatch(/two of its sync intervals/);
    expect(syncHealthNote(account(idle, "import"))).toBeNull();
  });
});

describe("reconcileNote", () => {
  const idle = { state: "idle" as const, lastRun: null };
  it("says when the server was last checked and what is gone", () => {
    expect(reconcileNote(account(idle))).toEqual({ gone: null, check: "Not compared with the server yet." });
    const checked = {
      ...account({ state: "idle", lastRun: { ...run, status: "ok", reconciledFolders: 4, gone: 2, back: 1, flagsChanged: 5 } }),
      goneMessages: 1234,
      lastReconciledAt: "2026-10-06T10:01:00Z",
    };
    const note = reconcileNote(checked);
    expect(note?.gone).toBe("1,234 messages no longer on the server");
    expect(note?.check).toMatch(/^Last compared with the server .* · last run: 2 gone, 1 back, 5 flag changes$/);
    expect(reconcileNote({ ...checked, goneMessages: 1 })?.gone).toBe("1 message no longer on the server");
  });
  it("is left out for import and removed accounts", () => {
    expect(reconcileNote(account(idle, "import"))).toBeNull();
    expect(reconcileNote({ ...account(idle), removed: true })).toBeNull();
  });
});

describe("bannerState", () => {
  const idle = { state: "idle" as const, lastRun: null };
  const named = (name: string, sync: Partial<SyncInfo>, over: Partial<Account> = {}) => ({ ...account({ ...idle, ...sync }), name, ...over });
  it("lists own failing and stale accounts", () => {
    const data = {
      manage: true, shared: [], syncInterval: "6h0m0s", syncIntervalMin: "5m0s", reconcileInterval: "24h0m0s", alertAfter: 3, smtp: null,
      accounts: [
        named("work", { health: "failing", failureStreak: 3 }),
        named("old", { health: "stale" }),
        named("fine", {}),
        named("off", { health: "failing" }, { enabled: false }),
        named("gone", { health: "failing" }, { removed: true }),
      ],
    };
    expect(bannerState(data)).toEqual({ failing: ["work"], stale: ["old"], otherFailing: 0 });
    expect(bannerState({ ...data, otherFailing: 2 }).otherFailing).toBe(2);
    expect(bannerState(null)).toEqual({ failing: [], stale: [], otherFailing: 0 });
  });
});
