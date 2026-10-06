import { describe, expect, it } from "vitest";
import { fileSize, formatInterval, relativeTime, senderName, shortDate, splitHighlights } from "./format";

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
