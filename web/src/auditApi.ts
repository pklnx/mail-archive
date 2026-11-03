// The audit log API of the Log page. A module of its own, outside api.ts,
// so that it loads with the lazy LogPage chunk and not with the mail view
// (#288 bundle budget).
import { getJSON, type ManagedUser } from "./api";

/** A customer group (#470) as an admin's Log shows it. */
export interface AuditGroup {
  id: number;
  name: string;
}

/** One row of the audit log. */
export interface AuditEntry {
  id: number;
  at: string;
  actor: "session" | "api_key" | "cli";
  /** The user's name at the time. */
  user?: string;
  /** The name of the API key, for actor api_key. */
  apiKey?: string;
  action: string;
  target: { type: "message" | "account" | "user"; id: string; name?: string; subject?: string } | null;
  clientIp?: string;
  userAgent?: string;
  details: Record<string, unknown>;
  /**
   * Another user's read of the viewer's shared account (#389): no IP, user
   * agent or key name, and details cut down to via_accounts and, for lists,
   * the named first page's terms or only whether it searched (search).
   */
  viaShare?: boolean;
  /**
   * The customer group the entry is recorded under (#470): in an admin's
   * page only, missing for entries from before 1.4.0 and instance-level
   * ones; a removed group has no name and removed: true.
   */
  group?: { id: number; name?: string; removed?: boolean };
}

/**
 * The filters of the Log page. since and until are inclusive local days
 * "YYYY-MM-DD" (or ""). actions are action groups, any of them; users (names)
 * and actors ("cli", "api_key") are for admins only, and an entry of any of
 * them matches (#376). An empty list is no filter. group, for admins only, is
 * a customer group's id or "none" (entries without a group, #470); it
 * intersects with users and actors. "" or missing is no filter.
 */
export interface AuditFilter {
  since: string;
  until: string;
  actions: string[];
  users: string[];
  actors: string[];
  group?: string;
}

/**
 * The first instant of the local day ymd plus n days, as RFC 3339 (#402).
 * new Date(y, m, d) is local midnight, also on 23 h and 25 h days, and the
 * day's first instant where midnight does not exist; it rolls over month and
 * year ends. A value that is not a day is passed on for the server to refuse.
 */
function localDayStart(ymd: string, n: number): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(ymd);
  if (!m) return ymd;
  return new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]) + n).toISOString();
}

/**
 * The query string of GET /api/audit for a filter and a page cursor. The
 * server reads a plain date as a UTC day, so the local days go as instants
 * (#402): since is the start of the From day, until the start of the day
 * after To (the server's until is exclusive).
 */
export function auditQuery(f: AuditFilter, cursor = ""): string {
  const p = new URLSearchParams();
  if (f.since) p.set("since", localDayStart(f.since, 0));
  if (f.until) p.set("until", localDayStart(f.until, 1));
  for (const a of f.actions) p.append("action", a);
  for (const u of f.users) p.append("user", u);
  for (const a of f.actors) p.append("actor", a);
  if (f.group) p.set("group", f.group);
  if (cursor) p.set("cursor", cursor);
  return p.toString();
}

/**
 * A user of the admin's user list with the customer group GET /api/users
 * gives each (#470); read as optional, so a list without groups counts as
 * one group.
 */
export type GroupedUser = Omit<ManagedUser, "group"> & { group?: AuditGroup };

export const auditApi = {
  list: (f: AuditFilter, cursor: string, signal?: AbortSignal) =>
    getJSON<{ entries: AuditEntry[]; nextCursor?: string }>(`/api/audit?${auditQuery(f, cursor)}`, signal),
  /**
   * The customer groups, for the Group filter of an admin's Log (#470).
   * GET /api/groups answers { groups: [...] } like GET /api/users; a bare
   * list is read as well.
   */
  groups: async (signal?: AbortSignal) => {
    const r = await getJSON<AuditGroup[] | { groups?: AuditGroup[] }>("/api/groups", signal);
    return Array.isArray(r) ? r : (r.groups ?? []);
  },
};
