// The accounts of the mail view: the viewer's own, then those other users
// shared with them (#389). Own accounts are named by their name, shared
// ones by owner/name, the key that account= takes. Names cannot contain
// "/", so the two never collide, also when a shared account has the name
// of an own one.
import type { Account, AccountsResponse, Folder, Health } from "./api";
import { t } from "./i18n";

export interface MailAccount {
  /** account= and the sidebar's node key: the name, or owner/name for a shared account. */
  key: string;
  name: string;
  /** The owner of a shared account; null for own accounts. */
  owner: string | null;
  kind: Account["kind"];
  enabled: boolean;
  removed: boolean;
  folders: Folder[];
  /** Own accounts only; a shared account's sync is its owner's business ("idle", "ok"). */
  syncState: Account["sync"]["state"];
  health: Health;
}

/** Own accounts first, in the server's order, then the shared ones. */
export function mailAccounts(data: AccountsResponse): MailAccount[] {
  return [
    ...data.accounts.map(
      (a): MailAccount => ({
        key: a.name, name: a.name, owner: null, kind: a.kind, enabled: a.enabled, removed: a.removed, folders: a.folders,
        syncState: a.sync.state, health: a.sync.health,
      }),
    ),
    ...data.shared.map(
      (a): MailAccount => ({
        key: a.key, name: a.name, owner: a.owner, kind: a.kind, enabled: a.enabled, removed: a.removed, folders: a.folders,
        syncState: "idle", health: "ok",
      }),
    ),
  ];
}

/** An account's name for the mail view: "info", or "info · anna" when anna shared it. */
export function accountLabel(a: { name: string; owner: string | null }): string {
  return a.owner ? t.sharedName(a.name, a.owner) : a.name;
}

/**
 * The label of an account= value: its account's, or the value itself while
 * the accounts are not loaded (or it names none).
 */
export function labelOfKey(data: AccountsResponse | null, key: string): string {
  const a = data ? mailAccounts(data).find((x) => x.key === key) : undefined;
  return a ? accountLabel(a) : key;
}
