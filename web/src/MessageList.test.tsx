// The message list's status for screen readers (#311, part 1): one visually
// hidden role="status" that stays mounted and reads the state of the last load,
// and no aria-busy on the list.
import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { MessagePage, MessageSummary } from "./api";
import { t } from "./i18n";
import { MessageList } from "./MessageList";
import { expectLoaded, expectLoading } from "./test/loading";

const msg = (id: string): MessageSummary => ({
  id, size: 1, subject: `Subject ${id}`, from: "a@example.com", sentAt: null, sortAt: "2026-10-01T00:00:00Z", hasAttachment: false,
});
const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { "Content-Type": "application/json" } });

/** The pending list requests, answered by the test. */
let pending: { cursor: string | null; resolve: (r: Response) => void; reject: (e: Error) => void }[];
/** The sentinel's IntersectionObserver callbacks. */
let observers: IntersectionObserverCallback[];

beforeEach(() => {
  pending = [];
  observers = [];
  vi.stubGlobal(
    "IntersectionObserver",
    class {
      constructor(cb: IntersectionObserverCallback) {
        observers.push(cb);
      }
      observe() {}
      disconnect() {}
    },
  );
  vi.spyOn(globalThis, "fetch").mockImplementation(
    (url) =>
      new Promise<Response>((resolve, reject) => {
        pending.push({ cursor: new URL(String(url), "http://x").searchParams.get("cursor"), resolve, reject });
      }),
  );
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

/** Answers the oldest pending request with a page. */
async function answer(page: MessagePage) {
  const p = pending.shift()!;
  await act(async () => p.resolve(json(page)));
}

/** The statuses of the list area (the list's own, not a conversation's). */
const statuses = () => screen.queryAllByRole("status");

it("has one status from the first render that reads Loading… and then the count", async () => {
  render(<MessageList filter={{}} selected="" open={() => {}} />);
  expect(statuses()).toHaveLength(1);
  const status = statuses()[0]!;
  expect(status.classList.contains("cds--visually-hidden")).toBe(true);
  expect(status.textContent).toBe(t.loading);
  expect(expectLoading(document.querySelector(".mail-list")!)).toBe(status);
  // Not inside the list, which no longer says it is busy.
  const list = screen.getByRole("list", { name: t.messages });
  expect(list.contains(status)).toBe(false);
  expect(list.hasAttribute("aria-busy")).toBe(false);

  await answer({ messages: [msg("a"), msg("b")], nextCursor: null });
  expect(await screen.findByText("Subject a")).toBeTruthy();
  // The same element, still mounted, now reads the visible count.
  expect(statuses()).toEqual([status]);
  expect(status.textContent).toBe(t.resultCount(2, false, "messages"));
  expectLoaded(document.querySelector(".mail-list")!);
  expect(list.hasAttribute("aria-busy")).toBe(false);
});

it("reads the empty text when nothing is found", async () => {
  render(<MessageList filter={{ q: "nothing" }} selected="" open={() => {}} />);
  const status = statuses()[0]!;
  await answer({ messages: [], nextCursor: null });
  expect(statuses()).toEqual([status]);
  expect(status.textContent).toBe(t.noMatches);
});

it("reads the empty text of a folder without messages", async () => {
  render(<MessageList filter={{}} selected="" open={() => {}} />);
  const status = statuses()[0]!;
  await answer({ messages: [], nextCursor: null });
  expect(status.textContent).toBe(t.noMessages);
});

it("reads the error text after a failed load", async () => {
  render(<MessageList filter={{}} selected="" open={() => {}} />);
  const status = statuses()[0]!;
  await act(async () => pending.shift()!.reject(new Error("boom")));
  expect(statuses()).toEqual([status]);
  expect(status.textContent).toBe(t.loadMessagesFailed("boom"));
});

it("reads Loading… and then the new count for a further page", async () => {
  render(<MessageList filter={{}} selected="" open={() => {}} />);
  const status = statuses()[0]!;
  await answer({ messages: [msg("a")], nextCursor: "c1" });
  expect(status.textContent).toBe(t.resultCount(1, true, "messages"));

  // The sentinel scrolls into view.
  act(() => observers.at(-1)!([{ isIntersecting: true } as IntersectionObserverEntry], {} as IntersectionObserver));
  expect(pending.map((p) => p.cursor)).toEqual(["c1"]);
  expect(statuses()).toEqual([status]);
  expect(status.textContent).toBe(t.loading);
  expect(screen.getByRole("list", { name: t.messages }).hasAttribute("aria-busy")).toBe(false);

  await answer({ messages: [msg("b")], nextCursor: null });
  expect(await screen.findByText("Subject b")).toBeTruthy();
  expect(statuses()).toEqual([status]);
  expect(status.textContent).toBe(t.resultCount(2, false, "messages"));
});
