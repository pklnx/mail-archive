// The filter form's date presets (#286): each fills after: (never before:)
// on the browser's local day; presetOf tells which preset a range is.
import { afterEach, describe, expect, it, vi } from "vitest";
import { inputFormat, presetOf, presetRange } from "./datePresets";

// Tests that set TZ restore it, so other tests run in the original zone.
afterEach(() => {
  vi.unstubAllEnvs();
});

/** now as a wall-clock time in zone tz (set before the Date is made). */
function at(tz: string, y: number, m: number, d: number, h = 12, min = 0): Date {
  vi.stubEnv("TZ", tz);
  return new Date(y, m - 1, d, h, min);
}

describe("presetRange", () => {
  it.each([
    // The DST end in Berlin: 25 Oct 2026 has 25 hours.
    ["Europe/Berlin", [2026, 10, 25, 12], "2026-10-19", "2026-09-26", "2026-01-01"],
    ["Europe/Berlin", [2026, 10, 26, 0, 30], "2026-10-20", "2026-09-27", "2026-01-01"],
    // The DST start in Berlin: 29 Mar 2026 has 23 hours.
    ["Europe/Berlin", [2026, 3, 30, 0, 15], "2026-03-24", "2026-03-01", "2026-01-01"],
    // Late evening west of UTC: UTC is the next day already.
    ["America/Los_Angeles", [2026, 10, 28, 23, 30], "2026-10-22", "2026-09-29", "2026-01-01"],
    // UTC+14: UTC is still the day before.
    ["Pacific/Kiritimati", [2026, 1, 1, 0, 30], "2025-12-26", "2025-12-03", "2026-01-01"],
    // A leap year: 29 days in February.
    ["Europe/Berlin", [2028, 3, 1], "2028-02-24", "2028-02-01", "2028-01-01"],
    ["UTC", [2028, 2, 29], "2028-02-23", "2028-01-31", "2028-01-01"],
  ] as const)("%s %j: last 7 from %s, last 30 from %s, this year from %s", (tz, [y, m, d, h, min], last7, last30, year) => {
    const now = at(tz, y, m, d, h, min);
    expect(presetRange("any", now)).toEqual({ after: "", before: "" });
    expect(presetRange("last7", now)).toEqual({ after: last7, before: "" });
    expect(presetRange("last30", now)).toEqual({ after: last30, before: "" });
    expect(presetRange("year", now)).toEqual({ after: year, before: "" });
  });
});

describe("presetOf", () => {
  it("gives the preset of its own range, Any time without dates, and Custom for every other range", () => {
    const now = at("Europe/Berlin", 2026, 10, 28);
    expect(presetOf("", "", now)).toBe("any");
    expect(presetOf("2026-10-22", "", now)).toBe("last7");
    expect(presetOf("2026-09-29", "", now)).toBe("last30");
    expect(presetOf("2026-01-01", "", now)).toBe("year");
    // Only before:, an end, an off-by-one start, last year's start.
    expect(presetOf("", "2026-10-01", now)).toBe("custom");
    expect(presetOf("2026-10-22", "2026-10-29", now)).toBe("custom");
    expect(presetOf("2026-10-21", "", now)).toBe("custom");
    expect(presetOf("2026-10-23", "", now)).toBe("custom");
    expect(presetOf("2025-01-01", "", now)).toBe("custom");
  });

  it("follows the local day, not UTC's", () => {
    // 23:30 in Los Angeles is the next day in UTC.
    const now = at("America/Los_Angeles", 2026, 10, 28, 23, 30);
    expect(presetOf("2026-10-22", "", now)).toBe("last7");
    expect(presetOf("2026-10-23", "", now)).toBe("custom");
  });
});

describe("inputFormat", () => {
  const letters = { day: "dd", month: "mm", year: "yyyy" };
  it.each([
    ["de", "d.m.Y", "dd.mm.yyyy"],
    ["de-AT", "d.m.Y", "dd.mm.yyyy"],
    ["en-US", "m/d/Y", "mm/dd/yyyy"],
    ["en", "m/d/Y", "mm/dd/yyyy"],
    ["en-GB", "d/m/Y", "dd/mm/yyyy"],
    ["sv", "Y-m-d", "yyyy-mm-dd"],
  ])("%s: %s, placeholder %s", (locale, format, placeholder) => {
    expect(inputFormat(locale, letters)).toEqual({ format, placeholder });
  });

  it("spells the placeholder with the given letters", () => {
    expect(inputFormat("de", { day: "TT", month: "MM", year: "JJJJ" }).placeholder).toBe("TT.MM.JJJJ");
  });
});
