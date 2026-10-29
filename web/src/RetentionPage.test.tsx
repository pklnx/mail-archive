// The Retention page (#168 part 5, #285 part 3): a tab each for rules, legal
// holds and deletion runs, in the URL, with a table and a page per entry.
// The danger confirmations send their request only after confirming, and
// the same one as before.
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { DeletionReview, DeletionRun, LegalHold, RetentionRule, RetentionRules } from "./api";
import { day, retentionDay } from "./format";
import { applySettings, messagesFor, t } from "./i18n";
import { accountLabel, countMatches, holdScope, RetentionPage, runOpen } from "./RetentionPage";
import { RunPage } from "./RetentionRun";
import { settingsTextFor } from "./settingsText";
import { expectLoaded, expectLoading } from "./test/loading";

const s = settingsTextFor("en");

const hold = (h: Partial<LegalHold>): LegalHold => ({
  id: 1,
  name: "audit",
  reason: "",
  user: "",
  accountId: null,
  account: "",
  accountOwner: "",
  filter: null,
  since: null,
  until: null,
  matchedAt: null,
  matched: 0,
  createdAt: "2026-10-01T00:00:00Z",
  createdBy: "admin",
  releasedAt: null,
  releasedBy: "",
  releaseNote: "",
  ...h,
});

describe("holdScope", () => {
  it("names the whole archive without conditions", () => {
    expect(holdScope(hold({}))).toBe(t.holdScopeAll);
  });
  it("lists every condition", () => {
    const s = holdScope(hold({ user: "alice", account: "work", accountOwner: "alice", filter: { q: "falcon", from: "x.example" } }));
    expect(s).toContain(t.holdScopeUser("alice"));
    expect(s).toContain(t.holdScopeAccount("work", "alice"));
    expect(s).toContain("falcon from:x.example");
    expect(s).toContain(t.holdNotMatched);
  });
  it("shows the matches once matched", () => {
    expect(holdScope(hold({ filter: { q: "x" }, matchedAt: "2026-10-01T00:00:00Z", matched: 3 }))).toContain(t.holdMatched(3));
  });
});

describe("accountLabel", () => {
  it("shows owner, name and removal", () => {
    expect(accountLabel({ owner: "alice", name: "work", removed: false })).toBe("alice / work");
    expect(accountLabel({ owner: "", name: "old", removed: true })).toBe(`- / old (${t.ruleRemoved})`);
  });
});

describe("retentionDay", () => {
  it("shows the last day of the period, not the first day after it", () => {
    // Midnight 1 January 2026 in Berlin is the first instant after the period.
    const shown = retentionDay("2025-12-31T23:00:00Z");
    expect(shown).toMatch(/2025/);
    expect(shown).not.toMatch(/2026/);
    expect(retentionDay("nonsense")).toBe("");
  });
});

describe("runOpen", () => {
  it("is open while proposed or deleting", () => {
    expect(runOpen({ state: "proposed" })).toBe(true);
    expect(runOpen({ state: "confirmed" })).toBe(true);
    expect(runOpen({ state: "done" })).toBe(false);
    expect(runOpen({ state: "cancelled" })).toBe(false);
  });
});

describe("countMatches", () => {
  it("takes only the exact count", () => {
    expect(countMatches("2", 2)).toBe(true);
    expect(countMatches(" 2 ", 2)).toBe(true);
    expect(countMatches("0", 0)).toBe(true);
    for (const wrong of ["", " ", "1", "3", "02", "2.0", "2x", "+2", "1e1"]) expect(countMatches(wrong, 2), wrong).toBe(false);
    expect(countMatches("", 0)).toBe(false);
  });
});

const rule5: RetentionRule = {
  id: 5, accountId: 1, account: "personal", accountKind: "imap", accountRemoved: false, owner: "demo", folder: "Archive/2014", years: 10,
  timeZone: "Europe/Berlin", note: "GoBD", createdAt: "2026-10-01T00:00:00Z", createdBy: "demo",
};
const rule7: RetentionRule = {
  id: 7, accountId: 2, account: "old", accountKind: "import", accountRemoved: true, owner: "", folder: "", years: 8,
  timeZone: "Europe/Berlin", note: "", createdAt: "2026-10-02T00:00:00Z", createdBy: "",
};
const what5 = "demo / personal / Archive/2014";
const what7 = `- / old (${t.ruleRemoved}) / ${t.ruleAllFolders}`;
const rules = (list: RetentionRule[]): RetentionRules => ({
  rules: list,
  accounts: [
    { id: 1, name: "personal", kind: "imap", removed: false, owner: "demo", folders: ["INBOX", "Archive/2014"] },
    { id: 2, name: "old", kind: "import", removed: true, owner: "", folders: [] },
  ],
  minYears: 6,
  defaultTimeZone: "Europe/Berlin",
});

const run = (r: Partial<DeletionRun>): DeletionRun => ({
  id: 3, state: "proposed", createdAt: "2026-10-20T00:00:00Z", createdBy: "demo", candidates: 2, bytes: 2048, confirmedAt: null, confirmedBy: "",
  finishedAt: null, deleted: 0, kept: 0, ...r,
});

let ruleList: RetentionRule[];
let holdList: LegalHold[];
let runs: DeletionRun[];
let review: DeletionReview;
let requests: { method: string; url: string; body: unknown }[];
let confirmFails: boolean;
/** Loading the runs fails (after an action, for example). */
let runsFail: boolean;
/** A test's own answer to a change, if it has one. */
let answer: ((method: string, url: string) => Response | Promise<Response> | undefined) | null;
/** Holds a read back until the promise it returns settles (a rejection fails the request). */
let gate: ((url: string) => Promise<void> | undefined) | null;
const changes = () => requests.filter((r) => r.method !== "GET");
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });

beforeEach(() => {
  window.history.replaceState(null, "", "/?view=retention");
  requests = [];
  confirmFails = false;
  runsFail = false;
  answer = null;
  gate = null;
  ruleList = [rule5, rule7];
  holdList = [
    hold({ id: 8, name: "Case 4711", user: "alice", reason: "lawsuit 12 O 345/26" }),
    hold({ id: 9, name: "Old case", releasedAt: "2026-10-02T00:00:00Z", releasedBy: "demo", releaseNote: "closed" }),
  ];
  runs = [run({ id: 2, state: "done", candidates: 4, deleted: 3, kept: 1 }), run({})];
  review = {
    run: run({}),
    groups: [{ owner: "demo", account: "personal", folder: "Archive/2014", year: 2014, messages: 2 }],
    candidates: [
      { sha256: "c".repeat(64), size: 1, retainUntil: "", state: "pending" },
      { sha256: "d".repeat(64), size: 1, retainUntil: "", state: "pending" },
    ],
    listed: 2,
  };
  vi.spyOn(globalThis, "fetch").mockImplementation(async (url, init) => {
    const method = init?.method ?? "GET";
    const u = String(url);
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    requests.push({ method, url: u, body });
    if (method === "GET" && gate) await gate(u);
    if (method !== "GET" && answer) {
      const res = await answer(method, u);
      if (res) return res;
    }
    if (method === "GET" && u === "/api/retention/rules") return json(rules(ruleList));
    if (method === "GET" && u.startsWith("/api/legal-holds")) {
      return json({ holds: holdList.filter((h) => u.includes("released=1") || !h.releasedAt) });
    }
    if (method === "GET" && u === "/api/users") return json({ users: [{ name: "alice" }] });
    if (method === "GET" && u === "/api/retention/runs") return runsFail ? json({ error: "database unavailable" }, 503) : json({ runs });
    if (method === "GET" && u.startsWith("/api/retention/runs/")) {
      // The review has the run's current state, as the server's does.
      const now = runs.find((r) => r.id === review.run.id);
      return json({ ...review, run: { ...review.run, state: now?.state ?? review.run.state } });
    }
    if (method === "POST" && u === "/api/retention/runs") {
      // As the server does: the new run is listed from now on.
      runs = [...runs, run({ id: 4 })];
      return json(run({ id: 4 }), 201);
    }
    if (method === "POST" && u === "/api/retention/rules") return json({ id: 6 }, 201);
    if (method === "DELETE" && u.startsWith("/api/retention/rules/")) {
      ruleList = ruleList.filter((r) => `/api/retention/rules/${r.id}` !== u);
      return new Response(null, { status: 204 });
    }
    if (method === "POST" && u === "/api/legal-holds") return json({ id: 10, matched: 1 }, 201);
    const release = /^\/api\/legal-holds\/(\d+)\/release$/.exec(u);
    if (method === "POST" && release) {
      holdList = holdList.map((h) => (h.id === Number(release[1]) ? { ...h, releasedAt: "2026-10-25T00:00:00Z", releasedBy: "demo", releaseNote: body.note } : h));
      return new Response(null, { status: 204 });
    }
    if (method === "POST" && u.endsWith("/confirm")) {
      if (confirmFails) return json({ error: "type the number of messages (2) to confirm" }, 400);
      // As the server does: 202 without a body, and the run is deleting.
      runs = runs.map((r) => (r.state === "proposed" ? { ...r, state: "confirmed" } : r));
      return new Response(null, { status: 202 });
    }
    if (method === "POST" && u.endsWith("/cancel")) {
      runs = runs.map((r) => (`/api/retention/runs/${r.id}/cancel` === u ? { ...r, state: "cancelled" } : r));
      return new Response(null, { status: 204 });
    }
    return new Response(null, { status: 204 });
  });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  window.history.replaceState(null, "", "/");
});

const dialog = () => screen.getByRole("dialog");
const h1 = (name: string) => screen.getByRole("heading", { level: 1, name });
const tab = (name: string) => screen.getByRole("tab", { name });
/** The row of an entry, by the text of its name link. */
const row = (name: string) => screen.getByRole("link", { name }).closest("tr")!;
const menuButton = (name: string) => screen.getByRole("button", { name: t.userActions(name) });

/** Opens the actions menu of an entry and picks an item. */
function pick(name: string, item: string) {
  fireEvent.click(menuButton(name));
  fireEvent.click(screen.getByRole("menuitem", { name: item }));
}

/** Shows a page from the URL, as a reload or a bookmark does. */
async function show(query: string, ready: string) {
  window.history.replaceState(null, "", `/?view=retention${query}`);
  render(<RetentionPage />);
  await screen.findAllByText(ready);
}

/** Opens run 3's page from the URL. */
async function showRun3() {
  window.history.replaceState(null, "", "/?view=retention&tab=runs&id=3");
  render(<RetentionPage />);
  await screen.findByRole("heading", { level: 1, name: t.runLine(3, 2, "2.0 KB") });
  return screen.findByRole("button", { name: s.deleteCount(2) });
}

/** Opens the Delete dialog of run 3: its count field. */
async function openDelete() {
  fireEvent.click(await showRun3());
  return within(dialog()).findByLabelText(t.runConfirmPrompt(2));
}

/** The state tag in the title row of run 3's page. */
const runTitle = (state: string) => within(h1(t.runLine(3, 2, "2.0 KB")).closest("header")!).getByText(t.runState(state));

/** Holds back the reads whose URL starts with prefix; the returned function answers them. */
function holdBack(prefix: string) {
  let release!: () => void;
  const released = new Promise<void>((r) => {
    release = r;
  });
  gate = (u) => (u.startsWith(prefix) ? released : undefined);
  return () => act(async () => release());
}

/** The header texts of the table skeleton in root. */
const skeletonHeaders = (root: HTMLElement) => [...root.querySelectorAll(".cds--data-table.cds--skeleton th")].map((th) => th.textContent);

describe("loading states (#287)", () => {
  it("shows the rules table as a skeleton while it loads, then the rules", async () => {
    const release = holdBack("/api/retention/rules");
    render(<RetentionPage />);
    const status = expectLoading()!;
    // Below 42rem (jsdom has no matching media query): the columns a phone shows.
    expect(skeletonHeaders(status)).toEqual([s.colRule, s.colPeriod, ""]);
    await release();
    await screen.findByText(what5);
    expectLoaded();
  });

  it("shows the holds and the runs as skeletons while they load", async () => {
    window.history.replaceState(null, "", "/?view=retention&tab=holds");
    let release = holdBack("/api/legal-holds");
    render(<RetentionPage />);
    expect(skeletonHeaders(expectLoading()!)).toEqual([s.colHold, s.colStatus, ""]);
    // The toolbar with Add stays usable above it.
    expect(screen.getByRole("button", { name: t.holdAdd })).toBeTruthy();
    await release();
    await screen.findByText("Case 4711");
    expectLoaded();
    release = holdBack("/api/retention/runs");
    fireEvent.click(tab(t.runsTab));
    expect(skeletonHeaders(expectLoading()!)).toEqual([t.runColRun, t.runColState]);
    await release();
    await screen.findByText(t.runLine(3, 2, "2.0 KB"));
    expectLoaded();
  });

  it("shows the error, not a skeleton, when the holds do not load", async () => {
    window.history.replaceState(null, "", "/?view=retention&tab=holds");
    gate = (u) => (u.startsWith("/api/legal-holds") ? Promise.reject(new TypeError("Failed to fetch")) : undefined);
    render(<RetentionPage />);
    expect((await screen.findByRole("alert")).textContent).toContain("Failed to fetch");
    expectLoaded();
  });

  it("shows a page skeleton while an entry's list loads, then its page", async () => {
    window.history.replaceState(null, "", "/?view=retention&id=5");
    const release = holdBack("/api/retention/rules");
    render(<RetentionPage />);
    expect(expectLoading()!.querySelector(".cds--skeleton__heading")).not.toBeNull();
    await release();
    expect(await screen.findByRole("heading", { level: 1, name: what5 })).toBeTruthy();
    expectLoaded();
  });

  it("shows a form skeleton under the title while an add page loads", async () => {
    window.history.replaceState(null, "", "/?view=retention&add=rule");
    const release = holdBack("/api/retention/rules");
    render(<RetentionPage />);
    expect(h1(t.ruleAdd)).toBeTruthy();
    expect(expectLoading()!.querySelector(".cds--skeleton__text")).not.toBeNull();
    await release();
    await waitFor(() => expectLoaded());
  });

  it("shows a skeleton for a run's groups while its review loads", async () => {
    window.history.replaceState(null, "", "/?view=retention&tab=runs&id=3");
    const release = holdBack("/api/retention/runs/3");
    render(<RetentionPage />);
    // The run's head and tiles come from the list; the review loads after.
    await screen.findByRole("heading", { level: 1, name: t.runLine(3, 2, "2.0 KB") });
    expect(document.querySelector(".app-summary__tile")).not.toBeNull();
    expectLoading();
    await release();
    expect(await screen.findByRole("region", { name: s.groups })).toBeTruthy();
    expectLoaded();
  });
});

describe("RetentionPage", () => {
  it("has the three sections as Carbon tabs in the URL, and loads each when it is shown", async () => {
    await show("", what5);
    const tabs = screen.getAllByRole("tab");
    expect(tabs.map((tab) => tab.textContent)).toEqual([t.retentionRulesTab, t.retentionHoldsTab, t.runsTab]);
    expect(tabs[0]!.getAttribute("aria-selected")).toBe("true");
    expect(tabs[0]!.className).toContain("cds--tabs__nav-item");
    // One helper line, with the guide for the rest.
    expect(screen.getByText(s.rulesHelp, { exact: false })).toBeTruthy();
    expect(screen.getByRole("link", { name: s.guideLink }).getAttribute("href")).toBe("https://pklnx.github.io/mail-archive/guide/retention#rules");
    expect(requests.some((r) => r.url.startsWith("/api/legal-holds") || r.url.startsWith("/api/retention/runs"))).toBe(false);
    const length = window.history.length;
    fireEvent.click(tab(t.retentionHoldsTab));
    // A tab replaces the URL: Back does not walk through the tabs.
    expect(window.location.search).toBe("?view=retention&tab=holds");
    expect(window.history.length).toBe(length);
    expect(tab(t.retentionHoldsTab).getAttribute("aria-selected")).toBe("true");
    await screen.findByText("Case 4711");
    expect(screen.queryByText(s.rulesHelp, { exact: false })).toBeNull();
    fireEvent.click(tab(t.runsTab));
    expect(window.location.search).toBe("?view=retention&tab=runs");
    fireEvent.click(tab(t.retentionRulesTab));
    expect(window.location.search).toBe("?view=retention");
  });

  it("opens the tab of the URL on a reload", async () => {
    await show("&tab=runs", t.runLine(3, 2, "2.0 KB"));
    expect(tab(t.runsTab).getAttribute("aria-selected")).toBe("true");
    expect(requests.some((r) => r.url.startsWith("/api/legal-holds"))).toBe(false);
    expect(document.activeElement).toBe(document.body);
  });

  describe("rules", () => {
    it("has a row per rule with the rule as its row header, a link to its page and a menu with Remove", async () => {
      await show("", what5);
      const headers = screen.getAllByRole("rowheader");
      expect(headers.map((h) => within(h).getByRole("link").textContent)).toEqual([what5, what7]);
      expect(headers.every((h) => h.tagName === "TH" && h.getAttribute("scope") === "row")).toBe(true);
      expect(within(row(what5)).getByRole("link", { name: what5 }).getAttribute("href")).toBe("?view=retention&id=5");
      expect(within(row(what5)).getByText(`${t.ruleYearsCount(10)} · Europe/Berlin`)).toBeTruthy();
      // The secondary columns, and the second line that replaces them below 42rem.
      const wide = screen.getAllByRole("columnheader").filter((h) => h.classList.contains("app-wide-only")).map((h) => h.textContent);
      expect(wide).toEqual([t.ruleNote, s.created]);
      expect(within(row(what5)).getAllByText("GoBD")).toHaveLength(2);
      expect(within(row(what5)).getAllByText("GoBD")[0]!.closest(".app-narrow-only")).toBeTruthy();
      // The one primary: Add rule, above the table.
      expect(screen.getByRole("button", { name: t.ruleAdd }).className).toContain("cds--btn--primary");
      fireEvent.click(menuButton(what5));
      const items = screen.getAllByRole("menuitem");
      expect(items.map((i) => i.textContent)).toEqual([t.ruleRemove]);
      expect(items[0]!.className).toContain("danger");
    });

    it("removes a rule from its row only after the danger confirmation, and puts the focus on the Rules tab", async () => {
      await show("", what5);
      pick(what5, t.ruleRemove);
      expect(dialog().closest(".cds--modal")!.className).toContain("cds--modal--danger");
      expect(within(dialog()).getByText(t.ruleRemoveTitle(what5))).toBeTruthy();
      fireEvent.click(within(dialog()).getByRole("button", { name: t.cancel }));
      expect(changes()).toEqual([]);
      expect(document.activeElement).toBe(menuButton(what5));
      pick(what5, t.ruleRemove);
      fireEvent.click(within(dialog()).getByRole("button", { name: t.ruleRemove }));
      await waitFor(() => expect(changes()).toEqual([{ method: "DELETE", url: "/api/retention/rules/5", body: undefined }]));
      await waitFor(() => expect(screen.queryByRole("link", { name: what5 })).toBeNull());
      expect(document.activeElement).toBe(tab(t.retentionRulesTab));
    });

    it("keeps the row and the focus on its menu when removing the rule fails", async () => {
      answer = (method) => (method === "DELETE" ? json({ error: "database unavailable" }, 503) : undefined);
      await show("", what5);
      pick(what5, t.ruleRemove);
      fireEvent.click(within(dialog()).getByRole("button", { name: t.ruleRemove }));
      expect(await screen.findByText(t.failed("database unavailable"))).toBeTruthy();
      expect(row(what5)).toBeTruthy();
      expect(document.activeElement).toBe(menuButton(what5));
    });

    it("opens a rule's page from its row as a history entry; the breadcrumb and Back return", async () => {
      await show("", what5);
      const length = window.history.length;
      expect(fireEvent.click(within(row(what5)).getByRole("link", { name: what5 }))).toBe(false);
      expect(window.location.search).toBe("?view=retention&id=5");
      expect(window.history.length).toBe(length + 1);
      expect(document.activeElement).toBe(h1(what5));
      const crumbs = screen.getByRole("navigation", { name: t.carbon.breadcrumb });
      const back = within(crumbs).getByRole("link", { name: t.retentionTitle });
      expect(back.getAttribute("href")).toBe("?view=retention");
      fireEvent.click(back);
      expect(window.location.search).toBe("?view=retention");
      expect(document.activeElement).toBe(h1(t.retentionTitle));
      act(() => window.history.back());
      await waitFor(() => expect(window.location.search).toBe("?view=retention&id=5"));
      expect(document.activeElement).toBe(h1(what5));
    });

    it("shows a rule's page: its period, time zone, creation and note, and a removed account's tag", async () => {
      await show("&id=5", what5);
      expect(document.activeElement).toBe(document.body);
      const tiles = Object.fromEntries([...document.querySelectorAll(".app-summary__tile")].map((el) => [el.querySelector("dt")!.textContent, el.querySelector("dd")!.textContent]));
      expect(tiles).toEqual({
        [s.colPeriod]: t.ruleYearsCount(10),
        [t.ruleTimeZone]: "Europe/Berlin",
        [s.created]: t.ruleCreated("demo", day("2026-10-01T00:00:00Z")),
      });
      expect(within(screen.getByRole("region", { name: t.ruleNote })).getByText("GoBD")).toBeTruthy();
      expect(screen.queryByText(s.accountRemoved)).toBeNull();
      cleanup();
      await show("&id=7", `- / old / ${t.ruleAllFolders}`);
      expect(within(h1(`- / old / ${t.ruleAllFolders}`).closest("header")!).getByText(s.accountRemoved)).toBeTruthy();
      expect(screen.queryByRole("region", { name: t.ruleNote })).toBeNull();
    });

    it("removes a rule from its page only after confirming, then shows the list in its place", async () => {
      window.history.replaceState(null, "", "/?view=log");
      window.history.pushState(null, "", "/?view=retention&id=5");
      render(<RetentionPage />);
      await screen.findByRole("heading", { level: 1, name: what5 });
      pick(what5, t.ruleRemove);
      fireEvent.click(within(dialog()).getByRole("button", { name: t.cancel }));
      expect(changes()).toEqual([]);
      expect(document.activeElement).toBe(menuButton(what5));
      pick(what5, t.ruleRemove);
      fireEvent.click(within(dialog()).getByRole("button", { name: t.ruleRemove }));
      await waitFor(() => expect(window.location.search).toBe("?view=retention"));
      expect(changes()).toEqual([{ method: "DELETE", url: "/api/retention/rules/5", body: undefined }]);
      await waitFor(() => expect(document.activeElement).toBe(h1(t.retentionTitle)));
      // The list replaced the page: Back does not lead to the removed rule.
      act(() => window.history.back());
      await waitFor(() => expect(window.location.search).toBe("?view=log"));
    });

    it("returns to the list from the page of a rule that does not exist", async () => {
      window.history.replaceState(null, "", "/?view=log");
      window.history.pushState(null, "", "/?view=retention&id=99");
      render(<RetentionPage />);
      await waitFor(() => expect(window.location.search).toBe("?view=retention"));
      expect(h1(t.retentionTitle)).toBeTruthy();
      expect(document.activeElement).toBe(document.body);
      act(() => window.history.back());
      await waitFor(() => expect(window.location.search).toBe("?view=log"));
    });

    it("adds a rule on its own page, below the legal period only after confirming with force, and returns to the tab", async () => {
      await show("", what5);
      fireEvent.click(screen.getByRole("button", { name: t.ruleAdd }));
      expect(window.location.search).toBe("?view=retention&add=rule");
      expect(document.activeElement).toBe(h1(t.ruleAdd));
      const crumbs = screen.getByRole("navigation", { name: t.carbon.breadcrumb });
      expect(within(crumbs).getByRole("link", { name: t.retentionTitle }).getAttribute("href")).toBe("?view=retention");
      fireEvent.change(screen.getByLabelText(t.ruleAccount), { target: { value: "1" } });
      fireEvent.change(screen.getByLabelText(t.ruleFolder), { target: { value: "INBOX" } });
      fireEvent.change(screen.getByLabelText(t.ruleYears), { target: { value: "5" } });
      expect(screen.getByText(t.ruleShortWarn(6))).toBeTruthy();
      fireEvent.click(screen.getByRole("button", { name: t.ruleAdd }));
      expect(within(dialog()).getByText(t.ruleShort(5, 6))).toBeTruthy();
      expect(changes()).toEqual([]);
      fireEvent.click(within(dialog()).getByRole("button", { name: t.cancel }));
      expect(changes()).toEqual([]);
      fireEvent.click(screen.getByRole("button", { name: t.ruleAdd }));
      fireEvent.click(within(dialog()).getByRole("button", { name: t.ruleAddAnyway }));
      await waitFor(() =>
        expect(changes()).toEqual([
          { method: "POST", url: "/api/retention/rules", body: { accountId: 1, folder: "INBOX", years: 5, timeZone: "Europe/Berlin", note: "", force: true } },
        ]),
      );
      await waitFor(() => expect(window.location.search).toBe("?view=retention"));
      expect(document.activeElement).toBe(h1(t.retentionTitle));
    });

    it("adds a rule of the legal period without asking; Cancel returns to the tab", async () => {
      await show("&add=rule", t.ruleAdd);
      const add = screen.getByRole("button", { name: t.ruleAdd }) as HTMLButtonElement;
      expect(add.disabled).toBe(true);
      fireEvent.change(screen.getByLabelText(t.ruleAccount), { target: { value: "1" } });
      fireEvent.change(screen.getByLabelText(t.ruleNote), { target: { value: "§147 AO" } });
      expect(screen.queryByText(t.ruleShortWarn(6))).toBeNull();
      fireEvent.click(add);
      expect(screen.queryByRole("dialog")).toBeNull();
      await waitFor(() =>
        expect(changes()).toEqual([
          { method: "POST", url: "/api/retention/rules", body: { accountId: 1, folder: "", years: 10, timeZone: "Europe/Berlin", note: "§147 AO", force: false } },
        ]),
      );
      await waitFor(() => expect(window.location.search).toBe("?view=retention"));
      cleanup();
      await show("&add=rule", t.ruleAdd);
      fireEvent.click(screen.getByRole("button", { name: t.cancel }));
      expect(window.location.search).toBe("?view=retention");
      expect(changes()).toHaveLength(1);
    });
  });

  describe("legal holds", () => {
    it("has a row per active hold with its scope and state, and Release in its menu", async () => {
      await show("&tab=holds", "Case 4711");
      const link = within(row("Case 4711")).getByRole("link", { name: "Case 4711" });
      expect(link.closest("th")!.getAttribute("scope")).toBe("row");
      expect(link.getAttribute("href")).toBe("?view=retention&tab=holds&id=8");
      expect(within(row("Case 4711")).getByText(s.holdActive)).toBeTruthy();
      expect(within(row("Case 4711")).getAllByText(t.holdScopeUser("alice"))).toHaveLength(2);
      expect(screen.queryByText("Old case")).toBeNull();
      expect(screen.getByRole("button", { name: t.holdAdd }).className).toContain("cds--btn--primary");
      fireEvent.click(menuButton("Case 4711"));
      expect(screen.getAllByRole("menuitem").map((i) => i.textContent)).toEqual([t.holdRelease]);
    });

    it("shows released holds with the toggle, without a menu", async () => {
      await show("&tab=holds", "Case 4711");
      fireEvent.click(screen.getByRole("switch", { name: t.holdShowReleased }));
      await screen.findByText("Old case");
      expect(requests.some((r) => r.url === "/api/legal-holds?released=1")).toBe(true);
      expect(within(row("Old case")).getByText(t.holdReleasedTag)).toBeTruthy();
      expect(within(row("Old case")).queryByRole("button")).toBeNull();
    });

    it("keeps the focus on the toggle when the released holds go", async () => {
      await show("&tab=holds", "Case 4711");
      const toggle = screen.getByRole("switch", { name: t.holdShowReleased });
      fireEvent.click(toggle);
      await screen.findByText("Old case");
      toggle.focus();
      fireEvent.click(toggle);
      // The rows go without the focus: it stays where it is.
      await waitFor(() => expect(screen.queryByText("Old case")).toBeNull());
      expect(document.activeElement).toBe(toggle);
    });

    it("releases a hold from its row only with a note, after confirming, and puts the focus on the Holds tab", async () => {
      await show("&tab=holds", "Case 4711");
      pick("Case 4711", t.holdRelease);
      const release = within(dialog()).getByRole("button", { name: t.holdRelease }) as HTMLButtonElement;
      expect(release.disabled).toBe(true);
      const note = within(dialog()).getByLabelText(t.holdReleaseNote);
      fireEvent.change(note, { target: { value: "   " } });
      expect(release.disabled).toBe(true);
      fireEvent.click(release);
      expect(changes()).toEqual([]);
      fireEvent.click(within(dialog()).getByRole("button", { name: t.cancel }));
      expect(changes()).toEqual([]);
      expect(document.activeElement).toBe(menuButton("Case 4711"));

      pick("Case 4711", t.holdRelease);
      fireEvent.change(within(dialog()).getByLabelText(t.holdReleaseNote), { target: { value: "  audit closed " } });
      fireEvent.click(within(dialog()).getByRole("button", { name: t.holdRelease }));
      await waitFor(() => expect(changes()).toEqual([{ method: "POST", url: "/api/legal-holds/8/release", body: { note: "audit closed" } }]));
      // Released holds are hidden: the row goes.
      await waitFor(() => expect(screen.queryByRole("link", { name: "Case 4711" })).toBeNull());
      expect(document.activeElement).toBe(tab(t.retentionHoldsTab));
    });

    it("puts the focus on the hold's name when its row stays after Release", async () => {
      await show("&tab=holds", "Case 4711");
      fireEvent.click(screen.getByRole("switch", { name: t.holdShowReleased }));
      await screen.findByText("Old case");
      pick("Case 4711", t.holdRelease);
      fireEvent.change(within(dialog()).getByLabelText(t.holdReleaseNote), { target: { value: "done" } });
      fireEvent.click(within(dialog()).getByRole("button", { name: t.holdRelease }));
      // The row stays, released, and its menu went.
      await waitFor(() => expect(within(row("Case 4711")).getByText(t.holdReleasedTag)).toBeTruthy());
      expect(within(row("Case 4711")).queryByRole("button")).toBeNull();
      expect(document.activeElement).toBe(screen.getByRole("link", { name: "Case 4711" }));
    });

    it("shows a hold's page with its scope, reason and release, and releases it there", async () => {
      await show("&tab=holds&id=8", "Case 4711");
      expect(document.activeElement).toBe(document.body);
      const header = h1("Case 4711").closest("header")!;
      expect(within(header).getByText(s.holdActive)).toBeTruthy();
      const labels = () => [...document.querySelectorAll(".app-summary__tile dt")].map((el) => el.textContent);
      expect(labels()).toEqual([s.colCovers, s.created]);
      expect(within(screen.getByRole("region", { name: t.holdReason })).getByText("lawsuit 12 O 345/26")).toBeTruthy();
      const crumbs = screen.getByRole("navigation", { name: t.carbon.breadcrumb });
      expect(within(crumbs).getByRole("link", { name: t.retentionTitle }).getAttribute("href")).toBe("?view=retention&tab=holds");

      pick("Case 4711", t.holdRelease);
      fireEvent.change(within(dialog()).getByLabelText(t.holdReleaseNote), { target: { value: "audit closed" } });
      fireEvent.click(within(dialog()).getByRole("button", { name: t.holdRelease }));
      await waitFor(() => expect(within(h1("Case 4711").closest("header")!).getByText(t.holdReleasedTag)).toBeTruthy());
      // The page stays, the hold is released, and the focus is on the h1.
      expect(window.location.search).toBe("?view=retention&tab=holds&id=8");
      expect(labels()).toEqual([s.colCovers, s.created, s.released]);
      expect(screen.getByText(t.holdReleased("demo", day("2026-10-25T00:00:00Z"), "audit closed"))).toBeTruthy();
      expect(screen.queryByRole("button", { name: t.userActions("Case 4711") })).toBeNull();
      expect(document.activeElement).toBe(h1("Case 4711"));
    });

    it("opens a released hold's page and returns to the list for an unknown one", async () => {
      await show("&tab=holds&id=9", "Old case");
      expect(requests.some((r) => r.url === "/api/legal-holds?released=1")).toBe(true);
      expect(screen.queryByRole("button", { name: t.userActions("Old case") })).toBeNull();
      cleanup();
      window.history.replaceState(null, "", "/?view=retention&tab=holds&id=99");
      render(<RetentionPage />);
      await waitFor(() => expect(window.location.search).toBe("?view=retention&tab=holds"));
      expect(await screen.findByText("Case 4711")).toBeTruthy();
    });

    it("adds a hold on its own page, the whole archive only with the box ticked, and says so on the Holds tab", async () => {
      await show("&tab=holds", "Case 4711");
      fireEvent.click(screen.getByRole("button", { name: t.holdAdd }));
      expect(window.location.search).toBe("?view=retention&tab=holds&add=hold");
      expect(document.activeElement).toBe(h1(t.holdAdd));
      await screen.findByRole("option", { name: "alice" });
      fireEvent.change(screen.getByLabelText(t.holdName), { target: { value: "Audit" } });
      fireEvent.change(screen.getByLabelText(t.holdReason), { target: { value: "Tax audit 2026" } });
      const add = screen.getByRole("button", { name: t.holdAdd }) as HTMLButtonElement;
      expect(add.disabled).toBe(true);
      fireEvent.click(screen.getByLabelText(t.holdAll));
      expect(add.disabled).toBe(false);
      fireEvent.click(add);
      await waitFor(() =>
        expect(changes()).toEqual([
          { method: "POST", url: "/api/legal-holds", body: { name: "Audit", reason: "Tax audit 2026", user: "", accountId: null, filter: null, since: null, until: null, all: true } },
        ]),
      );
      // Back on the tab, which shows the notice until the tab changes.
      await waitFor(() => expect(window.location.search).toBe("?view=retention&tab=holds"));
      expect(await screen.findByText(t.holdAdded(1))).toBeTruthy();
      expect(document.activeElement).toBe(h1(t.retentionTitle));
      fireEvent.click(tab(t.runsTab));
      fireEvent.click(tab(t.retentionHoldsTab));
      await screen.findByText("Case 4711");
      expect(screen.queryByText(t.holdAdded(1))).toBeNull();

      // A filter scopes the hold: the box is gone and all is false.
      fireEvent.click(screen.getByRole("button", { name: t.holdAdd }));
      fireEvent.change(screen.getByLabelText(t.holdName), { target: { value: "Court" } });
      fireEvent.change(screen.getByLabelText(t.holdReason), { target: { value: "Case" } });
      fireEvent.change(screen.getByLabelText(t.holdFilter), { target: { value: "from:registry@court.example" } });
      expect(screen.queryByLabelText(t.holdAll)).toBeNull();
      fireEvent.click(screen.getByRole("button", { name: t.holdAdd }));
      await waitFor(() => expect(changes()).toHaveLength(2));
      expect(changes()[1]!.body).toMatchObject({ name: "Court", filter: { from: "registry@court.example" }, all: false });
    });

    it("drops the notice when another hold is being added", async () => {
      await show("&tab=holds&add=hold", t.holdAdd);
      fireEvent.change(screen.getByLabelText(t.holdName), { target: { value: "Audit" } });
      fireEvent.change(screen.getByLabelText(t.holdReason), { target: { value: "Tax audit 2026" } });
      fireEvent.click(screen.getByLabelText(t.holdAll));
      fireEvent.click(screen.getByRole("button", { name: t.holdAdd }));
      expect(await screen.findByText(t.holdAdded(1))).toBeTruthy();
      fireEvent.click(screen.getByRole("button", { name: t.holdAdd }));
      fireEvent.click(screen.getByRole("button", { name: t.cancel }));
      await screen.findByText("Case 4711");
      expect(screen.queryByText(t.holdAdded(1))).toBeNull();
    });
  });

  describe("deletion runs", () => {
    it("has a row per run with the run as its row header and a link to its page, and no button", async () => {
      await show("&tab=runs", t.runLine(3, 2, "2.0 KB"));
      const headers = screen.getAllByRole("rowheader");
      expect(headers.map((h) => within(h).getByRole("link").textContent)).toEqual([t.runLine(2, 4, "2.0 KB"), t.runLine(3, 2, "2.0 KB")]);
      expect(headers.every((h) => h.getAttribute("scope") === "row")).toBe(true);
      expect(within(headers[1]!).getByRole("link").getAttribute("href")).toBe("?view=retention&tab=runs&id=3");
      expect(screen.getByRole("table", { name: t.runsTab }).querySelectorAll("button")).toHaveLength(0);
      expect(within(row(t.runLine(2, 4, "2.0 KB"))).getAllByText(t.runResult(3, 1))).toHaveLength(2);
      // An open run: no new one until it is finished.
      expect(screen.queryByRole("button", { name: t.runPropose })).toBeNull();
      fireEvent.click(within(headers[1]!).getByRole("link"));
      expect(window.location.search).toBe("?view=retention&tab=runs&id=3");
      await waitFor(() => expect(document.activeElement).toBe(h1(t.runLine(3, 2, "2.0 KB"))));
    });

    it("shows a run's page from the URL without moving the focus: the counts, groups and hashes", async () => {
      await showRun3();
      expect(document.activeElement).toBe(document.body);
      expect(runTitle("proposed")).toBeTruthy();
      const tiles = Object.fromEntries([...document.querySelectorAll(".app-summary__tile")].map((el) => [el.querySelector("dt")!.textContent, el.querySelector("dd")!.textContent]));
      expect(Object.keys(tiles)).toEqual([s.candidates, s.size, s.deleted, s.kept, t.runColProposed]);
      expect(tiles[s.candidates]).toBe("2");
      expect(tiles[s.size]).toBe("2.0 KB");
      const groups = screen.getByRole("region", { name: s.groups });
      expect(within(groups).getByRole("cell", { name: "demo / personal / Archive/2014" })).toBeTruthy();
      expect(screen.getByRole("button", { name: t.runHashes(2, 2) })).toBeTruthy();
      expect(screen.getByRole("button", { name: s.deleteCount(2) }).className).toContain("cds--btn--danger");
      expect(screen.queryByRole("dialog")).toBeNull();
    });

    it("wraps a group's path at its slashes, not anywhere", async () => {
      await showRun3();
      const cell = within(screen.getByRole("region", { name: s.groups })).getByRole("cell", { name: "demo / personal / Archive/2014" });
      expect(cell.className).not.toContain("app-break-all");
      expect(cell.innerHTML).toBe("demo /<wbr> personal /<wbr> Archive/<wbr>2014");
    });

    it("enables Delete only for the exact typed count and confirms with it", async () => {
      const field = await openDelete();
      expect(document.activeElement).toBe(field);
      expect(dialog().closest(".cds--modal")!.className).toContain("cds--modal--danger");
      const remove = within(dialog()).getByRole("button", { name: t.runConfirm }) as HTMLButtonElement;
      expect(remove.disabled).toBe(true);
      for (const wrong of ["", "1", "3", "02", "2.0", "1e1"]) {
        fireEvent.change(field, { target: { value: wrong } });
        expect(remove.disabled, wrong).toBe(true);
        fireEvent.submit(field.closest("form")!);
        fireEvent.click(remove);
      }
      expect(changes()).toEqual([]);
      fireEvent.change(field, { target: { value: "2" } });
      expect(remove.disabled).toBe(false);
      fireEvent.click(remove);
      await waitFor(() => expect(changes()).toEqual([{ method: "POST", url: "/api/retention/runs/3/confirm", body: { count: 2 } }]));
      // The server's 202 without a body is not an error: the run deletes now.
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
      expect(document.activeElement).toBe(h1(t.runLine(3, 2, "2.0 KB")));
      expect(await screen.findByText(t.runState("confirmed"))).toBeTruthy();
      expect(screen.queryByRole("alert")).toBeNull();
      expect(screen.queryByRole("button", { name: s.deleteCount(2) })).toBeNull();
    });

    it("names Delete as the running action while it is sent", async () => {
      let answered: () => void = () => {};
      answer = (_, u) => {
        if (!u.endsWith("/confirm")) return undefined;
        return new Promise<Response>((done) => {
          answered = () => {
            runs = runs.map((r) => (r.id === 3 ? { ...r, state: "confirmed" } : r));
            done(new Response(null, { status: 202 }));
          };
        });
      };
      const field = await openDelete();
      fireEvent.change(field, { target: { value: "2" } });
      fireEvent.click(within(dialog()).getByRole("button", { name: t.runConfirm }));
      const busy = await within(dialog()).findByText(t.runConfirm, { selector: ".cds--inline-loading__text" });
      expect(busy).toBeTruthy();
      expect(within(dialog()).queryByText(t.loading, { selector: ".cds--inline-loading__text" })).toBeNull();
      answered();
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    });

    it("keeps the dialog open with the error when the confirmation fails", async () => {
      confirmFails = true;
      const field = await openDelete();
      fireEvent.change(field, { target: { value: "2" } });
      fireEvent.click(within(dialog()).getByRole("button", { name: t.runConfirm }));
      expect(await within(dialog()).findByText(t.failed("type the number of messages (2) to confirm"))).toBeTruthy();
      expect(changes()).toHaveLength(1);
    });

    it("closing the dialog changes nothing and gives the focus back to Delete", async () => {
      await openDelete();
      fireEvent.click(within(dialog()).getByRole("button", { name: t.cancel }));
      expect(screen.queryByRole("dialog")).toBeNull();
      expect(changes()).toEqual([]);
      expect(document.activeElement).toBe(screen.getByRole("button", { name: s.deleteCount(2) }));
    });

    it("stays open and says so when the run was cancelled elsewhere before Delete", async () => {
      answer = (_, u) => {
        if (!u.endsWith("/confirm")) return undefined;
        runs = runs.map((r) => (r.id === 3 ? { ...r, state: "cancelled" } : r));
        return json({ error: "the run is no longer proposed" }, 409);
      };
      const field = await openDelete();
      fireEvent.change(field, { target: { value: "2" } });
      fireEvent.click(within(dialog()).getByRole("button", { name: t.runConfirm }));
      const text = `${t.failed("the run is no longer proposed")} ${t.runNowState(t.runState("cancelled"))}`;
      expect(await within(dialog()).findByText(text)).toBeTruthy();
      // The page shows the run as it is now; the dialog stays, with nothing to confirm.
      await waitFor(() => expect(runTitle("cancelled")).toBeTruthy());
      expect(screen.getByRole("dialog")).toBeTruthy();
      expect(within(dialog()).queryByRole("textbox")).toBeNull();
      expect((within(dialog()).getByRole("button", { name: t.runConfirm }) as HTMLButtonElement).disabled).toBe(true);
    });

    it("disables Delete and Cancel run and says so when the state cannot be loaded after a confirm", async () => {
      answer = (_, u) => {
        if (!u.endsWith("/confirm")) return undefined;
        runsFail = true;
        return new Response(null, { status: 202 });
      };
      const field = await openDelete();
      fireEvent.change(field, { target: { value: "2" } });
      const remove = within(dialog()).getByRole("button", { name: t.runConfirm }) as HTMLButtonElement;
      fireEvent.click(remove);
      expect(await within(dialog()).findByText(s.runStateUnknown)).toBeTruthy();
      expect(screen.queryByText(/JSON/)).toBeNull();
      expect(remove.disabled).toBe(true);
      fireEvent.submit(field.closest("form")!);
      fireEvent.click(within(dialog()).getByRole("button", { name: t.cancel }));
      // On the page: the error, and nothing more to send.
      expect(screen.getByRole("alert").textContent).toBe(s.runStateUnknown);
      expect((screen.getByRole("button", { name: s.deleteCount(2) }) as HTMLButtonElement).disabled).toBe(true);
      fireEvent.click(menuButton(t.runLine(3, 2, "2.0 KB")));
      const cancelRun = screen.getByRole("menuitem", { name: t.runCancel });
      expect(cancelRun.getAttribute("aria-disabled")).toBe("true");
      fireEvent.click(cancelRun);
      expect(changes()).toEqual([{ method: "POST", url: "/api/retention/runs/3/confirm", body: { count: 2 } }]);
    });

    it("cancels a run from the menu without asking; the tag says so and the focus is on the h1", async () => {
      await showRun3();
      pick(t.runLine(3, 2, "2.0 KB"), t.runCancel);
      expect(screen.queryByRole("dialog")).toBeNull();
      await waitFor(() => expect(changes()).toEqual([{ method: "POST", url: "/api/retention/runs/3/cancel", body: undefined }]));
      await waitFor(() => expect(runTitle("cancelled")).toBeTruthy());
      expect(document.activeElement).toBe(h1(t.runLine(3, 2, "2.0 KB")));
      expect(screen.queryByRole("alert")).toBeNull();
      expect(screen.queryByRole("button", { name: s.deleteCount(2) })).toBeNull();
      expect(screen.queryByRole("button", { name: t.userActions(t.runLine(3, 2, "2.0 KB")) })).toBeNull();
    });

    it("names Cancel run as the running action while it is sent", async () => {
      let answered: () => void = () => {};
      answer = (_, u) => (u.endsWith("/cancel") ? new Promise<Response>((done) => (answered = () => done(new Response(null, { status: 204 })))) : undefined);
      await showRun3();
      pick(t.runLine(3, 2, "2.0 KB"), t.runCancel);
      expect(await screen.findByText(t.runCancel, { selector: ".cds--inline-loading__text" })).toBeTruthy();
      expect(screen.queryByText(t.loading, { selector: ".cds--inline-loading__text" })).toBeNull();
      answered();
      await waitFor(() => expect(screen.queryByText(t.runCancel, { selector: ".cds--inline-loading__text" })).toBeNull());
    });

    it("shows a failed Cancel run as an alert under the title and puts the focus on the h1", async () => {
      answer = (_, u) => (u.endsWith("/cancel") ? json({ error: "database unavailable" }, 503) : undefined);
      await showRun3();
      pick(t.runLine(3, 2, "2.0 KB"), t.runCancel);
      const alert = await screen.findByRole("alert");
      expect(alert.textContent).toBe(t.failed("database unavailable"));
      expect(document.activeElement).toBe(h1(t.runLine(3, 2, "2.0 KB")));
      expect(runTitle("proposed")).toBeTruthy();
    });

    it("says so when another admin confirmed the run this one cancels", async () => {
      // The race: the run was confirmed elsewhere; Cancel run gets a 409.
      answer = (_, u) => {
        if (!u.endsWith("/cancel")) return undefined;
        runs = runs.map((r) => (r.id === 3 ? { ...r, state: "confirmed" } : r));
        return json({ error: "the run is no longer proposed" }, 409);
      };
      await showRun3();
      pick(t.runLine(3, 2, "2.0 KB"), t.runCancel);
      const text = `${t.failed("the run is no longer proposed")} ${t.runNowState(t.runState("confirmed"))}`;
      expect(await screen.findByText(text)).toBeTruthy();
      // The page shows the run as it is now: deleting, nothing to confirm.
      await waitFor(() => expect(runTitle("confirmed")).toBeTruthy());
      // The reload of the review keeps the error.
      expect(screen.getByText(text)).toBeTruthy();
      expect(screen.queryByRole("button", { name: s.deleteCount(2) })).toBeNull();
      expect(changes()).toEqual([{ method: "POST", url: "/api/retention/runs/3/cancel", body: undefined }]);
    });

    const props = {
      id: 3,
      heading: { current: null },
      parent: { label: t.retentionTitle, href: "?view=retention&tab=runs", go: () => {} },
      changed: async () => runs,
    };

    it("lets the changed-state line follow the polled state", async () => {
      answer = (_, u) => {
        if (!u.endsWith("/cancel")) return undefined;
        runs = runs.map((r) => (r.id === 3 ? { ...r, state: "confirmed" } : r));
        return json({ error: "the run is no longer proposed" }, 409);
      };
      const view = render(<RunPage {...props} state="proposed" />);
      await screen.findByRole("button", { name: s.deleteCount(2) });
      pick(t.runLine(3, 2, "2.0 KB"), t.runCancel);
      const failed = t.failed("the run is no longer proposed");
      expect(await screen.findByText(`${failed} ${t.runNowState(t.runState("confirmed"))}`)).toBeTruthy();
      // As the page does: the list's state reaches the run's page, later done.
      view.rerender(<RunPage {...props} state="confirmed" />);
      runs = runs.map((r) => (r.id === 3 ? { ...r, state: "done" } : r));
      view.rerender(<RunPage {...props} state="done" />);
      await waitFor(() => expect(runTitle("done")).toBeTruthy());
      expect(screen.getByText(`${failed} ${t.runNowState(t.runState("done"))}`)).toBeTruthy();
      expect(screen.queryByText(t.runNowState(t.runState("confirmed")), { exact: false })).toBeNull();
    });

    it("loads the run again when its state changes while the page is open", async () => {
      const reviews = () => requests.filter((r) => r.url === "/api/retention/runs/3").length;
      runs = [run({ state: "confirmed" })];
      const view = render(<RunPage {...props} state="confirmed" />);
      await waitFor(() => expect(runTitle("confirmed")).toBeTruthy());
      expect(reviews()).toBe(1);
      runs = [run({ state: "done", deleted: 2 })];
      review = { ...review, candidates: review.candidates.map((c) => ({ ...c, state: "deleted" as const })) };
      view.rerender(<RunPage {...props} state="done" />);
      await waitFor(() => expect(runTitle("done")).toBeTruthy());
      expect(screen.getByText(`${"c".repeat(64)} (deleted)`)).toBeTruthy();
      expect(reviews()).toBe(2);
    });

    it("polls the runs while one is deleting", async () => {
      vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
      try {
        runs = [run({ state: "confirmed" })];
        review = { ...review, run: runs[0]! };
        await show("&tab=runs&id=3", t.runLine(3, 2, "2.0 KB"));
        const listed = () => requests.filter((r) => r.url === "/api/retention/runs").length;
        expect(listed()).toBe(1);
        runs = [run({ state: "done", deleted: 2 })];
        await act(() => vi.advanceTimersByTimeAsync(3000));
        await waitFor(() => expect(runTitle("done")).toBeTruthy());
        expect(listed()).toBe(2);
      } finally {
        vi.useRealTimers();
      }
    });

    it("proposes a run and opens its page; a finished run offers nothing to confirm", async () => {
      runs = [run({ id: 2, state: "done", candidates: 4, deleted: 3, kept: 1 })];
      review = { ...review, run: runs[0]! };
      await show("&tab=runs&id=2", t.runLine(2, 4, "2.0 KB"));
      await screen.findByRole("region", { name: s.groups });
      expect(screen.queryByRole("button", { name: s.deleteCount(4) })).toBeNull();
      expect(screen.queryByRole("button", { name: t.userActions(t.runLine(2, 4, "2.0 KB")) })).toBeNull();
      expect(changes()).toEqual([]);
      cleanup();

      await show("&tab=runs", t.runLine(2, 4, "2.0 KB"));
      review = { ...review, run: run({ id: 4 }) };
      const length = window.history.length;
      fireEvent.click(screen.getByRole("button", { name: t.runPropose }));
      await waitFor(() => expect(changes()).toEqual([{ method: "POST", url: "/api/retention/runs", body: undefined }]));
      await waitFor(() => expect(window.location.search).toBe("?view=retention&tab=runs&id=4"));
      expect(window.history.length).toBe(length + 1);
      expect(await screen.findByRole("button", { name: s.deleteCount(2) })).toBeTruthy();
      expect(requests.some((r) => r.url === "/api/retention/runs/4")).toBe(true);
      expect(document.activeElement).toBe(h1(t.runLine(4, 2, "2.0 KB")));
    });

    it("offers only Cancel run when nothing can be deleted", async () => {
      review = { ...review, run: run({ candidates: 0 }), groups: [], candidates: [], listed: 0 };
      window.history.replaceState(null, "", "/?view=retention&tab=runs&id=3");
      render(<RetentionPage />);
      await screen.findByText(t.runEmpty);
      expect(screen.queryByRole("button", { name: s.deleteCount(0) })).toBeNull();
      pick(t.runLine(3, 0, "2.0 KB"), t.runCancel);
      await waitFor(() => expect(changes()).toEqual([{ method: "POST", url: "/api/retention/runs/3/cancel", body: undefined }]));
    });

    it("returns to the runs from the page of a run that does not exist", async () => {
      window.history.replaceState(null, "", "/?view=retention&tab=runs&id=99");
      render(<RetentionPage />);
      await waitFor(() => expect(window.location.search).toBe("?view=retention&tab=runs"));
      expect(await screen.findByText(t.runLine(3, 2, "2.0 KB"))).toBeTruthy();
    });
  });
});

describe("German", () => {
  const english = [/\bClose\b/, /\bLoading\b/, /Focus sentinel/, /\bdanger\b/, /Increment/, /Decrement/, /Scroll left/, /Scroll right/,
    /\bOff\b/, /\bOn\b/, /Number is not valid/, /increment\.number/, /decrement\.number/, /\bActions\b/, /\bmessages\b/];

  function readable(): string {
    const attrs = ["aria-label", "title", "placeholder", "alt", "aria-valuetext", "aria-description"];
    const values = [...document.body.querySelectorAll("*")].flatMap((el) => attrs.map((a) => el.getAttribute(a) ?? ""));
    return [document.body.textContent ?? "", ...values].join("\n");
  }

  function noEnglish() {
    const text = readable();
    for (const e of english) expect(text, String(e)).not.toMatch(e);
  }

  it("shows the tabs, tables, pages and Modals in German", async () => {
    applySettings({ language: "de", locale: "de" });
    try {
      const de = messagesFor("de");
      const sd = settingsTextFor("de");
      const what = "demo / personal / Archive/2014";
      render(<RetentionPage />);
      await screen.findByText(what);
      expect(screen.getByRole("tab", { name: de.retentionRulesTab })).toBeTruthy();
      expect(screen.getByText(sd.colRule)).toBeTruthy();
      noEnglish();
      fireEvent.click(screen.getByRole("button", { name: de.userActions(what) }));
      fireEvent.click(screen.getByRole("menuitem", { name: de.ruleRemove }));
      expect(screen.getByRole("button", { name: de.carbon.closeModal })).toBeTruthy();
      noEnglish();
      fireEvent.click(within(dialog()).getByRole("button", { name: de.cancel }));

      fireEvent.click(screen.getByRole("button", { name: de.ruleAdd }));
      expect(screen.getByRole("button", { name: de.carbon.ids["increment.number"] })).toBeTruthy();
      fireEvent.change(screen.getByLabelText(de.ruleYears), { target: { value: "5" } });
      expect(screen.getByText(de.ruleShortWarn(6))).toBeTruthy();
      noEnglish();
      fireEvent.click(screen.getByRole("button", { name: de.cancel }));
      fireEvent.click(screen.getByRole("link", { name: what }));
      await screen.findByText(sd.colPeriod);
      noEnglish();

      act(() => window.history.replaceState(null, "", "/?view=retention&tab=holds"));
      cleanup();
      render(<RetentionPage />);
      await screen.findByText("Case 4711");
      noEnglish();
      fireEvent.click(screen.getByRole("button", { name: de.userActions("Case 4711") }));
      fireEvent.click(screen.getByRole("menuitem", { name: de.holdRelease }));
      noEnglish();
      fireEvent.click(within(dialog()).getByRole("button", { name: de.cancel }));
      fireEvent.click(screen.getByRole("link", { name: "Case 4711" }));
      await screen.findByText(sd.colCovers);
      noEnglish();
      fireEvent.click(screen.getByRole("link", { name: de.retentionTitle }));
      fireEvent.click(screen.getByRole("button", { name: de.holdAdd }));
      await screen.findByLabelText(de.holdFilter);
      noEnglish();

      cleanup();
      window.history.replaceState(null, "", "/?view=retention&tab=runs");
      render(<RetentionPage />);
      await screen.findByText(de.runLine(3, 2, "2,0 KB"));
      noEnglish();
      fireEvent.click(screen.getByRole("link", { name: de.runLine(3, 2, "2,0 KB") }));
      fireEvent.click(await screen.findByRole("button", { name: sd.deleteCount(2) }));
      await within(dialog()).findByLabelText(de.runConfirmPrompt(2));
      noEnglish();
    } finally {
      applySettings({ language: "en", locale: "en" });
    }
  });
});
