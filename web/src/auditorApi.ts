// The requests of auditor access (#393): an owner's grants, and an auditor's
// choice of the grant the session reads through. Only lazy chunks import
// this module (AuditorAccess, AuditorShell), so it stays out of the
// initial bundle (#288 budget).
import { getJSON, send, type Session } from "./api";
import type { AuditEntry } from "./auditApi";

/** Why a grant ended; null while it is not revoked. */
export type RevokeReason = "owner" | "operator" | "group_change" | "account_move" | "account_purge" | "user_removed";

/**
 * A grant as its owner lists it. auditor is null when the auditor left the
 * owner's group or was removed (revokeReason says which). suspended: inside
 * its window, but the owner or the auditor is locked.
 */
export interface Grant {
  id: number;
  owner: string;
  auditor: string | null;
  label: string;
  periodFrom: string | null;
  periodUntil: string | null;
  startsAt: string;
  endsAt: string;
  createdAt: string;
  revokedAt: string | null;
  revokeReason: RevokeReason | null;
  state: "active" | "scheduled" | "expired" | "revoked" | "suspended";
  /** folders null: all folders, also later ones. */
  accounts: { name: string; folders: string[] | null }[];
}

/** A new grant. Dates are instants; startsAt null starts it now. */
export interface GrantInput {
  auditor: string;
  label: string;
  accounts: { name: string; folders: string[] | null }[];
  periodFrom: string | null;
  periodUntil: string | null;
  startsAt: string | null;
  endsAt: string;
  currentPassword: string;
  code: string;
}

const grantPath = (id: number) => `/api/auditor-grants/${id}`;

export const auditorApi = {
  grants: async (signal?: AbortSignal) => (await getJSON<{ grants: Grant[] }>("/api/auditor-grants", signal)).grants,
  create: async (g: GrantInput) => (await send<Grant>("POST", "/api/auditor-grants", g))!,
  extend: async (id: number, endsAt: string, currentPassword: string, code: string) =>
    (await send<Grant>("PATCH", grantPath(id), { endsAt, currentPassword, code }))!,
  revoke: (id: number) => send("DELETE", grantPath(id)),
  /**
   * The auditor's last access to the grant's mail, from the owner's Log
   * (Q7): the newest login, search or read entry of the auditor (not of the
   * operator) on the first page; null if there is none.
   */
  lastActivity: async (id: number, signal?: AbortSignal) => {
    const { entries } = await getJSON<{ entries: AuditEntry[] }>(`/api/audit?grant=${id}&action=login&action=search&action=read`, signal);
    return entries.find((e) => e.viaAuditor && e.user !== "operator")?.at ?? null;
  },
  /** Binds the auditor's session to one of their active grants; answers the session. */
  select: async (id: number) => (await send<Session>("PUT", "/api/session/grant", { id }))!,
};
