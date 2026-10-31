// The shortcuts overview loads on demand (#288). When its chunk does not
// load, the load-error notice floats in its place, and Escape or its close
// button closes it; the mail view stays usable.
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { App } from "./App";
import type { AccountsResponse, User } from "./api";
import { t } from "./i18n";

// The chunk is gone, as after a server upgrade (a 404 for its old name).
vi.mock("./ShortcutsModal", () => {
  throw new TypeError("Failed to fetch dynamically imported module");
});

const accounts: AccountsResponse = {
  accounts: [
    {
      name: "work", kind: "imap", enabled: true, removed: false, purging: false, purge: null, host: "h", port: 993, tls: "tls", username: "u",
      includedFolders: [], excludedFolders: [], syncInterval: "default", goneMessages: 0, lastReconciledAt: null,
      folders: [{ name: "INBOX", delimiter: "/", messages: 5, lastSyncedAt: null, lastReconciledAt: null }],
      addresses: [], lastReceivedAt: null, fill: null, fills: [], firstReceived: null, sharedWith: [],
      sync: { state: "idle", lastRun: null, failureStreak: 0, failingSince: null, health: "ok", syncEvery: null, nextAt: null },
    },
  ],
  manage: true, shared: [], syncInterval: "6h0m0s", syncIntervalMin: "5m0s", reconcileInterval: "", alertAfter: 3, smtp: null,
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

it("shows the notice in place of the overview; Escape and the close button close it", async () => {
  vi.spyOn(console, "warn").mockImplementation(() => {});
  render(<App user={user} logout={() => {}} logoutError="" />);
  const node = await screen.findByRole("treeitem", { name: "INBOX 5" });
  node.focus();
  fireEvent.keyDown(node, { key: "?" });
  const notice = () => screen.queryByText(t.viewLoadFailed);
  const title = await screen.findByText(t.viewLoadFailed);
  expect(title.closest('[role="alert"]')?.textContent).toContain(t.viewLoadFailedText);
  expect(title.closest(".app-load-error--floating")?.parentElement).toBe(document.body);
  expect(screen.queryByRole("dialog", { name: t.shortcuts })).toBeNull();
  // The app is still there.
  expect(screen.getByRole("searchbox", { name: t.searchMail })).toBeTruthy();
  fireEvent.keyDown(document.body, { key: "Escape" });
  await waitFor(() => expect(notice()).toBeNull());

  // Again from the user panel; its close button closes it.
  fireEvent.click(screen.getByRole("button", { name: t.userPanelOf(user.name) }));
  fireEvent.click(screen.getByRole("button", { name: t.shortcuts }));
  await screen.findByText(t.viewLoadFailed);
  fireEvent.click(screen.getByRole("button", { name: t.carbon.closeNotification }));
  await waitFor(() => expect(notice()).toBeNull());
  // The other shortcuts still work.
  fireEvent.keyDown(document.body, { key: "/" });
  expect(document.activeElement).toBe(screen.getByRole("searchbox", { name: t.searchMail }));
});
