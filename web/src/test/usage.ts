import type { StorageState, UsageAdmin, UsageMember } from "../api";

// Answers of GET /api/usage for the tests of the banner and the Storage
// page (#397, part 4). Only test files import this module.

const GiB = 1024 ** 3;

/** An admin's view: instance quota 50 GiB, groups Default (no quota) and Meier (40 GiB), three accounts. */
export function adminUsage(more: { instance?: Partial<UsageAdmin["instance"]>; groups?: UsageAdmin["groups"]; ungrouped?: UsageAdmin["ungrouped"] } = {}): UsageAdmin {
  return {
    instance: {
      messages: 12000,
      bytes: 30 * GiB,
      quotaBytes: 50 * GiB,
      warnPercent: 80,
      overflowPercent: 10,
      minFreeDiskBytes: GiB,
      state: "ok",
      stateSince: "2026-11-01T10:00:00Z",
      disk: { freeBytes: 200 * GiB, totalBytes: 500 * GiB },
      databaseBytes: 2 * GiB,
      relations: { messages: GiB },
      lastBackupAt: "2026-11-12T03:00:00Z",
      encryption: "on",
      ...more.instance,
    },
    groups: more.groups ?? [
      { id: 1, name: "Default", messages: 4000, bytes: 10 * GiB, quotaBytes: null, state: "ok", stateSince: null },
      { id: 2, name: "Meier", messages: 8000, bytes: 20 * GiB, quotaBytes: 40 * GiB, state: "ok", stateSince: null },
    ],
    ungrouped: more.ungrouped ?? { messages: 0, bytes: 0 },
    accounts: [
      { id: 1, name: "info", kind: "imap", removed: false, owner: "anna", groupId: 1, own: false, messages: 4000, bytes: 10 * GiB, exclusiveBytes: 9 * GiB, counted: true },
      { id: 2, name: "buchhaltung", kind: "imap", removed: false, owner: "admin", groupId: 2, own: true, messages: 8000, bytes: 20 * GiB, exclusiveBytes: 20 * GiB, counted: true },
      { id: 3, name: "neu", kind: "smtp", removed: false, owner: "anna", groupId: 1, own: false, messages: 0, bytes: 0, exclusiveBytes: 0, counted: false },
    ],
    computedAt: "2026-11-13T09:00:00Z",
  };
}

/** A group of adminUsage in another state. */
export const groupState = (u: UsageAdmin, name: string, state: StorageState): UsageAdmin => ({
  ...u,
  groups: u.groups.map((g) => (g.name === name ? { ...g, state } : g)),
});

/** A member's view. */
export function memberUsage(state: UsageMember["state"] = "ok"): UsageMember {
  return { state, accounts: [{ name: "info", messages: 4000, bytes: 10 * GiB }] };
}
