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

const userPath = (name: string) => `/api/users/${encodeURIComponent(name)}`;
const groupPath = (id: number) => `/api/groups/${id}`;

export const userAdminApi = {
  /** group: the customer group's id; needed while several groups exist (#470). */
  create: async (name: string, admin: boolean, group?: number) =>
    (await send<GeneratedPassword>("POST", "/api/users", { name, admin, group }))!,
  resetPassword: async (name: string, removePasskeys: boolean, revokeApiKeys: boolean) =>
    (await send<GeneratedPassword>("POST", `${userPath(name)}/password`, { removePasskeys, revokeApiKeys }))!,
  setAdmin: (name: string, admin: boolean) => send("PATCH", userPath(name), { admin }),
  setLocked: (name: string, locked: boolean) => send("PATCH", userPath(name), { locked }),
  /** Moves the user into the group; resolves to the number of shares that ended. */
  setGroup: async (name: string, group: number) => (await send<{ sharesEnded: number }>("PATCH", userPath(name), { group }))!.sharesEnded,
  remove: (name: string) => send("DELETE", userPath(name)),
  reset2FA: (name: string) => send("POST", `${userPath(name)}/2fa/reset`),
  removePasskeys: (name: string) => send("DELETE", `${userPath(name)}/passkeys`),
};

export const groupsApi = {
  list: async (signal?: AbortSignal) => (await getJSON<{ groups: CustomerGroup[] }>("/api/groups", signal)).groups,
  create: async (name: string) => (await send<CustomerGroup>("POST", "/api/groups", { name }))!,
  rename: (id: number, name: string) => send("PATCH", groupPath(id), { name }),
  remove: (id: number) => send("DELETE", groupPath(id)),
};
