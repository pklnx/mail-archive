import { describe, expect, it } from "vitest";
import type { Account } from "./api";
import { syncableCount, syncStatus } from "./AccountsPage";
import { isActive } from "./useAccounts";

function account(sync: Account["sync"], kind: Account["kind"] = "imap"): Account {
  return {
    name: "a", kind, enabled: kind === "imap", removed: false, host: "h", port: 993, tls: "tls", username: "u",
    includedFolders: [], excludedFolders: [], folders: [], sync,
  };
}

const run = { startedAt: "2026-10-06T10:00:00Z", finishedAt: "2026-10-06T10:01:00Z", fetched: 7, new: 3 };

describe("syncStatus", () => {
  it("describes each state", () => {
    expect(syncStatus(account({ state: "idle", lastRun: null })).text).toBe("Not synced yet");
    expect(syncStatus(account({ state: "queued", lastRun: null })).text).toBe("Waiting to sync…");
    expect(syncStatus(account({ state: "running", lastRun: { ...run, finishedAt: null, status: "running" } })).text).toBe(
      "Syncing: 7 fetched, 3 new",
    );
    expect(syncStatus(account({ state: "idle", lastRun: { ...run, status: "ok" } })).text).toMatch(/^Last sync .*: 3 new$/);
    const failed = syncStatus(account({ state: "idle", lastRun: { ...run, status: "failed", error: "login: denied" } }));
    expect(failed.text).toMatch(/failed$/);
    expect(failed.error).toBe("login: denied");
  });
  it("describes imports", () => {
    expect(syncStatus(account({ state: "idle", lastRun: null }, "import")).text).toMatch(/never synced/);
    expect(syncStatus(account({ state: "running", lastRun: { ...run, finishedAt: null, status: "running" } }, "import")).text).toBe(
      "Importing: 7 read, 3 new",
    );
    expect(syncStatus(account({ state: "idle", lastRun: { ...run, status: "ok" } }, "import")).text).toMatch(/^Last import .*: 3 new$/);
    const partial = syncStatus(account({ state: "idle", lastRun: { ...run, status: "partial", error: "1 message(s) skipped" } }, "import"));
    expect(partial.text).toMatch(/skipped messages$/);
    expect(partial.error).toBe("1 message(s) skipped");
  });
  it("leaves import accounts out of Sync all", () => {
    const idle = { state: "idle" as const, lastRun: null };
    expect(syncableCount([account(idle, "import")])).toBe(0);
    expect(syncableCount([account(idle, "import"), account(idle)])).toBe(1);
    expect(syncableCount([{ ...account(idle), removed: true }])).toBe(0);
  });
  it("polls fast only while a sync is pending", () => {
    const data = { manage: true, syncInterval: "", accounts: [account({ state: "idle", lastRun: null })] };
    expect(isActive(data)).toBe(false);
    expect(isActive({ ...data, accounts: [account({ state: "queued", lastRun: null })] })).toBe(true);
    expect(isActive(null)).toBe(false);
  });
});
