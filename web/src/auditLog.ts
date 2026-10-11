import type { AuditEntry } from "./api";

/** A row of the Log page: an entry and the entries folded into it. */
export interface LogRow {
  entry: AuditEntry;
  folded: AuditEntry[];
}

/** Searches of one user this close together, each a prefix of the next, are one search typed. */
const typingWindow = 10_000;
/** Inline images load right after the HTML body that shows them. */
const inlineWindow = 60_000;

const who = (e: AuditEntry) => `${e.actor}:${e.user ?? ""}:${e.apiKey ?? ""}`;
const query = (e: AuditEntry) => (typeof e.details.q === "string" ? e.details.q : "");
const time = (e: AuditEntry) => new Date(e.at).getTime();

/**
 * Folds the rows a person does not read one by one, from entries newest
 * first: the inline images of a message into its message.html row, and the
 * prefixes a search box sends while someone types ("inv", "invoice") into
 * the last search. The log itself stays complete; this only groups rows.
 */
export function foldEntries(entries: AuditEntry[]): LogRow[] {
  const rows: LogRow[] = [];
  const lastOf = new Map<string, LogRow>(); // the newest row so far per user
  const htmlOf = new Map<string, LogRow>(); // the newest message.html row per user and message
  for (const e of [...entries].reverse()) {
    const key = who(e);
    const prev = lastOf.get(key);
    if (e.action === "message.part" && e.details.inline === true && e.target) {
      const html = htmlOf.get(`${key}|${e.target.id}`);
      if (html && time(e) - time(html.entry) <= inlineWindow) {
        html.folded.push(e);
        continue;
      }
    }
    if (
      e.action === "message.search" &&
      prev?.entry.action === "message.search" &&
      time(e) - time(prev.entry) <= typingWindow &&
      query(e).startsWith(query(prev.entry)) &&
      query(prev.entry) !== ""
    ) {
      const row: LogRow = { entry: e, folded: [...prev.folded, prev.entry] };
      rows[rows.indexOf(prev)] = row;
      lastOf.set(key, row);
      continue;
    }
    const row: LogRow = { entry: e, folded: [] };
    rows.push(row);
    lastOf.set(key, row);
    if (e.action === "message.html" && e.target) htmlOf.set(`${key}|${e.target.id}`, row);
  }
  return rows.reverse();
}

/** A short form of a user agent: the browser and its platform, like "Firefox 141 · Windows". */
export function shortUserAgent(ua: string): string {
  let browser = "";
  let m: RegExpExecArray | null;
  if ((m = /Edg\/(\d+)/.exec(ua))) browser = `Edge ${m[1]}`;
  else if ((m = /Firefox\/(\d+)/.exec(ua))) browser = `Firefox ${m[1]}`;
  else if ((m = /Chrome\/(\d+)/.exec(ua))) browser = `Chrome ${m[1]}`;
  else if ((m = /Version\/(\d+).*Safari/.exec(ua))) browser = `Safari ${m[1]}`;
  // In this order: Android user agents also say Linux, iPhones "like Mac OS X".
  const platform = ["iPhone", "iPad", "Android", "Windows", "Mac OS X", "Linux"].find((p) => ua.includes(p))?.replace("Mac OS X", "macOS") ?? "";
  const parts = [browser, platform].filter(Boolean);
  return parts.length > 0 ? parts.join(" · ") : ua.slice(0, 40);
}
