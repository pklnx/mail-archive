import type { Filter } from "./api";

// Search syntax in the search field: free text plus prefixes such as
// `to:finanzamt after:2024-01-01`. The URL keeps the typed text; the client
// turns it into API parameters. The server never parses prefixes.

export interface ParsedQuery {
  /** Free text: everything that is not a recognized prefix. */
  text: string;
  from: string;
  to: string;
  attachment: string;
  hasAttachment: boolean;
  /** YYYY-MM-DD, inclusive. */
  after: string;
  /** YYYY-MM-DD, exclusive. */
  before: string;
}

export type TokenKey = "from" | "to" | "attachment" | "has" | "after" | "before";

const keys: readonly TokenKey[] = ["from", "to", "attachment", "has", "after", "before"];

interface Token {
  raw: string;
  /** Set for a recognized prefix token. */
  key?: TokenKey;
  value?: string;
}

/** Splits at whitespace outside double quotes; quotes stay in the tokens. */
function split(q: string): string[] {
  const out: string[] = [];
  let cur = "";
  let quoted = false;
  for (const ch of q) {
    if (ch === '"') quoted = !quoted;
    if (!quoted && /\s/.test(ch)) {
      if (cur) out.push(cur);
      cur = "";
      continue;
    }
    cur += ch;
  }
  if (cur) out.push(cur);
  return out;
}

function ymd(v: string): [number, number, number] {
  const [y = 0, m = 0, d = 0] = v.split("-").map(Number);
  return [y, m, d];
}

function isDate(v: string): boolean {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(v)) return false;
  const [y, m, d] = ymd(v);
  const date = new Date(Date.UTC(y, m - 1, d));
  return date.getUTCFullYear() === y && date.getUTCMonth() === m - 1 && date.getUTCDate() === d;
}

/** Strips the quotes around a value, also an unclosed one while typing. */
function unquote(v: string): string {
  if (v.startsWith('"')) v = v.slice(1);
  return v.endsWith('"') ? v.slice(0, -1) : v;
}

function classify(raw: string): Token {
  const m = /^(?<key>[a-z]+):(?<value>.*)$/is.exec(raw);
  const key = m?.groups?.key?.toLowerCase() as TokenKey | undefined;
  const rawValue = m?.groups?.value ?? "";
  if (!key || !keys.includes(key)) return { raw };
  if (rawValue.startsWith("//")) return { raw }; // a URL, not a prefix
  const value = unquote(rawValue);
  switch (key) {
    case "has":
      return value.toLowerCase() === "attachment" ? { raw, key, value: "attachment" } : { raw };
    case "after":
    case "before":
      return isDate(value) ? { raw, key, value } : { raw };
    default:
      // An empty value (still being typed) filters nothing and is no text.
      return { raw, key, value };
  }
}

function tokens(q: string): Token[] {
  return split(q).map(classify);
}

/** Parses the search field. If a prefix appears twice, the last one counts. */
export function parseQuery(q: string): ParsedQuery {
  const out: ParsedQuery = { text: "", from: "", to: "", attachment: "", hasAttachment: false, after: "", before: "" };
  const text: string[] = [];
  for (const tok of tokens(q)) {
    switch (tok.key) {
      case undefined:
        text.push(tok.raw);
        break;
      case "has":
        out.hasAttachment = true;
        break;
      default:
        out[tok.key] = tok.value ?? "";
    }
  }
  out.text = text.join(" ");
  return out;
}

/** Whether the search field contains a recognized prefix. */
export function hasPrefix(q: string): boolean {
  return tokens(q).some((tok) => tok.key !== undefined);
}

function format(key: TokenKey, value: string): string {
  const v = value.replaceAll('"', "");
  return /\s/.test(v) ? `${key}:"${v}"` : `${key}:${v}`;
}

/**
 * Sets one prefix in the search field, or removes it for an empty value.
 * The token takes the place of the first existing one, or goes to the end.
 * For `has`, any non-empty value means `has:attachment`.
 */
export function setToken(q: string, key: TokenKey, value: string): string {
  const all = tokens(q);
  const at = all.findIndex((tok) => tok.key === key);
  const rest = all.filter((tok) => tok.key !== key).map((tok) => tok.raw);
  if (value.replaceAll('"', "") !== "") {
    const tok = key === "has" ? "has:attachment" : format(key, value);
    rest.splice(at < 0 ? rest.length : at, 0, tok);
  }
  return rest.join(" ");
}

/**
 * The start of a day in the browser's time zone, as RFC 3339 with offset,
 * so that a date in the search means the user's day, not UTC's.
 */
export function localMidnight(date: string): string {
  const [y, m, d] = ymd(date);
  const local = new Date(y, m - 1, d);
  const offset = -local.getTimezoneOffset();
  const sign = offset < 0 ? "-" : "+";
  const abs = Math.abs(offset);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${date}T00:00:00${sign}${pad(Math.floor(abs / 60))}:${pad(abs % 60)}`;
}

/** The API filter for the search field: free text plus the prefixes. */
export function searchFilter(q: string): Filter {
  const p = parseQuery(q);
  return {
    q: p.text,
    from: p.from.trim(),
    to: p.to.trim(),
    attachment: p.attachment.trim(),
    hasAttachment: p.hasAttachment,
    after: p.after && localMidnight(p.after),
    before: p.before && localMidnight(p.before),
  };
}
