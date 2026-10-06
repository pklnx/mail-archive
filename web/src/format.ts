import { locale, t } from "./i18n";

const dateFmt = new Intl.DateTimeFormat(locale, { dateStyle: "medium" });
const timeFmt = new Intl.DateTimeFormat(locale, { timeStyle: "short" });
const fullFmt = new Intl.DateTimeFormat(locale, { dateStyle: "full", timeStyle: "short" });

/** Short date for lists: time for today, otherwise the date. */
export function shortDate(iso: string | null, now = new Date()): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const sameDay =
    d.getFullYear() === now.getFullYear() && d.getMonth() === now.getMonth() && d.getDate() === now.getDate();
  return sameDay ? timeFmt.format(d) : dateFmt.format(d);
}

export function longDate(iso: string | null): string {
  if (!iso) return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : fullFmt.format(d);
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

const relFmt = new Intl.RelativeTimeFormat(locale, { numeric: "auto" });

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
    if (Math.abs(v) < size) return relFmt.format(Math.round(v), unit);
    v /= size;
  }
  return relFmt.format(Math.round(v), "year");
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

export function formatCount(n: number): string {
  return n.toLocaleString(locale);
}
