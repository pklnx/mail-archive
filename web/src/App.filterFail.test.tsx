// The filter form loads on demand (#286). When its chunk does not load, the
// load-error notice with Retry shows inside the popover; the tags and the
// rest of the mail view stay usable.
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { App } from "./App";
import type { AccountsResponse, User } from "./api";
import { t } from "./i18n";

// The chunk is gone, as after a server upgrade (a 404 for its old name).
vi.mock("./FilterPanel", () => {
  throw new TypeError("Failed to fetch dynamically imported module");
});

const accounts: AccountsResponse = {
  accounts: [],
  manage: true, syncInterval: "6h0m0s", syncIntervalMin: "5m0s", reconcileInterval: "", alertAfter: 3, smtp: null,
};
const user: User = { name: "alice", admin: false, mustChangePassword: false, twoFactorEnabled: false, twoFactorRequired: false, language: "auto", locale: "auto" };

// jsdom lays nothing out; the popover's autoAlign would hide its content (App.test.tsx).
const rect = Element.prototype.getBoundingClientRect;
beforeEach(() => {
  window.history.replaceState(null, "", "/?q=from%3Asam");
  Element.prototype.getBoundingClientRect = () => DOMRect.fromRect({ x: 100, y: 100, width: 32, height: 32 });
  Object.defineProperty(document.documentElement, "clientWidth", { configurable: true, value: 1024 });
  Object.defineProperty(document.documentElement, "clientHeight", { configurable: true, value: 768 });
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
  Element.prototype.getBoundingClientRect = rect;
  vi.unstubAllGlobals();
  localStorage.clear();
});

it("shows the notice with Retry in the popover; the tags still work", async () => {
  vi.spyOn(console, "warn").mockImplementation(() => {});
  render(<App user={user} logout={() => {}} />);
  const button = screen.getByRole("button", { name: t.filters });
  fireEvent.click(button);
  const title = await screen.findByText(t.viewLoadFailed);
  const popover = document.getElementById(button.getAttribute("aria-controls")!)!;
  expect(popover.contains(title)).toBe(true);
  expect(within(popover).getByRole("button", { name: t.retry })).toBeTruthy();
  expect(screen.queryByRole("group", { name: t.filters })).toBeNull();
  // Escape on the button closes it; the tags are still there and remove their filter.
  fireEvent.keyDown(button, { key: "Escape" });
  expect(screen.queryByText(t.viewLoadFailed)).toBeNull();
  fireEvent.click(within(screen.getByRole("list", { name: t.activeFilters })).getByRole("button", { name: t.removeFilter(t.from) }));
  expect((screen.getByRole("searchbox", { name: t.searchMail }) as HTMLInputElement).value).toBe("");
});
