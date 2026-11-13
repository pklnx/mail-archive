// The requests for SMTP secrets (#395): the secrets a mail server logs in
// with to hand over an smtp account's journal copies. Only the account page
// and the Users page import this module, so it is part of their lazy chunks
// and not of the initial bundle, like userAdminApi.ts.
import { getJSON, send } from "./api";

/**
 * A secret, never the secret itself. "expiring": a newer secret replaced it,
 * and it works until expiresAt.
 */
export interface SMTPCredential {
  id: number;
  /** The secret's last 4 characters. */
  hint: string;
  createdAt: string;
  expiresAt: string | null;
  lastUsedAt: string | null;
  revokedAt: string | null;
  state: "active" | "expiring" | "expired" | "revoked";
}

export interface SMTPCredentialList {
  /** The account's SMTP user name, "smtp-<account id>". */
  username: string;
  credentials: SMTPCredential[];
}

/** An older secret that a new one changed: it expires at expiresAt, or was revoked at revokedAt. */
export interface SupersededCredential {
  id: number;
  expiresAt?: string;
  revokedAt?: string;
}

/** The answer to a new secret: the only place the secret appears. */
export interface NewSMTPCredential {
  username: string;
  secret: string;
  credential: SMTPCredential;
  previous: SupersededCredential[];
}

/** A secret in the admins' list of all smtp accounts' secrets. */
export interface AdminSMTPCredential extends SMTPCredential {
  account: string;
  username: string;
  /** Empty for an account without owner. */
  owner: string;
  accountRemoved: boolean;
}

/** Whether a secret still lets a mail server log in. */
export const usable = (c: SMTPCredential) => c.state === "active" || c.state === "expiring";

/**
 * The list of an answer. An answer without one (a proxy's page, an older
 * server) is a failed request, not "no secrets": the caller shows its error
 * or leaves its section out, and the page around it keeps working.
 */
function listOf<T>(body: { credentials?: unknown } | null | undefined): T[] {
  if (!Array.isArray(body?.credentials)) throw new Error("the answer has no list of SMTP secrets");
  return body.credentials as T[];
}

const path = (account: string) => `/api/accounts/${encodeURIComponent(account)}/smtp-credentials`;

export const smtpApi = {
  /** The secrets of the own smtp account (admins). */
  list: async (account: string, signal?: AbortSignal) => {
    const body = await getJSON<SMTPCredentialList>(path(account), signal);
    listOf<SMTPCredential>(body);
    return body;
  },
  /**
   * A new secret; the current one stays usable for graceDays (0: revoked at
   * once). Needs the password and, with 2FA on, a code.
   */
  create: async (account: string, input: { currentPassword: string; code: string; graceDays: number }) =>
    (await send<NewSMTPCredential>("POST", path(account), input))!,
  revoke: (account: string, id: number) => send("DELETE", `${path(account)}/${id}`),
  /** Every smtp account's secrets, for admins (#395 Q2). */
  listAll: async (signal?: AbortSignal) => listOf<AdminSMTPCredential>(await getJSON<{ credentials?: unknown }>("/api/admin/smtp-credentials", signal)),
  revokeAny: (id: number) => send("DELETE", `/api/admin/smtp-credentials/${id}`),
};
