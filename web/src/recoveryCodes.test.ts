import { describe, expect, it } from "vitest";
import { messagesFor } from "./i18n";
import { recoveryFileName, recoveryPdf, recoveryText, type RecoverySheet } from "./recoveryCodes";

const codes = Array.from({ length: 10 }, (_, i) => `abcd-efgh-jkmn-pq${"rstuvwxyz2"[i]}${i}`);
const sheet: RecoverySheet = {
  user: "patrick",
  origin: "https://mail.example.org",
  createdAt: new Date(2026, 9, 12, 17, 41),
  codes,
};
const en = messagesFor("en");
const de = messagesFor("de");

/** The content stream of a PDF made by onePagePdf (all ASCII). */
function content(pdf: Uint8Array): string {
  const s = String.fromCharCode(...pdf);
  return s.slice(s.indexOf("stream\n") + 7, s.indexOf("\nendstream"));
}

/** y positions of the Courier 16 lines (the codes). */
const codeYs = (c: string) => [...c.matchAll(/\/F3 16 Tf 56 ([\d.]+) Td/g)].map((m) => Number(m[1]));

describe("recoveryPdf", () => {
  it("has the codes in Courier, numbered, the user, server and date", () => {
    const c = content(recoveryPdf(sheet, en, "en-US"));
    codes.forEach((code, i) => expect(c).toContain(`/F3 16 Tf 56 ${680 - 26 * i} Td (${String(i + 1).padStart(2)}   ${code}) Tj`));
    expect(c).toContain("(Mail Archive \\226 Recovery codes)");
    expect(c).toContain("(patrick)");
    expect(c).toContain("(https://mail.example.org)");
    expect(c).toContain("(October 12, 2026 at 5:41 PM)");
  });

  it("dates the sheet in its own language", () => {
    const c = content(recoveryPdf(sheet, de, "de-DE"));
    expect(c).toContain("(12. Oktober 2026 um 17:41)");
    expect(c).toContain("(Mail Archive \\226 Wiederherstellungscodes)");
    expect(c).toContain("(Benutzer)");
  });

  it("wraps a long host and a long user name, and moves the codes down", () => {
    const plain = codeYs(content(recoveryPdf(sheet, en, "en-US")));
    const host = content(recoveryPdf({ ...sheet, origin: "https://" + "h".repeat(192) }, en, "en-US"));
    expect(codeYs(host)[0]!).toBe(plain[0]! - 3 * 16);
    expect(host).toContain(`(${"h".repeat(60)})`);
    const user = content(recoveryPdf({ ...sheet, user: "u".repeat(64) }, en, "en-US"));
    expect(codeYs(user)[0]!).toBe(plain[0]! - 16);
    expect(user).toContain(`(${"u".repeat(60)})`);
    expect(user).toContain("(uuuu)");
  });

  it("keeps every line on the page", () => {
    const c = content(recoveryPdf({ ...sheet, user: "u".repeat(64), origin: "https://" + "h".repeat(192) }, de, "de-DE"));
    const ys = [...c.matchAll(/Tf [\d.]+ ([\d.]+) Td/g)].map((m) => Number(m[1]));
    expect(Math.min(...ys.filter((y) => y !== 40))).toBeGreaterThan(60);
  });
});

describe("recoverySheetNote", () => {
  it("fits the page width in both languages", () => {
    for (const m of [en, de]) {
      // 483 pt at 10 pt Helvetica; Helvetica letters are at most about 0.56 em on average.
      for (const line of m.recoverySheetNote) expect(line.length, line).toBeLessThanOrEqual(85);
    }
  });
});

describe("recoveryText", () => {
  it("has the same lines", () => {
    const text = recoveryText(sheet, en, "en-US");
    expect(text.split("\n").slice(0, 6)).toEqual([
      "Mail Archive – Recovery codes",
      "",
      "User:    patrick",
      "Server:  https://mail.example.org",
      // Intl's own spacing: U+202F before "PM" in current ICU, a space in older ones.
      "Created: " + new Intl.DateTimeFormat("en-US", { dateStyle: "long", timeStyle: "short" }).format(sheet.createdAt),
      "",
    ]);
    codes.forEach((code, i) => expect(text).toContain(`${String(i + 1).padStart(2)}   ${code}\n`));
    for (const line of en.recoverySheetNote) expect(text).toContain(line);
  });
});

describe("recoveryFileName", () => {
  it("names the user and the local date", () => {
    expect(recoveryFileName("patrick", new Date(2026, 9, 12), "pdf")).toBe("mail-archive-recovery-codes-patrick-2026-10-12.pdf");
    expect(recoveryFileName("a.b", new Date(2026, 0, 5, 23, 59), "txt")).toBe("mail-archive-recovery-codes-a.b-2026-01-05.txt");
  });
});
