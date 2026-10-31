// While the filter form's chunk loads (#286): the popover shows Carbon's
// skeleton, and Edit filters has already closed the user panel. Its click
// does not reach window (ActiveFilters), so Carbon's outside click listener
// of the user panel cannot close it, and no focus has moved yet.
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { App } from "./App";
import type { AccountsResponse, User } from "./api";
import { t } from "./i18n";
import { expectLoading } from "./test/loading";

// A chunk that is still on its way.
vi.mock("./FilterPanel", () => new Promise(() => {}));

const accounts: AccountsResponse = {
  accounts: [],
  manage: true, shared: [], syncInterval: "6h0m0s", syncIntervalMin: "5m0s", reconcileInterval: "", alertAfter: 3, smtp: null,
};
const user: User = { name: "alice", admin: false, mustChangePassword: false, twoFactorEnabled: false, twoFactorRequired: false, language: "auto", locale: "auto" };

beforeEach(() => {
  window.history.replaceState(null, "", "/?q=from%3Asam");
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

it("shows the skeleton in the popover, and Edit filters closes the user panel at once", () => {
  render(<App user={user} logout={() => {}} logoutError="" />);
  const userAction = screen.getByRole("button", { name: t.userPanelOf(user.name) });
  fireEvent.click(userAction);
  expect(userAction.getAttribute("aria-expanded")).toBe("true");
  fireEvent.click(screen.getByRole("button", { name: t.editFilters }));
  expect(userAction.getAttribute("aria-expanded")).toBe("false");
  const button = screen.getByRole("button", { name: t.filters });
  expect(button.getAttribute("aria-expanded")).toBe("true");
  const popover = document.getElementById(button.getAttribute("aria-controls")!)!;
  // The skeleton, and "Loading…" only for screen readers (#287).
  expect(popover.querySelector(".app-view-fallback .cds--skeleton__text")).not.toBeNull();
  expectLoading(popover);
  expect(popover.textContent).toBe(t.loading);
});
