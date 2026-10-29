// The messages of an expanded conversation while they load (#287): skeleton
// rows in the shape of the nested rows and a hidden status, not Carbon's
// InlineLoading with a visible "Loading…".
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { MessageSummary } from "./api";
import { t } from "./i18n";
import { MessageList } from "./MessageList";
import { expectLoaded, expectLoading } from "./test/loading";

const msg = (id: string, more: Partial<MessageSummary> = {}): MessageSummary => ({
  id, size: 1, subject: `Subject ${id}`, from: "a@example.com", sentAt: null, sortAt: "2026-10-01T00:00:00Z", hasAttachment: false, ...more,
});
const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { "Content-Type": "application/json" } });

let release: () => void;
/** The messages of the grouped row's conversation. */
let count: number;

beforeEach(() => {
  vi.stubGlobal(
    "IntersectionObserver",
    class {
      observe() {}
      disconnect() {}
    },
  );
  count = 3;
  const released = new Promise<void>((r) => (release = r));
  vi.spyOn(globalThis, "fetch").mockImplementation(async (url) => {
    const u = new URL(String(url), "http://x");
    if (u.searchParams.get("thread")) {
      await released;
      return json({ messages: [msg("c3", { thread: "t" }), msg("c2", { thread: "t" }), msg("c1", { thread: "t" })], nextCursor: null });
    }
    return json({ messages: [msg("c3", { thread: "t", count })], nextCursor: null });
  });
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

it("shows skeleton rows with a hidden status while a conversation's messages load", async () => {
  render(<MessageList filter={{ group: true }} selected="" open={() => {}} />);
  fireEvent.click(await screen.findByRole("button", { name: t.showConversation(2) }));
  const members = screen.getByRole("list", { name: t.conversation });
  expectLoading(members);
  // One skeleton row per earlier message (at most three), in the nested rows' shape.
  const rows = members.querySelectorAll(".mail-row--skeleton");
  expect(rows).toHaveLength(2);
  for (const r of rows) expect(r.className).toContain("mail-row--nested");
  expect(members.querySelector(".cds--inline-loading")).toBeNull();
  await act(async () => release());
  expect(await within(members).findByText("Subject c2")).toBeTruthy();
  expectLoaded(members);
});

it("shows at most three skeleton rows for a long conversation", async () => {
  count = 12;
  render(<MessageList filter={{ group: true }} selected="" open={() => {}} />);
  fireEvent.click(await screen.findByRole("button", { name: t.showConversation(11) }));
  const members = screen.getByRole("list", { name: t.conversation });
  expectLoading(members);
  expect(members.querySelectorAll(".mail-row--skeleton")).toHaveLength(3);
});
