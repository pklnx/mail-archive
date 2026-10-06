// Types and calls for the JSON API served by `mail-archive serve`.

export interface Folder {
  name: string;
  messages: number;
  lastSyncedAt: string | null;
}

export type TLSMode = "tls" | "starttls" | "none";

export interface LastRun {
  startedAt: string;
  finishedAt: string | null;
  status: "running" | "ok" | "partial" | "failed";
  fetched: number;
  new: number;
  error?: string;
}

export interface Account {
  name: string;
  enabled: boolean;
  /** Removed accounts keep their mail but are never synced or changed. */
  removed: boolean;
  host: string;
  port: number;
  tls: TLSMode;
  username: string;
  includedFolders: string[];
  excludedFolders: string[];
  folders: Folder[];
  sync: { state: "idle" | "queued" | "running"; lastRun: LastRun | null };
}

export interface AccountsResponse {
  accounts: Account[];
  /** False when the server has no secret key: browse only. */
  manage: boolean;
  /** Go duration like "6h0m0s", empty if the schedule is off. */
  syncInterval: string;
}

export interface AccountInput {
  name?: string;
  host?: string;
  port?: number;
  tls?: TLSMode;
  username?: string;
  password?: string;
  enabled?: boolean;
  excludedFolders?: string[];
  /** POST only: save even if trash or spam folders would be archived. */
  confirmFolders?: boolean;
}

/**
 * The trash and spam folders the server asks about when an account is
 * created without confirmFolders, or null for any other error.
 */
export function folderQuestion(err: unknown): string[] | null {
  if (!(err instanceof ApiError) || err.status !== 409) return null;
  const list = err.body.suggestedExclusions;
  return Array.isArray(list) && list.every((f) => typeof f === "string") ? (list as string[]) : null;
}

export interface ServerFolder {
  name: string;
  specialUse?: string;
  selected: boolean;
}

export interface MessageSummary {
  id: string;
  size: number;
  subject: string;
  from: string;
  sentAt: string | null;
  sortAt: string;
  /** Matches are marked with U+E000 (start) and U+E001 (end). */
  snippet?: string;
}

export interface MessagePage {
  messages: MessageSummary[];
  nextCursor: string | null;
}

export interface Part {
  index: number;
  contentType: string;
  filename?: string;
  contentId?: string;
  size: number;
  attachment: boolean;
  inline: boolean;
}

export interface Location {
  account: string;
  folder: string;
  uid: number;
  flags: string[];
  internalDate: string | null;
}

export interface MessageDetail extends MessageSummary {
  messageId: string;
  to: string;
  cc: string;
  dateHeader: string;
  text: string;
  hasHtml: boolean;
  truncated: boolean;
  parts: Part[];
  locations: Location[];
}

export interface Filter {
  q?: string;
  account?: string;
  folder?: string;
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
    /** The parsed JSON error body, for errors that carry more than a message. */
    readonly body: Record<string, unknown> = {},
  ) {
    super(message);
  }
}

/**
 * Dispatched on window when an API request answers 401: the session ended
 * or was never there. The UI then shows the login page.
 */
export const UNAUTHORIZED_EVENT = "mail-archive:unauthorized";

const sessionPath = "/api/session";

async function errorFor(path: string, res: Response): Promise<ApiError> {
  const err = await errorFrom(res);
  // The session ended, or the user still has to replace a generated
  // password: the login gate checks the session again.
  if ((res.status === 401 && path !== sessionPath) || err.body.passwordChangeRequired === true) {
    window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
  }
  return err;
}

async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  const res = await fetch(path, { signal, headers: { Accept: "application/json" } });
  if (!res.ok) throw await errorFor(path, res);
  return (await res.json()) as T;
}

async function errorFrom(res: Response): Promise<ApiError> {
  let body: Record<string, unknown> = {};
  try {
    body = (await res.json()) as Record<string, unknown>;
  } catch {
    // not JSON
  }
  return new ApiError(res.status, typeof body.error === "string" ? body.error : res.statusText, body);
}

// Write requests carry JSON; the server rejects anything else (CSRF guard).
async function send<T>(method: string, path: string, body?: unknown): Promise<T | null> {
  const res = await fetch(path, {
    method,
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await errorFor(path, res);
  return res.status === 204 ? null : ((await res.json()) as T);
}

const accountPath = (name: string) => `/api/accounts/${encodeURIComponent(name)}`;

export function listAccounts(signal?: AbortSignal): Promise<AccountsResponse> {
  return getJSON("/api/accounts", signal);
}

export const accountsApi = {
  create: (a: AccountInput) => send("POST", "/api/accounts", a),
  update: (name: string, a: AccountInput) => send("PATCH", accountPath(name), a),
  remove: async (name: string) => (await send<{ result: "deleted" | "removed" }>("DELETE", accountPath(name)))!.result,
  serverFolders: async (name: string) =>
    (await getJSON<{ folders: ServerFolder[] }>(`${accountPath(name)}/server-folders`)).folders,
  sync: (name: string) => send("POST", `${accountPath(name)}/sync`),
  syncAll: () => send("POST", "/api/sync"),
};

export function listMessages(filter: Filter, cursor: string | null, signal?: AbortSignal): Promise<MessagePage> {
  const p = new URLSearchParams();
  if (filter.q) p.set("q", filter.q);
  if (filter.account) p.set("account", filter.account);
  if (filter.folder) p.set("folder", filter.folder);
  if (cursor) p.set("cursor", cursor);
  p.set("limit", "50");
  return getJSON(`/api/messages?${p}`, signal);
}

export function getMessage(id: string, signal?: AbortSignal): Promise<MessageDetail> {
  return getJSON(`/api/messages/${encodeURIComponent(id)}`, signal);
}

export const messageURL = {
  html: (id: string, images: boolean) => `/api/messages/${id}/html${images ? "?images=1" : ""}`,
  raw: (id: string) => `/api/messages/${id}/raw`,
  part: (id: string, n: number) => `/api/messages/${id}/parts/${n}`,
};

export interface User {
  name: string;
  admin: boolean;
  /** Logged in with a generated password: must choose their own first. */
  mustChangePassword: boolean;
}

/** The login state: a user, or none and whether the first admin is missing. */
export type SessionState = { user: User } | { user: null; setupRequired: boolean };

export const sessionApi = {
  get: async (signal?: AbortSignal): Promise<SessionState> => {
    const res = await fetch(sessionPath, { signal, headers: { Accept: "application/json" } });
    if (res.status === 401) {
      const err = await errorFrom(res);
      return { user: null, setupRequired: err.body.setupRequired === true };
    }
    if (!res.ok) throw await errorFrom(res);
    return (await res.json()) as { user: User };
  },
  login: async (username: string, password: string): Promise<User> =>
    (await send<{ user: User }>("POST", sessionPath, { username, password }))!.user,
  logout: () => send("DELETE", sessionPath),
};

/** Seconds to wait after too many failed logins, from a 429 answer. */
export function retryAfter(err: unknown): number | null {
  if (!(err instanceof ApiError) || err.status !== 429) return null;
  const s = err.body.retryAfter;
  return typeof s === "number" && s > 0 ? s : 60;
}

export const profileApi = {
  changePassword: (current: string, next: string) => send("PUT", "/api/profile/password", { current, new: next }),
};

/** A user as admins see them on the users page. */
export interface ManagedUser {
  name: string;
  admin: boolean;
  locked: boolean;
  mustChangePassword: boolean;
  accounts: number;
  createdAt: string;
  lastLoginAt: string | null;
  /** The logged-in admin, whose own row cannot be changed there. */
  self: boolean;
}

/** A generated password, shown once. */
export interface GeneratedPassword {
  name: string;
  password: string;
}

const userPath = (name: string) => `/api/users/${encodeURIComponent(name)}`;

export const usersApi = {
  list: async (signal?: AbortSignal) => (await getJSON<{ users: ManagedUser[] }>("/api/users", signal)).users,
  create: async (name: string, admin: boolean) => (await send<GeneratedPassword>("POST", "/api/users", { name, admin }))!,
  resetPassword: async (name: string) => (await send<GeneratedPassword>("POST", `${userPath(name)}/password`))!,
  setAdmin: (name: string, admin: boolean) => send("PATCH", userPath(name), { admin }),
  setLocked: (name: string, locked: boolean) => send("PATCH", userPath(name), { locked }),
  remove: (name: string) => send("DELETE", userPath(name)),
};
