// The requests of the Users page that change users and customer groups.
// Only UsersPage imports this module, so it is part of the lazy Users chunk
// and not of the initial bundle (#470 part 2); api.ts keeps usersApi.list,
// which the Log and Retention pages use too.
import { getJSON, send, type GeneratedPassword } from "./api";

/** A customer group (#470), with its users and their accounts (removed ones too). */
export interface CustomerGroup {
  id: number;
  name: string;
  users: number;
  accounts: number;
  createdAt: string;
}

/** A grant that ended with its auditor's removal (#393): it stays in its owner's list as history. */
export interface RevokedGrant {
  id: number;
  label: string;
  owner: string;
  auditor: string;
}

const userPath = (name: string) => `/api/users/${encodeURIComponent(name)}`;
const groupPath = (id: number) => `/api/groups/${id}`;

export const userAdminApi = {
  /**
   * group: the customer group's id; needed while several groups exist
   * (#470). auditor: an auditor login (#393), never an admin; sent only when set.
   */
  create: async (name: string, admin: boolean, group?: number, auditor = false) =>
    (await send<GeneratedPassword>("POST", "/api/users", { name, admin, ...(auditor && { auditor }), group }))!,
  resetPassword: async (name: string, removePasskeys: boolean, revokeApiKeys: boolean) =>
    (await send<GeneratedPassword>("POST", `${userPath(name)}/password`, { removePasskeys, revokeApiKeys }))!,
  setAdmin: (name: string, admin: boolean) => send("PATCH", userPath(name), { admin }),
  setLocked: (name: string, locked: boolean) => send("PATCH", userPath(name), { locked }),
  /** Moves the user into the group; resolves to how many shares and auditor grants (#393) ended. */
  setGroup: async (name: string, group: number) =>
    (await send<{ sharesEnded: number; grantsRevoked?: number }>("PATCH", userPath(name), { group }))!,
  /** Removes the user; for an auditor resolves to the grants that ended (#393), else to none. */
  remove: async (name: string) => (await send<{ grantsRevoked?: RevokedGrant[] }>("DELETE", userPath(name)))?.grantsRevoked ?? [],
  reset2FA: (name: string) => send("POST", `${userPath(name)}/2fa/reset`),
  removePasskeys: (name: string) => send("DELETE", `${userPath(name)}/passkeys`),
};

export const groupsApi = {
  list: async (signal?: AbortSignal) => (await getJSON<{ groups: CustomerGroup[] }>("/api/groups", signal)).groups,
  create: async (name: string) => (await send<CustomerGroup>("POST", "/api/groups", { name }))!,
  rename: (id: number, name: string) => send("PATCH", groupPath(id), { name }),
  remove: (id: number) => send("DELETE", groupPath(id)),
};
