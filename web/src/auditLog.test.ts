import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { auditQuery, type AuditEntry } from "./api";
import { foldEntries, shortUserAgent } from "./auditLog";
import { defaultFilter, entryDetail, targetLabel } from "./LogPage";
import { messagesFor, t } from "./i18n";

let id = 0;
function entry(action: string, at: string, more: Partial<AuditEntry> = {}): AuditEntry {
  return { id: ++id, at: `2026-10-11T10:${at}Z`, actor: "session", user: "alice", action, target: null, details: {}, ...more };
}
const msg = (sha: string) => ({ type: "message" as const, id: sha });
const newestFirst = (list: AuditEntry[]) => [...list].reverse();

describe("foldEntries", () => {
  it("folds the prefixes of a search typed into the last search", () => {
    const list = [
      entry("message.search", "00:00", { details: { q: "inv" } }),
      entry("message.search", "00:02", { details: { q: "invoi" } }),
      entry("message.search", "00:03", { details: { q: "invoice" } }),
      entry("message.open", "00:05", { target: msg("a") }),
    ];
    const rows = foldEntries(newestFirst(list));
    expect(rows.map((r) => r.entry.action)).toEqual(["message.open", "message.search"]);
    expect(rows[1]!.entry.details.q).toBe("invoice");
    expect(rows[1]!.folded.map((e) => e.details.q)).toEqual(["inv", "invoi"]);
  });

  it("keeps searches apart that are not one typed search", () => {
    const list = [
      entry("message.search", "00:00", { details: { q: "invoice" } }),
      entry("message.search", "00:30", { details: { q: "invoice 2026" } }), // too late
      entry("message.search", "00:31", { details: { q: "tax" } }), // no prefix
      entry("message.search", "00:32", { details: { q: "tax" }, user: "bob" }), // another user
    ];
    expect(foldEntries(newestFirst(list))).toHaveLength(4);
  });

  it("does not fold across another action of the same user", () => {
    const list = [
      entry("message.search", "00:00", { details: { q: "in" } }),
      entry("message.open", "00:01", { target: msg("a") }),
      entry("message.search", "00:02", { details: { q: "invoice" } }),
    ];
    expect(foldEntries(newestFirst(list))).toHaveLength(3);
  });

  it("folds inline images into the HTML row of their message", () => {
    const list = [
      entry("message.open", "00:00", { target: msg("a") }),
      entry("message.html", "00:01", { target: msg("a") }),
      entry("message.part", "00:01", { target: msg("a"), details: { index: 1, inline: true } }),
      entry("message.part", "00:02", { target: msg("a"), details: { index: 2, inline: true } }),
      entry("message.part", "00:09", { target: msg("a"), details: { index: 3, inline: false } }),
      entry("message.part", "00:10", { target: msg("b"), details: { index: 1, inline: true } }),
    ];
    const rows = foldEntries(newestFirst(list));
    expect(rows.map((r) => r.entry.action)).toEqual(["message.part", "message.part", "message.html", "message.open"]);
    expect(rows[2]!.folded).toHaveLength(2);
  });
});

// The days go as instants of local midnight (#402), so the zone is fixed.
describe("auditQuery", () => {
  beforeEach(() => vi.stubEnv("TZ", "Europe/Berlin"));
  afterEach(() => vi.unstubAllEnvs());
  it("turns the filter into query parameters", () => {
    const none = { actions: [], users: [], actors: [] };
    expect(auditQuery({ ...none, since: "2026-10-01", until: "", actions: ["read"] })).toBe("since=2026-09-30T22%3A00%3A00.000Z&action=read");
    expect(auditQuery({ ...none, since: "", until: "2026-10-31", actors: ["cli"] }, "abc")).toBe("until=2026-10-31T23%3A00%3A00.000Z&actor=cli&cursor=abc");
    expect(auditQuery({ ...none, since: "", until: "", actors: ["api_key"] })).toBe("actor=api_key");
    expect(auditQuery({ ...none, since: "", until: "", users: ["alice"] })).toBe("user=alice");
  });
  it("starts 30 days back", () => {
    expect(defaultFilter(new Date("2026-10-31T12:00:00Z")).since).toBe("2026-10-01");
  });
});

describe("entry texts", () => {
  it("names targets and details", () => {
    expect(targetLabel(entry("message.open", "00:00", { target: { type: "message", id: "a".repeat(64), subject: "Invoice" } }))).toBe("Invoice");
    expect(targetLabel(entry("message.open", "00:00", { target: msg("b".repeat(64)) }))).toBe("bbbbbbbbbbbb…");
    expect(targetLabel(entry("account.add", "00:00", { target: { type: "account", id: "3", name: "work" } }))).toBe("work");
    expect(entryDetail(entry("message.search", "00:00", { details: { q: "tax", from: "bob" } }))).toBe("“tax” from:bob");
    expect(
      entryDetail(
        entry("profile.change", "00:00", {
          details: { fields: ["language", "locale"], language: { from: "auto", to: "de" }, locale: { from: "auto", to: "de-AT" } },
        }),
      ),
    ).toBe("language: auto → de, locale: auto → de-AT");
    expect(entryDetail(entry("auth.login_failed", "00:00", { details: { method: "password", reason: "wrong_password" } }))).toBe("password, wrong_password");
  });
  it("words other users' reads of a shared account without the terms of unnamed lists (#389, R6)", () => {
    const via = [{ id: 3, owner: 2, key: "alice/work", n: 2 }];
    const shared = (action: string, details: Record<string, unknown>) => entryDetail(entry(action, "00:00", { user: "pat", viaShare: true, details }));
    // Unnamed lists and every further page: the count, never the terms, even if they came.
    expect(shared("message.search", { search: true, q: "secret", via_accounts: via })).toBe("pat's search showed 2 messages of work");
    expect(shared("message.list", { search: false, folder: "Board", via_accounts: via })).toBe("pat's list showed 2 messages of work");
    expect(shared("message.page", { search: true, q: "secret", account: "alice/work", via_accounts: via })).toBe("pat's search showed 2 messages of work");
    expect(shared("message.page", { search: false, via_accounts: [{ ...via[0], n: 1 }] })).toBe("pat's list showed 1 message of work");
    // A named first page keeps its terms (decision 13).
    expect(shared("message.search", { q: "secret", account: "alice/work", via_accounts: [{ ...via[0], n: 0 }] })).toBe(
      "“secret” account:alice/work; pat's search showed 0 messages of work",
    );
    expect(shared("message.conversation", { via_accounts: [{ ...via[0], n: 1 }] })).toBe("pat's conversation view showed 1 message of work");
    expect(shared("message.open", { via_accounts: [{ id: 3, owner: 2, key: "alice/work" }] })).toBe("in work");
    const de = messagesFor("de");
    expect(de.logShared("pat", "search", 2, "work")).toBe("Suche von pat zeigte 2 Nachrichten aus work");
    expect(de.logShared("pat", "list", 1, "work")).toBe("Liste von pat zeigte 1 Nachricht aus work");
  });
  it("words the share entries and the lists of the reader's own Log (#389)", () => {
    expect(entryDetail(entry("account.share", "00:00", { details: { user: "pat", user_id: 4 } }))).toBe(t.logShareWith("pat"));
    expect(entryDetail(entry("message.list", "00:00", { details: { account: "anna/work", folder: "INBOX", via_owners: [2] } }))).toBe("account:anna/work folder:INBOX");
    const de = messagesFor("de");
    for (const a of ["message.list", "message.page", "message.conversation", "account.share", "account.unshare"]) {
      expect(t.auditAction(a)).not.toBe(a);
      expect(de.auditAction(a)).not.toBe(a);
    }
    expect(t.auditAction("account.share")).toBe("Shared account");
    expect(de.auditAction("account.unshare")).toBe("Freigabe beendet");
  });
  it("words every action and falls back to the name", () => {
    expect(t.auditAction("message.open")).not.toBe("message.open");
    expect(t.auditAction("data.fill_change")).toBe("Changed import from IMAP");
    expect(t.auditAction("data.fill_cancel")).toBe("Stopped or cancelled import from IMAP");
    expect(t.auditAction("future.thing")).toBe("future.thing");
  });
  it("words the account purge actions in every language", () => {
    const de = messagesFor("de");
    for (const a of ["account.purge", "account.purge_batch", "account.purge_finish", "account.purge_cancel"]) {
      expect(t.auditAction(a)).not.toBe(a);
      expect(de.auditAction(a)).not.toBe(a);
    }
    expect(t.auditAction("account.purge")).toBe("Account deletion confirmed");
    expect(de.auditAction("account.purge")).toBe("Löschen des Kontos bestätigt");
  });
  it("shortens user agents", () => {
    expect(shortUserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:141.0) Gecko/20100101 Firefox/141.0")).toBe("Firefox 141 · Windows");
    expect(shortUserAgent("Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 Chrome/140.0 Mobile Safari/537.36")).toBe("Chrome 140 · Android");
    expect(shortUserAgent("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Version/18.0 Mobile/15E148 Safari/604.1")).toBe(
      "Safari 18 · iPhone",
    );
    expect(shortUserAgent("curl/8.9.1")).toBe("curl/8.9.1");
  });
});
