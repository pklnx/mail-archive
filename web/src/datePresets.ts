import { dayOfDate } from "./format";

// The date presets of the filter panel (#286). A preset only fills the
// search's after: and before: (the syntax does not change); none sets
// before:. Days are the browser's local days.

export const presets = ["any", "last7", "last30", "year", "custom"] as const;
export type Preset = (typeof presets)[number];

/** The range a preset stands for, on the local day of now. Custom stands for no range of its own. */
export function presetRange(preset: Exclude<Preset, "custom">, now: Date): { after: string; before: string } {
  const back = (n: number) => dayOfDate(new Date(now.getFullYear(), now.getMonth(), now.getDate() - n));
  switch (preset) {
    case "any":
      return { after: "", before: "" };
    case "last7":
      return { after: back(6), before: "" };
    case "last30":
      return { after: back(29), before: "" };
    case "year":
      return { after: dayOfDate(new Date(now.getFullYear(), 0, 1)), before: "" };
  }
}

/** The preset a range is: Any time without dates, Custom for every range no preset gives. */
export function presetOf(after: string, before: string, now: Date): Preset {
  for (const p of ["any", "last7", "last30", "year"] as const) {
    const r = presetRange(p, now);
    if (r.after === after && r.before === before) return p;
  }
  return "custom";
}

type Part = "day" | "month" | "year";

/**
 * The date picker's input format for a locale, in flatpickr's tokens (d, m,
 * Y), in the locale's order and with its separators: "de" gives d.m.Y,
 * "en-US" m/d/Y, "en-GB" d/m/Y. The placeholder spells the same format
 * with the UI language's letters for each part (dd.mm.yyyy, TT.MM.JJJJ).
 */
export function inputFormat(locale: string, letters: Record<Part, string>): { format: string; placeholder: string } {
  const token: Record<Part, string> = { day: "d", month: "m", year: "Y" };
  let format = "";
  let placeholder = "";
  for (const p of new Intl.DateTimeFormat(locale, { day: "2-digit", month: "2-digit", year: "numeric" }).formatToParts(new Date(2026, 9, 28))) {
    if (p.type in token) {
      format += token[p.type as Part];
      placeholder += letters[p.type as Part];
    } else if (p.type === "literal") {
      // Letters would be flatpickr tokens; direction marks are invisible.
      const sep = p.value.replace(/[\p{L}\u200e\u200f]/gu, "");
      format += sep;
      placeholder += sep;
    }
  }
  return { format, placeholder };
}
