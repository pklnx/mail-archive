// Types and calls for the JSON API served by `mail-archive serve`.

export interface Folder {
  name: string;
  messages: number;
  lastSyncedAt: string | null;
}

export interface Account {
  name: string;
  enabled: boolean;
  folders: Folder[];
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
  if (!res.ok) {
    let msg = res.statusText;
    try {
      msg = ((await res.json()) as { error?: string }).error ?? msg;
    } catch {
      // not JSON
    }
    throw new ApiError(res.status, msg);
  }
  return (await res.json()) as T;
}

export function listAccounts(signal?: AbortSignal): Promise<{ accounts: Account[] }> {
  return getJSON("/api/accounts", signal);
}

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
