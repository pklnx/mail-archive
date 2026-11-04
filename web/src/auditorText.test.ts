import { afterEach, describe, expect, it } from "vitest";
import { auditorText, auditorTextFor, lastDay, periodText } from "./auditorText";
import { day } from "./format";
import { applySettings, languages } from "./i18n";

afterEach(() => applySettings({ language: "en", locale: "en" }));

describe("auditor texts (#393)", () => {
  it("translates every text", () => {
    const en = auditorTextFor("en");
    for (const l of languages) {
      const other = auditorTextFor(l);
      expect(Object.keys(other).sort(), l).toEqual(Object.keys(en).sort());
      for (const key of Object.keys(en) as (keyof typeof en)[]) {
        expect(typeof other[key], `${l}.${key}`).toBe(typeof en[key]);
        if (typeof other[key] === "string") expect(other[key], `${l}.${key}`).not.toBe("");
      }
    }
  });

  it("follows the UI language", () => {
    expect(auditorText().title).toBe("Auditor access");
    applySettings({ language: "de", locale: "de" });
    expect(auditorText().title).toBe("Prüferzugang");
    expect(auditorText().noAuditor).toBe("In deiner Kundengruppe gibt es keinen Prüfer mit diesem Namen.");
  });

  it("shows a half-open end as its last day", () => {
    expect(lastDay(new Date(2024, 0, 1).toISOString())).toBe(day(new Date(2023, 11, 31).toISOString()));
    expect(lastDay("not a date")).toBe("");
    const from = new Date(2021, 0, 1).toISOString();
    const until = new Date(2024, 0, 1).toISOString();
    expect(periodText({ periodFrom: from, periodUntil: until })).toBe(`Messages from ${day(from)} to ${lastDay(until)}.`);
    expect(periodText({ periodFrom: null, periodUntil: until })).toBe(`Messages up to ${lastDay(until)}.`);
    expect(periodText({ periodFrom: null, periodUntil: null })).toBe("");
  });
});
