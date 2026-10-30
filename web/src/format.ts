import { locale, t } from "./i18n";

// The formatters are made on first use for the current locale, which the
// user's settings can change after this module loaded.
const formatters = new Map<string, Intl.DateTimeFormat | Intl.RelativeTimeFormat>();

function cached<F extends Intl.DateTimeFormat | Intl.RelativeTimeFormat>(kind: string, make: (loc: string) => F): () => F {
  return () => {
    const key = `${kind} ${locale}`;
    let f = formatters.get(key);
    if (!f) {
      f = make(locale);
      formatters.set(key, f);
    }
    return f as F;
  };
}

const dateFmt = cached("date", (l) => new Intl.DateTimeFormat(l, { dateStyle: "medium" }));
const timeFmt = cached("time", (l) => new Intl.DateTimeFormat(l, { timeStyle: "short" }));
const fullFmt = cached("full", (l) => new Intl.DateTimeFormat(l, { dateStyle: "full", timeStyle: "short" }));
const stampFmt = cached("stamp", (l) => new Intl.DateTimeFormat(l, { dateStyle: "medium", timeStyle: "medium" }));
const monthFmt = cached("month", (l) => new Intl.DateTimeFormat(l, { month: "long", year: "numeric" }));
const yearFmt = cached("year", (l) => new Intl.DateTimeFormat(l, { year: "numeric" }));
const relFmt = cached("relative", (l) => new Intl.RelativeTimeFormat(l, { numeric: "auto" }));

/**
 * How a location reads in the message view. Old locations get a note, and
 * so do locations the server no longer lists.
 */
export function locationLabel(l: {
  account: string;
  folder: string;
  superseded: boolean;
  goneAt?: string | null;
  lastSeenAt?: string;
}): string {
  const where = `${l.account} / ${l.folder}`;
  if (l.superseded) return `${where} (${t.renumbered})`;
  if (l.goneAt) return `${where} (${t.goneFromServer(l.lastSeenAt ? dateFmt().format(new Date(l.lastSeenAt)) : "")})`;
  return where;
}

/**
 * The origin row of a location in an smtp account without an envelope: the
 * import of files (its label) or the fill (its mailbox) it came from, when
 * the server knows it, and otherwise a neutral text.
 */
export function originText(l: { fill: { username: string } | null; import: { label: string } | null }): string {
  if (l.import) return t.originImportedFrom(l.import.label);
  if (l.fill) return t.originFilledFrom(l.fill.username);
  return t.originFilled;
}

/** Short date for lists: time for today, otherwise the date. */
export function shortDate(iso: string | null, now = new Date()): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const sameDay =
    d.getFullYear() === now.getFullYear() && d.getMonth() === now.getMonth() && d.getDate() === now.getDate();
  return sameDay ? timeFmt().format(d) : dateFmt().format(d);
}

/**
 * The heading a list row falls under, newest first: today, yesterday, this
 * week (from Monday), this month, then the month of this year, then the year.
 */
export function dateGroup(iso: string, now = new Date()): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const days = Math.round((today.getTime() - new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()) / 86400000);
  if (days <= 0) return t.today;
  if (days === 1) return t.yesterday;
  if (days <= (today.getDay() + 6) % 7) return t.thisWeek;
  if (d.getFullYear() === now.getFullYear() && d.getMonth() === now.getMonth()) return t.thisMonth;
  return d.getFullYear() === now.getFullYear() ? monthFmt().format(d) : yearFmt().format(d);
}

export function longDate(iso: string | null): string {
  if (!iso) return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : fullFmt().format(d);
}

/** Date and time to the second, for lists of events. */
export function timestamp(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : stampFmt().format(d);
}

export function fileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB"];
  let v = bytes / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  const digits = v < 10 ? 1 : 0;
  return `${v.toLocaleString(locale, { minimumFractionDigits: digits, maximumFractionDigits: digits, useGrouping: false })} ${units[i]}`;
}

/** A number with up to two decimals in the user's number format, without grouping ("13,25" in German). */
export function decimal(n: number): string {
  return n.toLocaleString(locale, { maximumFractionDigits: 2, useGrouping: false });
}

/** The display name of an address like `"Jane Doe" <jane@x>`. */
export function senderName(from: string): string {
  const m = /^\s*"?([^"<]+?)"?\s*<[^>]+>\s*$/.exec(from);
  return (m?.[1] ?? from).trim() || t.unknownSender;
}

export type Segment = { text: string; match: boolean };

/** Splits a snippet on the server's highlight markers (U+E000 … U+E001). */
export function splitHighlights(snippet: string): Segment[] {
  const out: Segment[] = [];
  let match = false;
  let buf = "";
  for (const ch of snippet) {
    if (ch === "" || ch === "") {
      if (buf) out.push({ text: buf, match });
      buf = "";
      match = ch === "";
      continue;
    }
    buf += ch;
  }
  if (buf) out.push({ text: buf, match });
  return out;
}

/** "5 minutes ago", "yesterday", … */
export function relativeTime(iso: string, now = new Date()): string {
  const secs = (new Date(iso).getTime() - now.getTime()) / 1000;
  if (Number.isNaN(secs)) return "";
  const steps: [Intl.RelativeTimeFormatUnit, number][] = [
    ["second", 60],
    ["minute", 60],
    ["hour", 24],
    ["day", 30],
    ["month", 12],
  ];
  let v = secs;
  for (const [unit, size] of steps) {
    if (Math.abs(v) < size) return relFmt().format(Math.round(v), unit);
    v /= size;
  }
  return relFmt().format(Math.round(v), "year");
}

/** A Go duration like "6h0m0s" or "1h30m0s" in words: "6 hours". */
export function formatInterval(goDuration: string): string {
  const m = /^(?:(\d+)h)?(?:(\d+)m)?(?:[\d.]+s)?$/.exec(goDuration);
  if (!goDuration || !m) return goDuration;
  const parts: string[] = [];
  const unit = (n: number, u: string) =>
    new Intl.NumberFormat(locale, { style: "unit", unit: u, unitDisplay: "long" }).format(n);
  if (m[1] && m[1] !== "0") parts.push(unit(Number(m[1]), "hour"));
  if (m[2] && m[2] !== "0") parts.push(unit(Number(m[2]), "minute"));
  return parts.join(" ") || goDuration;
}

/** A Go duration like "720h0m0s" in words, in days when it is whole days: "30 days". */
export function formatDuration(goDuration: string): string {
  const min = durationMinutes(goDuration);
  if (min === null || min === 0 || min % (24 * 60) !== 0) return formatInterval(goDuration);
  return new Intl.NumberFormat(locale, { style: "unit", unit: "day", unitDisplay: "long" }).format(min / (24 * 60));
}

/** Whole minutes of a Go duration like "1h30m0s", or null if it is not one. */
export function durationMinutes(goDuration: string): number | null {
  const m = /^(?:(\d+)h)?(?:(\d+)m)?(?:0s)?$/.exec(goDuration);
  if (!goDuration || !m || (!m[1] && !m[2])) return null;
  return Number(m[1] ?? 0) * 60 + Number(m[2] ?? 0);
}

/** A Go duration as a frequency: "every hour", "every 30 minutes", "every day". */
export function formatEvery(goDuration: string, m = t): string {
  const min = durationMinutes(goDuration);
  if (min === null) return goDuration;
  if (min % (24 * 60) === 0) return min === 24 * 60 ? m.everyDay : m.everyDays(min / (24 * 60));
  if (min % 60 === 0) return min === 60 ? m.everyHour : m.everyHours(min / 60);
  return m.everyMinutes(min);
}

export function formatCount(n: number): string {
  return n.toLocaleString(locale);
}

/** The last day of a retention period, from its end (the first instant after it). */
export function retentionDay(iso: string): string {
  const ms = Date.parse(iso);
  return Number.isNaN(ms) ? "" : dateFmt().format(new Date(ms - 1));
}

/** A date for lists: the day only. */
export function day(iso: string | null): string {
  if (!iso) return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : dateFmt().format(d);
}

/**
 * Local calendar days (#286). A search date (after:, before:) is a day
 * "YYYY-MM-DD" in the browser's time zone; `new Date("YYYY-MM-DD")` would
 * be UTC midnight, the day before west of UTC. Days move with setDate, never
 * by 86 400 000 ms, which is off by one across a DST change.
 */
export function dateOfDay(ymd: string): Date {
  const [y, m, d] = ymd.split("-").map(Number);
  return new Date(y!, m! - 1, d);
}

/** The local day of a date as "YYYY-MM-DD". */
export function dayOfDate(date: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${String(date.getFullYear()).padStart(4, "0")}-${p(date.getMonth() + 1)}-${p(date.getDate())}`;
}

/** The day n days after ymd (before it for a negative n). */
export function shiftDay(ymd: string, n: number): string {
  const d = dateOfDay(ymd);
  d.setDate(d.getDate() + n);
  return dayOfDate(d);
}

/** A search day (parseQuery checked it) for the tags, in the user's date format. */
export function localDay(ymd: string): string {
  return dateFmt().format(dateOfDay(ymd));
}
