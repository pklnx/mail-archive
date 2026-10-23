import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { DeletionReview, DeletionRun, LegalHold, RetentionRules } from "./api";
import { retentionDay } from "./format";
import { applySettings, messagesFor, t } from "./i18n";
import { accountLabel, countMatches, holdScope, RetentionPage, RunReview, runOpen } from "./RetentionPage";

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

// The page itself (#168 part 5): Carbon Tabs, and the danger confirmations
// that send their request only after confirming, and the same one as before.
const rules: RetentionRules = {
  rules: [
    {
      id: 5, accountId: 1, account: "personal", accountKind: "imap", accountRemoved: false, owner: "demo", folder: "Archive/2014", years: 10,
      timeZone: "Europe/Berlin", note: "GoBD", createdAt: "2026-10-01T00:00:00Z", createdBy: "demo",
    },
  ],
  accounts: [
    { id: 1, name: "personal", kind: "imap", removed: false, owner: "demo", folders: ["INBOX", "Archive/2014"] },
    { id: 2, name: "old", kind: "import", removed: true, owner: "", folders: [] },
  ],
  minYears: 6,
  defaultTimeZone: "Europe/Berlin",
};

const run = (r: Partial<DeletionRun>): DeletionRun => ({
  id: 3, state: "proposed", createdAt: "2026-10-20T00:00:00Z", createdBy: "demo", candidates: 2, bytes: 2048, confirmedAt: null, confirmedBy: "",
  finishedAt: null, deleted: 0, kept: 0, ...r,
});

let runs: DeletionRun[];
let review: DeletionReview;
let requests: { method: string; url: string; body: unknown }[];
let confirmFails: boolean;
/** Loading the runs fails (after an action, for example). */
let runsFail: boolean;
/** A test's own answer to Delete or Cancel run, if it has one. */
let answer: ((url: string) => Response | undefined) | null;
const changes = () => requests.filter((r) => r.method !== "GET");

beforeEach(() => {
  requests = [];
  confirmFails = false;
  runsFail = false;
  answer = null;
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
    requests.push({ method, url: u, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
    if (method === "GET" && u === "/api/retention/rules") return json(rules);
    if (method === "GET" && u.startsWith("/api/legal-holds")) {
      return json({ holds: [hold({ id: 8, name: "Case 4711", user: "alice" })].concat(u.includes("released=1") ? [hold({ id: 9, name: "Old case", releasedAt: "2026-10-02T00:00:00Z", releasedBy: "demo", releaseNote: "closed" })] : []) });
    }
    if (method === "GET" && u === "/api/users") return json({ users: [{ name: "alice" }] });
    if (method === "GET" && u === "/api/retention/runs") return runsFail ? json({ error: "database unavailable" }, 503) : json({ runs });
    if (method === "GET" && u.startsWith("/api/retention/runs/")) {
      // The review has the run's current state, as the server's does.
      const now = runs.find((r) => r.id === review.run.id);
      return json({ ...review, run: { ...review.run, state: now?.state ?? review.run.state } });
    }
    if (method === "POST" && (u.endsWith("/confirm") || u.endsWith("/cancel")) && answer) {
      const res = answer(u);
      if (res) return res;
    }
    if (method === "POST" && u === "/api/retention/runs") return json(run({ id: 4 }), 201);
    if (method === "POST" && u === "/api/retention/rules") return json({ id: 6 }, 201);
    if (method === "POST" && u === "/api/legal-holds") return json({ id: 10, matched: 1 }, 201);
    if (method === "POST" && u.endsWith("/confirm")) {
      if (confirmFails) return json({ error: "type the number of messages (2) to confirm" }, 400);
      // As the server does: 202 without a body, and the run is deleting.
      runs = runs.map((r) => (r.state === "proposed" ? { ...r, state: "confirmed" } : r));
      return new Response(null, { status: 202 });
    }
    return new Response(null, { status: 204 });
  });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const dialog = () => screen.getByRole("dialog");

async function showTab(name: string) {
  render(<RetentionPage />);
  await screen.findByText("demo / personal / Archive/2014");
  if (name !== t.retentionRulesTab) fireEvent.click(screen.getByRole("tab", { name }));
}

describe("RetentionPage", () => {
  it("has the three sections as Carbon tabs and loads each when it is shown", async () => {
    await showTab(t.retentionRulesTab);
    const tabs = screen.getAllByRole("tab");
    expect(tabs.map((tab) => tab.textContent)).toEqual([t.retentionRulesTab, t.retentionHoldsTab, t.runsTab]);
    expect(tabs[0]!.getAttribute("aria-selected")).toBe("true");
    expect(tabs[0]!.className).toContain("cds--tabs__nav-item");
    expect(screen.getByText(t.retentionLegal)).toBeTruthy();
    expect(requests.some((r) => r.url.startsWith("/api/legal-holds") || r.url.startsWith("/api/retention/runs"))).toBe(false);
    fireEvent.click(screen.getByRole("tab", { name: t.retentionHoldsTab }));
    expect(screen.getByRole("tab", { name: t.retentionHoldsTab }).getAttribute("aria-selected")).toBe("true");
    await screen.findByText("Case 4711");
    expect(screen.queryByText(t.retentionLegal)).toBeNull();
  });

  it("removes a rule only after the danger confirmation", async () => {
    await showTab(t.retentionRulesTab);
    fireEvent.click(screen.getByRole("button", { name: t.ruleRemove }));
    expect(dialog().closest(".cds--modal")!.className).toContain("cds--modal--danger");
    expect(within(dialog()).getByText(t.ruleRemoveTitle("demo / personal / Archive/2014"))).toBeTruthy();
    fireEvent.click(within(dialog()).getByRole("button", { name: t.cancel }));
    expect(changes()).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: t.ruleRemove }));
    fireEvent.click(within(dialog()).getByRole("button", { name: t.ruleRemove }));
    await waitFor(() => expect(changes()).toEqual([{ method: "DELETE", url: "/api/retention/rules/5", body: undefined }]));
  });

  it("warns below the legal period and adds such a rule only after confirming, with force", async () => {
    await showTab(t.retentionRulesTab);
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
  });

  it("adds a rule of the legal period without asking", async () => {
    await showTab(t.retentionRulesTab);
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
  });

  it("releases a hold only with a note, after confirming", async () => {
    await showTab(t.retentionHoldsTab);
    await screen.findByText("Case 4711");
    fireEvent.click(screen.getByRole("button", { name: t.holdRelease }));
    const release = within(dialog()).getByRole("button", { name: t.holdRelease }) as HTMLButtonElement;
    expect(release.disabled).toBe(true);
    const note = within(dialog()).getByLabelText(t.holdReleaseNote);
    fireEvent.change(note, { target: { value: "   " } });
    expect(release.disabled).toBe(true);
    fireEvent.click(release);
    expect(changes()).toEqual([]);
    fireEvent.click(within(dialog()).getByRole("button", { name: t.cancel }));
    expect(changes()).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: t.holdRelease }));
    fireEvent.change(within(dialog()).getByLabelText(t.holdReleaseNote), { target: { value: "  audit closed " } });
    fireEvent.click(within(dialog()).getByRole("button", { name: t.holdRelease }));
    await waitFor(() => expect(changes()).toEqual([{ method: "POST", url: "/api/legal-holds/8/release", body: { note: "audit closed" } }]));
  });

  it("shows released holds with the toggle", async () => {
    await showTab(t.retentionHoldsTab);
    await screen.findByText("Case 4711");
    expect(screen.queryByText("Old case")).toBeNull();
    fireEvent.click(screen.getByRole("switch", { name: t.holdShowReleased }));
    await screen.findByText("Old case");
    expect(requests.some((r) => r.url === "/api/legal-holds?released=1")).toBe(true);
    expect(screen.getByText(t.holdReleasedTag)).toBeTruthy();
  });

  it("holds the whole archive only with the box ticked", async () => {
    await showTab(t.retentionHoldsTab);
    await screen.findByText("Case 4711");
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
    expect(await screen.findByText(t.holdAdded(1))).toBeTruthy();
    // A filter scopes the hold: the box is gone and all is false.
    fireEvent.change(screen.getByLabelText(t.holdName), { target: { value: "Court" } });
    fireEvent.change(screen.getByLabelText(t.holdReason), { target: { value: "Case" } });
    fireEvent.change(screen.getByLabelText(t.holdFilter), { target: { value: "from:registry@court.example" } });
    expect(screen.queryByLabelText(t.holdAll)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: t.holdAdd }));
    await waitFor(() => expect(changes()).toHaveLength(2));
    expect(changes()[1]!.body).toMatchObject({ name: "Court", filter: { from: "registry@court.example" }, all: false });
  });

  it("enables Delete only for the exact typed count and confirms with it", async () => {
    await showTab(t.runsTab);
    await screen.findByText(t.runLine(3, 2, "2.0 KB"));
    // An open run: no new one until it is finished.
    expect(screen.queryByRole("button", { name: t.runPropose })).toBeNull();
    fireEvent.click(screen.getAllByRole("button", { name: t.runShow })[1]!);
    const field = await within(dialog()).findByLabelText(t.runConfirmPrompt(2));
    expect(document.activeElement).toBe(field);
    expect(dialog().closest(".cds--modal")!.className).toContain("cds--modal--danger");
    expect(within(dialog()).getByRole("cell", { name: "demo / personal / Archive/2014" })).toBeTruthy();
    expect(within(dialog()).getByRole("button", { name: t.runHashes(2, 2) })).toBeTruthy();
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
    expect(await screen.findByText(t.runState("confirmed"))).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("keeps the review open with the error when the confirmation fails", async () => {
    confirmFails = true;
    await showTab(t.runsTab);
    await screen.findByText(t.runLine(3, 2, "2.0 KB"));
    fireEvent.click(screen.getAllByRole("button", { name: t.runShow })[1]!);
    const field = await within(dialog()).findByLabelText(t.runConfirmPrompt(2));
    fireEvent.change(field, { target: { value: "2" } });
    fireEvent.click(within(dialog()).getByRole("button", { name: t.runConfirm }));
    expect(await within(dialog()).findByText(t.failed("type the number of messages (2) to confirm"))).toBeTruthy();
    expect(changes()).toHaveLength(1);
  });

  async function openRun3() {
    await showTab(t.runsTab);
    await screen.findByText(t.runLine(3, 2, "2.0 KB"));
    fireEvent.click(screen.getAllByRole("button", { name: t.runShow })[1]!);
    return within(dialog()).findByLabelText(t.runConfirmPrompt(2));
  }

  it("stays open and says so when another admin confirmed the run this one cancels", async () => {
    // The race: the run was confirmed elsewhere; Cancel run gets a 409.
    answer = (u) => {
      if (!u.endsWith("/cancel")) return undefined;
      runs = runs.map((r) => (r.id === 3 ? { ...r, state: "confirmed" } : r));
      return new Response(JSON.stringify({ error: "the run is no longer proposed" }), { status: 409, headers: { "Content-Type": "application/json" } });
    };
    await openRun3();
    fireEvent.click(within(dialog()).getByRole("button", { name: t.runCancel }));
    const text = `${t.failed("the run is no longer proposed")} ${t.runNowState(t.runState("confirmed"))}`;
    expect(await within(dialog()).findByText(text)).toBeTruthy();
    // The review shows the run as it is now: deleting, nothing to confirm.
    expect(await within(dialog()).findByRole("heading", { name: `${t.runLine(3, 2, "2.0 KB")} · ${t.runState("confirmed")}` })).toBeTruthy();
    // The reload of the review keeps the error.
    expect(within(dialog()).getByText(text)).toBeTruthy();
    expect(within(dialog()).queryByRole("button", { name: t.runConfirm })).toBeNull();
    expect(changes()).toEqual([{ method: "POST", url: "/api/retention/runs/3/cancel", body: undefined }]);
  });

  it("lets the changed-state line follow the polled state", async () => {
    answer = (u) => {
      if (!u.endsWith("/cancel")) return undefined;
      runs = runs.map((r) => (r.id === 3 ? { ...r, state: "confirmed" } : r));
      return new Response(JSON.stringify({ error: "the run is no longer proposed" }), { status: 409, headers: { "Content-Type": "application/json" } });
    };
    const props = { id: 3, title: "", changed: async () => runs, close: () => {} };
    const view = render(<RunReview {...props} state="proposed" />);
    await within(dialog()).findByLabelText(t.runConfirmPrompt(2));
    fireEvent.click(within(dialog()).getByRole("button", { name: t.runCancel }));
    const failed = t.failed("the run is no longer proposed");
    expect(await within(dialog()).findByText(`${failed} ${t.runNowState(t.runState("confirmed"))}`)).toBeTruthy();
    // As the page does: the list's state reaches the review, later done.
    view.rerender(<RunReview {...props} state="confirmed" />);
    runs = runs.map((r) => (r.id === 3 ? { ...r, state: "done" } : r));
    view.rerender(<RunReview {...props} state="done" />);
    expect(await within(dialog()).findByRole("heading", { name: `${t.runLine(3, 2, "2.0 KB")} · ${t.runState("done")}` })).toBeTruthy();
    expect(within(dialog()).getByText(`${failed} ${t.runNowState(t.runState("done"))}`)).toBeTruthy();
    expect(within(dialog()).queryByText(t.runNowState(t.runState("confirmed")), { exact: false })).toBeNull();
  });

  it("stays open and says so when the run was cancelled elsewhere before Delete", async () => {
    answer = (u) => {
      if (!u.endsWith("/confirm")) return undefined;
      runs = runs.map((r) => (r.id === 3 ? { ...r, state: "cancelled" } : r));
      return new Response(JSON.stringify({ error: "the run is no longer proposed" }), { status: 409, headers: { "Content-Type": "application/json" } });
    };
    const field = await openRun3();
    fireEvent.change(field, { target: { value: "2" } });
    fireEvent.click(within(dialog()).getByRole("button", { name: t.runConfirm }));
    const text = `${t.failed("the run is no longer proposed")} ${t.runNowState(t.runState("cancelled"))}`;
    expect(await within(dialog()).findByText(text)).toBeTruthy();
    expect(screen.getByRole("dialog")).toBeTruthy();
  });

  it("disables Delete and says so when the state cannot be loaded after a confirm", async () => {
    answer = (u) => {
      if (!u.endsWith("/confirm")) return undefined;
      runsFail = true;
      return new Response(null, { status: 202 });
    };
    const field = await openRun3();
    fireEvent.change(field, { target: { value: "2" } });
    const remove = within(dialog()).getByRole("button", { name: t.runConfirm }) as HTMLButtonElement;
    fireEvent.click(remove);
    expect(await within(dialog()).findByText(t.runStateUnknown)).toBeTruthy();
    expect(screen.queryByText(/JSON/)).toBeNull();
    expect(remove.disabled).toBe(true);
    fireEvent.submit(field.closest("form")!);
    fireEvent.click(within(dialog()).getByRole("button", { name: t.runCancel }));
    expect(changes()).toEqual([{ method: "POST", url: "/api/retention/runs/3/confirm", body: { count: 2 } }]);
  });

  it("loads the review again when the run's state changes while it is open", async () => {
    const reviews = () => requests.filter((r) => r.url === "/api/retention/runs/3").length;
    runs = [run({ state: "confirmed" })];
    const props = { id: 3, title: "", changed: async () => runs, close: () => {} };
    const view = render(<RunReview {...props} state="confirmed" />);
    expect(await within(dialog()).findByRole("heading", { name: `${t.runLine(3, 2, "2.0 KB")} · ${t.runState("confirmed")}` })).toBeTruthy();
    expect(reviews()).toBe(1);
    runs = [run({ state: "done", deleted: 2 })];
    review = { ...review, candidates: review.candidates.map((c) => ({ ...c, state: "deleted" as const })) };
    view.rerender(<RunReview {...props} state="done" />);
    expect(await within(dialog()).findByRole("heading", { name: `${t.runLine(3, 2, "2.0 KB")} · ${t.runState("done")}` })).toBeTruthy();
    expect(within(dialog()).getByText(`${"c".repeat(64)} (deleted)`)).toBeTruthy();
    expect(reviews()).toBe(2);
  });

  it("cancels a run, and closing the review changes nothing", async () => {
    await showTab(t.runsTab);
    await screen.findByText(t.runLine(3, 2, "2.0 KB"));
    fireEvent.click(screen.getAllByRole("button", { name: t.runShow })[1]!);
    await within(dialog()).findByLabelText(t.runConfirmPrompt(2));
    // The footer button, beside the X in the corner.
    fireEvent.click(within(dialog()).getByText(t.close, { selector: ".cds--modal-footer button" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(changes()).toEqual([]);
    fireEvent.click(screen.getAllByRole("button", { name: t.runShow })[1]!);
    await within(dialog()).findByLabelText(t.runConfirmPrompt(2));
    fireEvent.click(within(dialog()).getByRole("button", { name: t.runCancel }));
    await waitFor(() => expect(changes()).toEqual([{ method: "POST", url: "/api/retention/runs/3/cancel", body: undefined }]));
  });

  it("proposes a run and opens its review; a finished run offers nothing to confirm", async () => {
    runs = [run({ id: 2, state: "done", candidates: 4, deleted: 3, kept: 1 })];
    await showTab(t.runsTab);
    await screen.findByText(t.runLine(2, 4, "2.0 KB"));
    expect(screen.getByText(t.runResult(3, 1))).toBeTruthy();
    review = { ...review, run: runs[0]! };
    fireEvent.click(screen.getByRole("button", { name: t.runShow }));
    await within(dialog()).findByRole("cell", { name: "demo / personal / Archive/2014" });
    expect(within(dialog()).queryByRole("textbox")).toBeNull();
    expect(within(dialog()).queryByRole("button", { name: t.runConfirm })).toBeNull();
    expect(within(dialog()).queryByRole("button", { name: t.runCancel })).toBeNull();
    fireEvent.click(within(dialog()).getByRole("button", { name: t.carbon.closeModal }));
    expect(changes()).toEqual([]);

    review = { ...review, run: run({ id: 4 }) };
    fireEvent.click(screen.getByRole("button", { name: t.runPropose }));
    await waitFor(() => expect(changes()).toEqual([{ method: "POST", url: "/api/retention/runs", body: undefined }]));
    expect(await within(dialog()).findByLabelText(t.runConfirmPrompt(2))).toBeTruthy();
    expect(requests.some((r) => r.url === "/api/retention/runs/4")).toBe(true);
  });

  it("offers Cancel run without a typed count when nothing can be deleted", async () => {
    review = { ...review, run: run({ candidates: 0 }), groups: [], candidates: [], listed: 0 };
    await showTab(t.runsTab);
    await screen.findByText(t.runLine(3, 2, "2.0 KB"));
    fireEvent.click(screen.getAllByRole("button", { name: t.runShow })[1]!);
    await within(dialog()).findByText(t.runEmpty);
    expect(within(dialog()).queryByRole("textbox")).toBeNull();
    expect(within(dialog()).queryByRole("button", { name: t.runConfirm })).toBeNull();
    fireEvent.click(within(dialog()).getByRole("button", { name: t.runCancel }));
    await waitFor(() => expect(changes()).toEqual([{ method: "POST", url: "/api/retention/runs/3/cancel", body: undefined }]));
  });
});

describe("German", () => {
  const english = [/\bClose\b/, /\bLoading\b/, /Focus sentinel/, /\bdanger\b/, /Increment/, /Decrement/, /Scroll left/, /Scroll right/,
    /\bOff\b/, /\bOn\b/, /Number is not valid/, /increment\.number/, /decrement\.number/];

  function readable(): string {
    const attrs = ["aria-label", "title", "placeholder", "alt", "aria-valuetext", "aria-description"];
    const values = [...document.body.querySelectorAll("*")].flatMap((el) => attrs.map((a) => el.getAttribute(a) ?? ""));
    return [document.body.textContent ?? "", ...values].join("\n");
  }

  function noEnglish() {
    const text = readable();
    for (const e of english) expect(text, String(e)).not.toMatch(e);
  }

  it("shows the tabs, forms and Modals in German", async () => {
    applySettings({ language: "de", locale: "de" });
    try {
      const de = messagesFor("de");
      render(<RetentionPage />);
      await screen.findByText("demo / personal / Archive/2014");
      expect(screen.getByRole("tab", { name: de.retentionRulesTab })).toBeTruthy();
      expect(screen.getByRole("button", { name: de.carbon.ids["increment.number"] })).toBeTruthy();
      fireEvent.change(screen.getByLabelText(de.ruleYears), { target: { value: "5" } });
      expect(screen.getByText(de.ruleShortWarn(6))).toBeTruthy();
      noEnglish();
      fireEvent.click(screen.getByRole("button", { name: de.ruleRemove }));
      expect(screen.getByRole("button", { name: de.carbon.closeModal })).toBeTruthy();
      noEnglish();
      fireEvent.click(within(dialog()).getByRole("button", { name: de.cancel }));

      fireEvent.click(screen.getByRole("tab", { name: de.retentionHoldsTab }));
      await screen.findByText("Case 4711");
      noEnglish();
      fireEvent.click(screen.getByRole("button", { name: de.holdRelease }));
      noEnglish();
      fireEvent.click(within(dialog()).getByRole("button", { name: de.cancel }));

      fireEvent.click(screen.getByRole("tab", { name: de.runsTab }));
      await screen.findByText(de.runLine(3, 2, "2,0 KB"));
      fireEvent.click(screen.getAllByRole("button", { name: de.runShow })[1]!);
      await within(dialog()).findByLabelText(de.runConfirmPrompt(2));
      expect(within(dialog()).getByText(de.close, { selector: ".cds--modal-footer button" })).toBeTruthy();
      noEnglish();
    } finally {
      applySettings({ language: "en", locale: "en" });
    }
  });
});
