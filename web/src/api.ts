// Types and calls for the JSON API served by `mail-archive serve`.

export interface Folder {
  name: string;
  messages: number;
  lastSyncedAt: string | null;
  /** When the folder was last compared with the server. */
  lastReconciledAt: string | null;
  /**
   * The server's hierarchy delimiter ("/" or "."), "" for a server without
   * hierarchy, null while unknown (before the folder's first sync).
   */
  delimiter: string | null;
}

export type TLSMode = "tls" | "starttls" | "none";

export interface LastRun {
  startedAt: string;
  finishedAt: string | null;
  status: "running" | "ok" | "partial" | "failed";
  fetched: number;
  new: number;
  error?: string;
  /** Reconcile: folders compared with the server, messages newly gone or back, flag changes. */
  reconciledFolders: number;
  gone: number;
  back: number;
  flagsChanged: number;
}

export interface Account {
  name: string;
  /**
   * "import": mail imported from files with `import`; "smtp": mail a mail
   * server delivers over SMTP. Neither has a server or is ever synced.
   */
  kind: "imap" | "import" | "smtp";
  enabled: boolean;
  /** Removed accounts keep their mail but are never synced or changed. */
  removed: boolean;
  host: string;
  port: number;
  tls: TLSMode;
  username: string;
  includedFolders: string[];
  excludedFolders: string[];
  /**
   * The account's own sync interval: "default" (the server's), "manual"
   * (not on the schedule) or a Go duration like "30m0s". Always "default"
   * for import and smtp accounts.
   */
  syncInterval: string;
  folders: Folder[];
  sync: SyncInfo;
  /** Messages whose copies in this account are all gone from the server. */
  goneMessages: number;
  /** The latest comparison of any folder with the server. */
  lastReconciledAt: string | null;
  /** smtp accounts: the addresses they receive for, and the last accepted message. */
  addresses: string[];
  lastReceivedAt: string | null;
}

/**
 * "failing": the last alertAfter syncs failed; "stale": no successful sync
 * within two of the account's intervals (at least one plus an hour), never
 * while it is running or queued.
 */
export type Health = "ok" | "failing" | "stale";

export interface SyncInfo {
  state: "idle" | "queued" | "running";
  lastRun: LastRun | null;
  /** Failed syncs in a row, and when the first of them started. */
  failureStreak: number;
  failingSince: string | null;
  health: Health;
  /** How often the schedule syncs the account (Go duration), null when it does not. */
  syncEvery: string | null;
  /** When the schedule syncs it next; in the past while it waits. Null without syncEvery or when disabled. */
  nextAt: string | null;
}

export interface AccountsResponse {
  accounts: Account[];
  /** False when the server has no secret key: browse only. */
  manage: boolean;
  /** The server's default interval, Go duration like "6h0m0s"; empty if the schedule is off. */
  syncInterval: string;
  /** The floor for every account's interval, like "5m0s"; empty when the server cannot sync. */
  syncIntervalMin: string;
  /** Go duration like "24h0m0s", empty if folders are only compared on request. */
  reconcileInterval: string;
  /** Failed syncs in a row that make an account failing. */
  alertAfter: number;
  /** Admins only: failing accounts of other users, as a count. */
  otherFailing?: number;
  /** Null when the SMTP server is off: smtp accounts then receive nothing. */
  smtp: Record<string, never> | null;
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
  /** "default", "manual" or a Go duration of whole minutes. */
  syncInterval?: string;
  /** POST only: save even if trash or spam folders would be archived. */
  confirmFolders?: boolean;
  /** POST only: "imap" (default) or "smtp" (admins). */
  kind?: "imap" | "smtp";
  /** smtp accounts, admins only: the addresses; in an update the complete new list. */
  addresses?: string[];
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
  hasAttachment: boolean;
  /** Grouped listings: the thread key and the number of matching messages in it. */
  thread?: string;
  count?: number;
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
  /** From before the folder's UIDVALIDITY changed: the server renumbered the folder. */
  superseded: boolean;
  /** When a reconcile found it missing on the server; null while it is there. */
  goneAt: string | null;
  /** When the server last listed it. */
  lastSeenAt: string;
  /** smtp accounts: the envelope the message arrived with. */
  envelope: Envelope | null;
  /** When the retention period ends; null without a rule (kept for ever). */
  retainUntil: string | null;
  /** On legal hold; only sent to admins. */
  onHold?: boolean;
}

/** The SMTP envelope of a received message; the recipients include BCC. */
export interface Envelope {
  mailFrom: string;
  rcptTo: string[];
  receivedAt: string;
  clientIp: string;
  helo: string;
}

export interface MessageDetail extends MessageSummary {
  messageId: string;
  /** The Message-ID this message answers. */
  inReplyTo?: string;
  to: string;
  cc: string;
  dateHeader: string;
  text: string;
  hasHtml: boolean;
  truncated: boolean;
  parts: Part[];
  locations: Location[];
  /** The latest retention end over the locations; null if one is kept for ever. */
  retainUntil: string | null;
  /** One of the locations is on legal hold; only sent to admins. */
  onHold?: boolean;
}

export interface Filter {
  q?: string;
  account?: string;
  folder?: string;
  /** Substrings of the sender, a To or Cc recipient, an attachment name. */
  from?: string;
  to?: string;
  attachment?: string;
  hasAttachment?: boolean;
  /** RFC 3339; after is inclusive, before exclusive. */
  after?: string;
  before?: string;
  /** One row per conversation. */
  group?: boolean;
  /** Only the messages of this conversation (a thread key). */
  thread?: string;
  /** Only messages no longer on any server (only in the archive). */
  gone?: boolean;
}

export type Relation = "self" | "parent" | "reply" | "thread";

export interface ConversationEntry {
  id: string;
  subject: string;
  from: string;
  sentAt: string | null;
  sortAt: string;
  relation: Relation;
}

export interface Conversation {
  /** Oldest first. */
  messages: ConversationEntry[];
  total: number;
  /** Older messages were left out. */
  truncated: boolean;
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
  if ((res.status === 401 && !path.startsWith(sessionPath)) || err.body.passwordChangeRequired === true || err.body.twoFactorSetupRequired === true) {
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
  /** Sync, and compare every folder with the server. */
  reconcile: (name: string) => send("POST", `${accountPath(name)}/sync`, { reconcile: true }),
  syncAll: () => send("POST", "/api/sync"),
};

export function listMessages(filter: Filter, cursor: string | null, signal?: AbortSignal, limit = 50): Promise<MessagePage> {
  const p = new URLSearchParams();
  if (filter.q) p.set("q", filter.q);
  if (filter.account) p.set("account", filter.account);
  if (filter.folder) p.set("folder", filter.folder);
  if (filter.from) p.set("from", filter.from);
  if (filter.to) p.set("to", filter.to);
  if (filter.attachment) p.set("attachment", filter.attachment);
  if (filter.hasAttachment) p.set("has", "attachment");
  if (filter.after) p.set("after", filter.after);
  if (filter.before) p.set("before", filter.before);
  if (filter.group) p.set("group", "1");
  if (filter.thread) p.set("thread", filter.thread);
  if (filter.gone) p.set("gone", "1");
  if (cursor) p.set("cursor", cursor);
  p.set("limit", String(limit));
  return getJSON(`/api/messages?${p}`, signal);
}

export function getMessage(id: string, signal?: AbortSignal): Promise<MessageDetail> {
  return getJSON(`/api/messages/${encodeURIComponent(id)}`, signal);
}

export function getConversation(id: string, signal?: AbortSignal): Promise<Conversation> {
  return getJSON(`/api/messages/${encodeURIComponent(id)}/conversation`, signal);
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
  twoFactorEnabled: boolean;
  twoFactorRequired: boolean;
  /** Web UI settings: "auto" (follow the browser) or a tag. */
  language: string;
  locale: string;
}

/**
 * The login state: a user, or none and whether the first admin is missing.
 * passkeyOrigin is where passkey logins work, if the server offers them.
 */
export type SessionState = { user: User } | { user: null; setupRequired: boolean; passkeyOrigin?: string };

export const sessionApi = {
  get: async (signal?: AbortSignal): Promise<SessionState> => {
    const res = await fetch(sessionPath, { signal, headers: { Accept: "application/json" } });
    if (res.status === 401) {
      const err = await errorFrom(res);
      const origin = err.body.passkeyOrigin;
      return { user: null, setupRequired: err.body.setupRequired === true, passkeyOrigin: typeof origin === "string" ? origin : undefined };
    }
    if (!res.ok) throw await errorFrom(res);
    return (await res.json()) as { user: User };
  },
  login: async (username: string, password: string): Promise<{ user?: User; twoFactorRequired?: boolean; challenge?: string }> =>
    (await send<{ user?: User; twoFactorRequired?: boolean; challenge?: string }>("POST", sessionPath, { username, password }))!,
  login2FA: async (challenge: string, code: string): Promise<User> =>
    (await send<{ user: User }>("POST", "/api/session/2fa", { challenge, code }))!.user,
  logout: () => send("DELETE", sessionPath),
};

/** A ceremony started on the server: options for the browser and a token. */
export interface Ceremony {
  token: string;
  publicKey: Record<string, unknown>;
}

export interface Passkey {
  id: number;
  name: string;
  createdAt: string;
  lastUsedAt: string | null;
}

export interface PasskeyList {
  /** False when the server has no public URL. */
  available: boolean;
  /** Where passkeys work, like https://archive.example.ts.net. */
  origin?: string;
  max: number;
  passkeys: Passkey[];
}

/** The server's answer for a passkey it does not know. */
export function unknownPasskey(err: unknown): { rpId: string; credentialId: string } | null {
  if (!(err instanceof ApiError) || err.body.unknownCredential !== true) return null;
  const { rpId, credentialId } = err.body;
  return typeof rpId === "string" && typeof credentialId === "string" ? { rpId, credentialId } : null;
}

export const passkeyApi = {
  beginLogin: async () => (await send<Ceremony>("POST", "/api/session/passkey/begin"))!,
  finishLogin: async (token: string, credential: unknown) =>
    (await send<{ user: User }>("POST", "/api/session/passkey/finish", { token, credential }))!.user,
  list: () => getJSON<PasskeyList>("/api/profile/passkeys"),
  beginRegistration: async (name: string, currentPassword: string, code: string) =>
    (await send<Ceremony>("POST", "/api/profile/passkeys/begin", { name, currentPassword, code }))!,
  finishRegistration: (token: string, credential: unknown) => send("POST", "/api/profile/passkeys/finish", { token, credential }),
  remove: async (id: number) => (await send<{ credentialId: string; rpId?: string }>("DELETE", `/api/profile/passkeys/${id}`))!,
};

export type ApiKeyScope = "read" | "sync";

/** An API key as listed: never the key itself. */
export interface ApiKey {
  id: number;
  /** Only in the admin list. */
  user?: string;
  name: string;
  /** The last 4 characters of the key. */
  hint: string;
  scopes: ApiKeyScope[];
  createdAt: string;
  expiresAt: string | null;
  lastUsedAt: string | null;
  revokedAt: string | null;
  state: "active" | "expired" | "revoked";
}

export interface ApiKeyList {
  max: number;
  keys: ApiKey[];
}

/** A key just created: the key itself is in this answer only. */
export type NewApiKey = ApiKey & { key: string };

export interface ApiKeyInput {
  name: string;
  scopes: ApiKeyScope[];
  /** Days until the key expires; null for never. */
  expiresInDays: number | null;
  currentPassword: string;
  code: string;
}

export const apiKeyApi = {
  list: () => getJSON<ApiKeyList>("/api/profile/api-keys"),
  create: async (input: ApiKeyInput) => (await send<NewApiKey>("POST", "/api/profile/api-keys", input))!,
  revoke: (id: number) => send("DELETE", `/api/profile/api-keys/${id}`),
  /** Every user's keys, for admins. */
  listAll: async () => (await getJSON<{ keys: ApiKey[] }>("/api/api-keys")).keys,
  revokeAny: (id: number) => send("DELETE", `/api/api-keys/${id}`),
};

/** Seconds to wait after too many failed logins, from a 429 answer. */
export function retryAfter(err: unknown): number | null {
  if (!(err instanceof ApiError) || err.status !== 429) return null;
  const s = err.body.retryAfter;
  return typeof s === "number" && s > 0 ? s : 60;
}

export interface TwoFactorSetup {
  enabled: boolean;
  required: boolean;
  admin: boolean;
  setupPending: boolean;
  secret?: string;
  otpauthUri?: string;
  qrDataUrl?: string;
}

export const profileApi = {
  changePassword: (current: string, next: string) => send("PUT", "/api/profile/password", { current, new: next }),
  update: (p: { language?: string; locale?: string }) => send("PATCH", "/api/profile", p),
  twoFactor: {
    get: () => getJSON<TwoFactorSetup>("/api/profile/2fa"),
    setup: () => send<TwoFactorSetup>("POST", "/api/profile/2fa/setup"),
    confirm: (code: string) => send<{ enabled: boolean; recoveryCodes: string[] }>("POST", "/api/profile/2fa/confirm", { code }),
    disable: (currentPassword: string, code: string) => send("DELETE", "/api/profile/2fa", { currentPassword, code }),
    regenerateRecoveryCodes: (code: string) => send<{ recoveryCodes: string[] }>("POST", "/api/profile/2fa/recovery-codes", { code }),
  },
};

/** A user as admins see them on the users page. */
export interface ManagedUser {
  name: string;
  admin: boolean;
  locked: boolean;
  mustChangePassword: boolean;
  twoFactorEnabled: boolean;
  passkeys: number;
  /** API keys that are neither revoked nor expired. */
  apiKeys: number;
  accounts: number;
  /** The user's accounts whose syncs keep failing. */
  failingAccounts: number;
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
  resetPassword: async (name: string, removePasskeys: boolean, revokeApiKeys: boolean) =>
    (await send<GeneratedPassword>("POST", `${userPath(name)}/password`, { removePasskeys, revokeApiKeys }))!,
  setAdmin: (name: string, admin: boolean) => send("PATCH", userPath(name), { admin }),
  setLocked: (name: string, locked: boolean) => send("PATCH", userPath(name), { locked }),
  remove: (name: string) => send("DELETE", userPath(name)),
  reset2FA: (name: string) => send("POST", `${userPath(name)}/2fa/reset`),
  removePasskeys: (name: string) => send("DELETE", `${userPath(name)}/passkeys`),
};

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
}

/** The filters of the Log page. who is "" (all), "cli", "api_key" or a user name (admins only). */
export interface AuditFilter {
  since: string;
  until: string;
  group: string;
  who: string;
}

/** The query string of GET /api/audit for a filter and a page cursor. */
export function auditQuery(f: AuditFilter, cursor = ""): string {
  const p = new URLSearchParams();
  if (f.since) p.set("since", f.since);
  if (f.until) p.set("until", f.until);
  if (f.group) p.set("action", f.group);
  if (f.who === "cli" || f.who === "api_key") p.set("actor", f.who);
  else if (f.who) p.set("user", f.who);
  if (cursor) p.set("cursor", cursor);
  return p.toString();
}

export const auditApi = {
  list: (f: AuditFilter, cursor: string, signal?: AbortSignal) =>
    getJSON<{ entries: AuditEntry[]; nextCursor?: string }>(`/api/audit?${auditQuery(f, cursor)}`, signal),
};


/** A retention rule: an account's (or folder's) mail is kept for years after the end of its year. */
export interface RetentionRule {
  id: number;
  accountId: number;
  account: string;
  accountKind: string;
  accountRemoved: boolean;
  owner: string;
  /** Empty for every folder of the account. */
  folder: string;
  years: number;
  timeZone: string;
  note: string;
  createdAt: string;
  createdBy: string;
}

/** An account to choose for a rule or a hold. */
export interface RetentionAccount {
  id: number;
  name: string;
  kind: string;
  removed: boolean;
  owner: string;
  folders: string[];
}

export interface RetentionRules {
  rules: RetentionRule[];
  accounts: RetentionAccount[];
  /** Rules shorter than this need force. */
  minYears: number;
  defaultTimeZone: string;
}

export interface RetentionRuleInput {
  accountId: number;
  folder: string;
  years: number;
  timeZone: string;
  note: string;
  force: boolean;
}

export interface HoldFilter {
  q?: string;
  from?: string;
  to?: string;
  attachment?: string;
  hasAttachment?: boolean;
}

export interface LegalHold {
  id: number;
  name: string;
  reason: string;
  user: string;
  accountId: number | null;
  account: string;
  accountOwner: string;
  filter: HoldFilter | null;
  since: string | null;
  until: string | null;
  matchedAt: string | null;
  matched: number;
  createdAt: string;
  createdBy: string;
  releasedAt: string | null;
  releasedBy: string;
  releaseNote: string;
}

export interface LegalHoldInput {
  name: string;
  reason: string;
  user: string;
  accountId: number | null;
  filter: HoldFilter | null;
  since: string | null;
  until: string | null;
  all: boolean;
}

export const retentionApi = {
  rules: (signal?: AbortSignal) => getJSON<RetentionRules>("/api/retention/rules", signal),
  addRule: async (r: RetentionRuleInput) => (await send<{ id: number }>("POST", "/api/retention/rules", r))!,
  removeRule: (id: number) => send("DELETE", `/api/retention/rules/${id}`),
  holds: async (released: boolean, signal?: AbortSignal) =>
    (await getJSON<{ holds: LegalHold[] }>(`/api/legal-holds${released ? "?released=1" : ""}`, signal)).holds,
  addHold: async (h: LegalHoldInput) => (await send<{ id: number; matched?: number }>("POST", "/api/legal-holds", h))!,
  releaseHold: (id: number, note: string) => send("POST", `/api/legal-holds/${id}/release`, { note }),
  runs: async (signal?: AbortSignal) => (await getJSON<{ runs: DeletionRun[] }>("/api/retention/runs", signal)).runs,
  propose: async () => (await send<DeletionRun>("POST", "/api/retention/runs"))!,
  run: (id: number, signal?: AbortSignal) => getJSON<DeletionReview>(`/api/retention/runs/${id}`, signal),
  confirm: (id: number, count: number) => send("POST", `/api/retention/runs/${id}/confirm`, { count }),
  cancel: (id: number) => send("POST", `/api/retention/runs/${id}/cancel`),
};

export type DeletionRunState = "proposed" | "confirmed" | "done" | "cancelled";

export interface DeletionRun {
  id: number;
  state: DeletionRunState;
  createdAt: string;
  createdBy: string;
  candidates: number;
  bytes: number;
  confirmedAt: string | null;
  confirmedBy: string;
  finishedAt: string | null;
  deleted: number;
  kept: number;
}

/** A run for review: counts by place and year, and candidates by hash only. */
export interface DeletionReview {
  run: DeletionRun;
  groups: { owner: string; account: string; folder: string; year: number; messages: number }[];
  candidates: { sha256: string; size: number; retainUntil: string; state: "pending" | "deleted" | "kept" }[];
  /** How many candidates are listed; at most 1000. */
  listed: number;
}
