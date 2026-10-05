const dateFmt = new Intl.DateTimeFormat(undefined, { dateStyle: "medium" });
const timeFmt = new Intl.DateTimeFormat(undefined, { timeStyle: "short" });
const fullFmt = new Intl.DateTimeFormat(undefined, { dateStyle: "full", timeStyle: "short" });

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
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}

/** The display name of an address like `"Jane Doe" <jane@x>`. */
export function senderName(from: string): string {
  const m = /^\s*"?([^"<]+?)"?\s*<[^>]+>\s*$/.exec(from);
  return (m?.[1] ?? from).trim() || "(unknown sender)";
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
