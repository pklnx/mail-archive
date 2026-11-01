// The message list's status for screen readers (#311, part 1): one visually
// hidden role="status" that stays mounted and reads the state of the last load,
// and no aria-busy on the list. Also the "Only in archive" tag from the rows' flag (#310),
// and the attachment a search matched by (#390, part 4).
import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { MessagePage, MessageSummary } from "./api";
import { applySettings, t } from "./i18n";
import { MessageList, splitExtension } from "./MessageList";
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

/** The row button of a subject, and whether it holds the "Only in archive" tag. */
const tagged = (subject: string) => {
  const row = screen.getByText(subject).closest("button")!;
  return [...row.querySelectorAll(".cds--tag")].some((tag) => tag.textContent === t.onlyInArchive);
};

it("tags each row that is only in the archive in any listing, from the row's flag (#310)", async () => {
  render(<MessageList filter={{ account: "a" }} selected="" open={() => {}} />);
  await answer({ messages: [{ ...msg("a"), gone: true }, msg("b")], nextCursor: null });
  expect(await screen.findByText("Subject a")).toBeTruthy();
  expect(tagged("Subject a")).toBe(true);
  expect(tagged("Subject b")).toBe(false);
});

it("tags every row of the gone view from its flag", async () => {
  render(<MessageList filter={{ gone: true }} selected="" open={() => {}} />);
  await answer({ messages: [{ ...msg("a"), gone: true }, { ...msg("b"), gone: true }], nextCursor: null });
  expect(await screen.findByText("Subject a")).toBeTruthy();
  expect(tagged("Subject a")).toBe(true);
  expect(tagged("Subject b")).toBe(true);
});

it("tags the rows of an expanded conversation that are only in the archive", async () => {
  render(<MessageList filter={{ group: true }} selected="" open={() => {}} />);
  await answer({ messages: [{ ...msg("a"), thread: "t", count: 3 }], nextCursor: null });
  expect(tagged("Subject a")).toBe(false);
  await act(async () => screen.getByRole("button", { name: t.showConversation(2) }).click());
  await answer({ messages: [{ ...msg("a"), thread: "t" }, { ...msg("b"), gone: true }, msg("c")], nextCursor: null });
  expect(await screen.findByText("Subject b")).toBeTruthy();
  expect(tagged("Subject b")).toBe(true);
  expect(tagged("Subject c")).toBe(false);
});

/** The snippet line of a subject's row. */
const snippetOf = (subject: string) => screen.getByText(subject).closest("button")!.querySelector(".mail-row__snippet");
/** The "In <attachment>:" line of a subject's row. */
const matchOf = (subject: string) => screen.getByText(subject).closest("button")!.querySelector(".mail-row__match");

it("names the attachment a search matched by on its own line above its snippet, in English and German (#390, part 4; #479)", async () => {
  const rows: MessageSummary[] = [
    { ...msg("a"), hasAttachment: true, snippet: "\uE000Rechnungsbetrag\uE001 1.234,56 €", matchedAttachment: { part: 3, filename: "Rechnung-2026-10.pdf" } },
    { ...msg("b"), hasAttachment: true, snippet: "Siehe \uE000Rechnungsbetrag\uE001 im Text" },
    { ...msg("c"), hasAttachment: true, snippet: "\uE000Rechnungsbetrag\uE001", matchedAttachment: { part: 4, filename: "" } },
  ];
  for (const [language, unnamed] of [
    ["en", "In part 4:"],
    ["de", "In Teil 4:"],
  ] as const) {
    applySettings({ language, locale: language });
    try {
      render(<MessageList filter={{ q: "Rechnungsbetrag" }} selected="" open={() => {}} />);
      await answer({ messages: rows, nextCursor: null });
      expect(await screen.findByText("Subject a")).toBeTruthy();
      // The name is a sibling before the snippet, not inside its two clamped lines (#479).
      const match = matchOf("Subject a")!;
      // Screen readers get the text in one piece; the cut line is hidden from them.
      expect(match.querySelector(".cds--visually-hidden")?.textContent).toBe("In Rechnung-2026-10.pdf:");
      const line = match.querySelector(".mail-row__match-line")!;
      expect(line.getAttribute("aria-hidden")).toBe("true");
      expect(line.textContent).toBe("In Rechnung-2026-10.pdf:");
      expect(match.querySelector(".mail-row__match-name")?.textContent).toBe("Rechnung-2026-10");
      expect(match.querySelector(".mail-row__match-ext")?.textContent).toBe(".pdf");
      const a = snippetOf("Subject a")!;
      expect(a.textContent).toBe("Rechnungsbetrag 1.234,56 €");
      expect(a.querySelector(".mail-row__match")).toBeNull();
      expect(a.querySelector("mark")?.textContent).toBe("Rechnungsbetrag");
      // The row's name reads the full file name and keeps it apart from the snippet.
      expect(screen.getByRole("button", { name: /In Rechnung-2026-10\.pdf: Rechnungsbetrag/ })).toBeTruthy();
      // The paperclip is decoration, in the hidden line; the text names the file.
      expect(line.querySelector("svg")).toBeTruthy();
      // A body match: the snippet alone.
      expect(snippetOf("Subject b")!.textContent).toBe("Siehe Rechnungsbetrag im Text");
      expect(matchOf("Subject b")).toBeNull();
      // Without a file name: the part, in the user's language, and no extension.
      expect(matchOf("Subject c")!.querySelector(".cds--visually-hidden")?.textContent).toBe(unnamed);
      expect(matchOf("Subject c")!.querySelector(".mail-row__match-line")?.textContent).toBe(unnamed);
      expect(matchOf("Subject c")!.querySelector(".mail-row__match-ext")).toBeNull();
      expect(snippetOf("Subject c")!.textContent).toBe("Rechnungsbetrag");
    } finally {
      cleanup();
      applySettings({ language: "en", locale: "en" });
    }
  }
});

it.each([
  ["Rechnung-2026-10.pdf", "Rechnung-2026-10", ".pdf"],
  ["a.tar.gz", "a.tar", ".gz"],
  [".bashrc", ".bashrc", ""],
  ["name.", "name.", ""],
  ["README", "README", ""],
  ["Report v1.2 final", "Report v1.2 final", ""],
  ["photo.JPEG", "photo", ".JPEG"],
  ["archive.toolongextension", "archive.toolongextension", ""],
  ["Teil 4", "Teil 4", ""],
])("splitExtension(%j) is [%j, %j] (#479)", (name, stem, ext) => {
  expect(splitExtension(name)).toEqual([stem, ext]);
});
