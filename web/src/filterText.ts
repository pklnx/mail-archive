import { lang, type Lang } from "./i18n";

// The texts of the filter form (FilterPanel.tsx) and the date range
// (DateRange.tsx) that i18n.ts does not have. Only the date range component
// and the filter form import it; both are lazy, so it stays out of the
// initial bundle (#286, #377).

const en = {
  presets: { any: "Any time", last7: "Last 7 days", last30: "Last 30 days", year: "This year", custom: "Custom" },
  since: "Since",
  until: "Until",
  /** The letters the placeholder spells the input format with (dd/mm/yyyy). */
  letters: { day: "dd", month: "mm", year: "yyyy" },
  format: (placeholder: string) => `Format: ${placeholder}`,
  calendar: "Calendar",
  nextMonth: "Next month",
  previousMonth: "Previous month",
};

const de: typeof en = {
  presets: { any: "Beliebig", last7: "Letzte 7 Tage", last30: "Letzte 30 Tage", year: "Dieses Jahr", custom: "Benutzerdefiniert" },
  since: "Ab",
  until: "Bis",
  letters: { day: "TT", month: "MM", year: "JJJJ" },
  format: (placeholder) => `Format: ${placeholder}`,
  calendar: "Kalender",
  nextMonth: "Nächster Monat",
  previousMonth: "Vorheriger Monat",
};

const dictionaries = { en, de } satisfies Record<Lang, typeof en>;

export type FilterText = typeof en;

/** The texts in the current UI language (the live lang binding of i18n.ts). */
export function filterText(): FilterText {
  return dictionaries[lang];
}

export function filterTextFor(l: Lang): FilterText {
  return dictionaries[l];
}
