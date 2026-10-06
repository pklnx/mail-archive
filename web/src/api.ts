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
  ) {
    super(message);
  }
}

async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  const res = await fetch(path, { signal, headers: { Accept: "application/json" } });
  if (!res.ok) throw await errorFrom(res);
  return (await res.json()) as T;
}

async function errorFrom(res: Response): Promise<ApiError> {
  let msg = res.statusText;
  try {
    msg = ((await res.json()) as { error?: string }).error ?? msg;
  } catch {
    // not JSON
  }
  return new ApiError(res.status, msg);
}

// Write requests carry JSON; the server rejects anything else (CSRF guard).
async function send<T>(method: string, path: string, body?: unknown): Promise<T | null> {
  const res = await fetch(path, {
    method,
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await errorFrom(res);
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
