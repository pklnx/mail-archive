import { describe, expect, it } from "vitest";
import type { LegalHold } from "./api";
import { retentionDay } from "./format";
import { t } from "./i18n";
import { accountLabel, holdScope, runOpen } from "./RetentionPage";

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
