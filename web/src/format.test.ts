import { afterEach, describe, expect, it } from "vitest";
import { dateGroup, day, fileSize, formatCount, formatInterval, locationLabel, originText, relativeTime, senderName, shortDate, splitHighlights } from "./format";
import { applySettings, t } from "./i18n";

describe("splitHighlights", () => {
  it("splits on the server's markers", () => {
    expect(splitHighlights("Ihre Rechnung liegt bei")).toEqual([
      { text: "Ihre ", match: false },
      { text: "Rechnung", match: true },
      { text: " liegt bei", match: false },
    ]);
  });
  it("handles plain text and adjacent matches", () => {
    expect(splitHighlights("plain")).toEqual([{ text: "plain", match: false }]);
    expect(splitHighlights("ab")).toEqual([
      { text: "a", match: true },
      { text: "b", match: true },
    ]);
  });
  it("treats markup in snippets as text", () => {
    expect(splitHighlights("<b>x</b>")).toEqual([{ text: "<b>x</b>", match: false }]);
  });
});

describe("senderName", () => {
  it("extracts display names", () => {
    expect(senderName('"Jürgen Müller" <j@example.com>')).toBe("Jürgen Müller");
    expect(senderName("Alice <alice@example.com>")).toBe("Alice");
    expect(senderName("bob@example.com")).toBe("bob@example.com");
    expect(senderName("")).toBe("(unknown sender)");
  });
});

describe("fileSize", () => {
  it("formats sizes", () => {
    expect(fileSize(512)).toBe("512 B");
    expect(fileSize(1536)).toBe("1.5 KB");
    expect(fileSize(20 * 1024 * 1024)).toBe("20 MB");
  });
});

describe("shortDate", () => {
  it("shows the time for today and the date otherwise", () => {
    const now = new Date(2026, 9, 5, 18, 0);
    expect(shortDate(new Date(2026, 9, 5, 9, 30).toISOString(), now)).toMatch(/9|09/);
    expect(shortDate(new Date(2025, 0, 2).toISOString(), now)).toMatch(/2025/);
    expect(shortDate(null, now)).toBe("");
    expect(shortDate("garbage", now)).toBe("");
  });
});

describe("relativeTime and formatInterval", () => {
  it("formats in words", () => {
    const now = new Date("2026-10-06T12:00:00Z");
    expect(relativeTime("2026-10-06T11:55:00Z", now)).toBe("5 minutes ago");
    expect(relativeTime("2026-10-05T12:00:00Z", now)).toBe("yesterday");
    expect(relativeTime("garbage", now)).toBe("");
    expect(formatInterval("6h0m0s")).toBe("6 hours");
    expect(formatInterval("1h30m0s")).toBe("1 hour 30 minutes");
    expect(formatInterval("5m0s")).toBe("5 minutes");
  });
});

describe("originText", () => {
  it("names the label of an import of files", () => {
    expect(originText({ fill: null, import: { label: "piler" } })).toBe(t.originImportedFrom("piler"));
    expect(t.originImportedFrom("piler")).toContain("piler");
    expect(t.originImportedFrom("piler")).toContain("files");
  });
  it("names the mailbox a fill imported from", () => {
    expect(originText({ fill: { username: "ak@example.com" }, import: null })).toBe(t.originFilledFrom("ak@example.com"));
    expect(t.originFilledFrom("ak@example.com")).toContain("ak@example.com");
  });
  it("falls back to a neutral text without a fill or an import", () => {
    expect(originText({ fill: null, import: null })).toBe(t.originFilled);
    expect(t.originFilled).not.toContain("IMAP");
  });
});

describe("locationLabel", () => {
  it("marks locations from before a renumbering", () => {
    expect(locationLabel({ account: "work", folder: "INBOX", superseded: false })).toBe("work / INBOX");
    expect(locationLabel({ account: "work", folder: "INBOX", superseded: true })).toBe(`work / INBOX (${t.renumbered})`);
  });
  it("marks locations no longer on the server, with the last time seen", () => {
    const l = { account: "work", folder: "INBOX", superseded: false, goneAt: "2026-10-09T08:00:00Z", lastSeenAt: "2026-10-08T12:00:00Z" };
    const label = locationLabel(l);
    expect(label.startsWith(`work / INBOX (`)).toBe(true);
    expect(label).toContain("2026");
    expect(locationLabel({ ...l, goneAt: null })).toBe("work / INBOX");
    // A renumbered location is history either way.
    expect(locationLabel({ ...l, superseded: true })).toBe(`work / INBOX (${t.renumbered})`);
  });
});

describe("formats after a settings change", () => {
  afterEach(() => applySettings({ language: "auto", locale: "auto" }));
  it("follows the new locale", () => {
    applySettings({ language: "auto", locale: "en-US" });
    expect(formatCount(1234)).toBe("1,234");
    expect(day("2026-10-12T12:00:00Z")).toBe("Oct 12, 2026");
    applySettings({ language: "auto", locale: "de-DE" });
    expect(formatCount(1234)).toBe("1.234");
    expect(day("2026-10-12T12:00:00Z")).toBe("12.10.2026");
    expect(shortDate("2026-10-12T12:00:00Z", new Date("2027-01-01T00:00:00Z"))).toBe("12.10.2026");
    expect(relativeTime("2026-10-11T12:00:00Z", new Date("2026-10-12T12:00:00Z"))).toBe("gestern");
  });
});

describe("dateGroup", () => {
  afterEach(() => applySettings({ language: "auto", locale: "auto" }));
  // Thursday, October 15, 2026, local time.
  const now = new Date(2026, 9, 15, 12, 0);
  const at = (m: number, d: number, y = 2026) => new Date(y, m, d, 9, 30).toISOString();
  it("goes from today to the year", () => {
    applySettings({ language: "en", locale: "en-US" });
    expect(dateGroup(at(9, 15), now)).toBe("Today");
    expect(dateGroup(at(9, 14), now)).toBe("Yesterday");
    expect(dateGroup(at(9, 12), now)).toBe("This week"); // Monday
    expect(dateGroup(at(9, 11), now)).toBe("This month"); // Sunday before
    expect(dateGroup(at(9, 1), now)).toBe("This month");
    expect(dateGroup(at(8, 30), now)).toBe("September 2026");
    expect(dateGroup(at(0, 2), now)).toBe("January 2026");
    expect(dateGroup(at(11, 31, 2025), now)).toBe("2025");
  });
  it("puts dates after now under today and follows the language", () => {
    applySettings({ language: "de", locale: "de-DE" });
    expect(dateGroup(at(9, 16), now)).toBe("Heute");
    expect(dateGroup(at(8, 3), now)).toBe("September 2026");
    expect(dateGroup("nonsense", now)).toBe("");
  });
  it("counts this week from Monday, also on Sunday and Monday", () => {
    applySettings({ language: "en", locale: "en-US" });
    const sunday = new Date(2026, 9, 18, 12, 0);
    expect(dateGroup(at(9, 12), sunday)).toBe("This week");
    expect(dateGroup(at(9, 11), sunday)).toBe("This month");
    const monday = new Date(2026, 9, 12, 12, 0);
    expect(dateGroup(at(9, 11), monday)).toBe("Yesterday");
    expect(dateGroup(at(9, 10), monday)).toBe("This month");
  });
});
