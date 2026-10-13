import { afterEach, describe, expect, it, vi } from "vitest";
import type { Account, AccountsResponse, Folder } from "./api";
import { collapseAll, loadCollapsed, prune, rename, reveal, saveCollapsed, toggle } from "./sidebarState";

const f = (name: string): Folder => ({ name, delimiter: "/", messages: 1, lastSyncedAt: null, lastReconciledAt: null });

function account(name: string, folders: string[]): Account {
  return {
    name, kind: "imap", enabled: true, removed: false, host: "h", port: 993, tls: "tls", username: "u",
    includedFolders: [], excludedFolders: [], syncInterval: "default", folders: folders.map(f), goneMessages: 0, lastReconciledAt: null,
    addresses: [], lastReceivedAt: null, fill: null, firstReceived: null,
    sync: { state: "idle", lastRun: null, failureStreak: 0, failingSince: null, health: "ok", syncEvery: null, nextAt: null },
  };
}

const data = (...accounts: Account[]): AccountsResponse => ({
  accounts, manage: true, syncInterval: "6h0m0s", syncIntervalMin: "5m0s", reconcileInterval: "", alertAfter: 3, smtp: null,
});

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});

describe("storage", () => {
  it("keeps the state per user", () => {
    saveCollapsed("alice", { work: ["", "INBOX"] });
    expect(loadCollapsed("alice")).toEqual({ work: ["", "INBOX"] });
    expect(loadCollapsed("bob")).toEqual({});
    expect(JSON.parse(localStorage.getItem("mail-archive.sidebar.alice")!)).toEqual({ v: 1, collapsed: { work: ["", "INBOX"] } });
    saveCollapsed("alice", {});
    expect(localStorage.getItem("mail-archive.sidebar.alice")).toBeNull();
  });
  it("reads invalid or blocked storage as expanded", () => {
    localStorage.setItem("mail-archive.sidebar.alice", "{nope");
    expect(loadCollapsed("alice")).toEqual({});
    localStorage.setItem("mail-archive.sidebar.alice", JSON.stringify({ v: 2, collapsed: { a: [""] } }));
    expect(loadCollapsed("alice")).toEqual({});
    const fail = () => {
      throw new Error("blocked");
    };
    vi.stubGlobal("localStorage", { getItem: fail, setItem: fail, removeItem: fail });
    expect(loadCollapsed("alice")).toEqual({});
    expect(() => saveCollapsed("alice", { a: [""] })).not.toThrow();
  });
});

describe("changes", () => {
  const d = data(account("work", ["INBOX", "Lists/dev/go", "Archive/2024"]), account("home", []));

  it("toggles nodes", () => {
    const c = toggle({}, "work", "Lists");
    expect(c).toEqual({ work: ["Lists"] });
    expect(toggle(c, "work", "Lists")).toEqual({});
  });
  it("keeps path-only nodes when pruning and returns the same object without a change", () => {
    const c = { work: ["", "Lists", "Lists/dev", "Archive"], gone: [""] };
    const p = prune(c, d);
    expect(p).toEqual({ work: ["", "Lists", "Lists/dev", "Archive"] });
    expect(prune(p, d)).toBe(p);
    expect(prune({ work: ["Old"] }, d)).toEqual({});
  });
  it("reveals the path of a folder, not for an account alone", () => {
    const c = { work: ["", "Lists", "Lists/dev", "Archive"] };
    expect(reveal(c, d, "work", "Lists/dev/go")).toEqual({ work: ["Archive"] });
    expect(reveal(c, d, "work", "")).toBe(c);
    expect(reveal(c, d, "work", "INBOX")).toEqual({ work: ["Lists", "Lists/dev", "Archive"] });
  });
  it("moves the state of a renamed account", () => {
    expect(rename({ work: ["Lists"], home: [""] }, "work", "job")).toEqual({ job: ["Lists"], home: [""] });
    const c = { home: [""] };
    expect(rename(c, "work", "job")).toBe(c);
  });
  it("collapses every account with folders and every parent", () => {
    expect(collapseAll(d)).toEqual({ work: ["", "Archive", "Lists", "Lists/dev"] });
  });
});
