// The shortcuts overview loads on demand (#288). The opener's focus is
// captured when the user presses "?", before the chunk is there, so focus
// moved while it loads does not change where it goes back to.
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { App } from "./App";
import type { AccountsResponse, User } from "./api";
import { t } from "./i18n";

// The chunk arrives when the test says so.
const gate = vi.hoisted(() => {
  let open!: () => void;
  const promise = new Promise<void>((resolve) => (open = resolve));
  return { promise, open };
});
vi.mock("./ShortcutsModal", async (importOriginal) => {
  await gate.promise;
  return importOriginal();
});

const accounts: AccountsResponse = {
  accounts: [
    {
      name: "work", kind: "imap", enabled: true, removed: false, purging: false, purge: null, host: "h", port: 993, tls: "tls", username: "u",
      includedFolders: [], excludedFolders: [], syncInterval: "default", goneMessages: 0, lastReconciledAt: null,
      folders: [{ name: "INBOX", delimiter: "/", messages: 5, lastSyncedAt: null, lastReconciledAt: null }],
      addresses: [], lastReceivedAt: null, fill: null, fills: [], firstReceived: null,
      sync: { state: "idle", lastRun: null, failureStreak: 0, failingSince: null, health: "ok", syncEvery: null, nextAt: null },
    },
  ],
  manage: true, syncInterval: "6h0m0s", syncIntervalMin: "5m0s", reconcileInterval: "", alertAfter: 3, smtp: null,
};
const user: User = { name: "alice", admin: false, mustChangePassword: false, twoFactorEnabled: false, twoFactorRequired: false, language: "auto", locale: "auto" };

beforeEach(() => {
  window.history.replaceState(null, "", "/");
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      const body = path.startsWith("/api/accounts") ? accounts : path.startsWith("/api/messages?") ? { messages: [], nextCursor: null } : {};
      return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  localStorage.clear();
});

it("returns the focus to where it was when ? was pressed, also if it moved while the chunk loaded", async () => {
  render(<App user={user} logout={() => {}} logoutError="" />);
  const node = await screen.findByRole("treeitem", { name: "INBOX 5" });
  node.focus();
  fireEvent.keyDown(node, { key: "?" });
  // Still loading: no dialog, no placeholder.
  expect(screen.queryByRole("dialog", { name: t.shortcuts })).toBeNull();
  expect(document.querySelector(".cds--modal")).toBeNull();
  // The user moves on while it loads.
  const search = screen.getByRole("searchbox", { name: t.searchMail });
  search.focus();
  await act(async () => gate.open());
  const dialog = await screen.findByRole("dialog", { name: t.shortcuts });
  expect(dialog.contains(document.activeElement)).toBe(true);
  fireEvent.keyDown(document.activeElement!, { key: "Escape" });
  await vi.waitFor(() => expect(screen.queryByRole("dialog", { name: t.shortcuts })).toBeNull());
  expect(document.activeElement).toBe(node);
});
