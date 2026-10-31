import { describe, expect, it } from "vitest";
import type { AccountsResponse } from "./api";
import { messagesFor, t } from "./i18n";
import { accountLabel, labelOfKey, mailAccounts } from "./mailAccounts";

// The mail view's accounts (#389): own ones by name, then shared ones by
// owner/name, so a shared "work" never stands for an own "work".
const data = {
  accounts: [{ name: "work", kind: "imap", enabled: true, removed: false, folders: [], sync: { state: "running", health: "failing" } }],
  shared: [{ key: "anna/work", name: "work", owner: "anna", kind: "import", enabled: false, removed: true, folders: [] }],
} as unknown as AccountsResponse;

describe("mailAccounts", () => {
  it("puts the own accounts first and keys the shared ones by owner/name", () => {
    const list = mailAccounts(data);
    expect(list.map((a) => [a.key, a.owner, a.syncState, a.health])).toEqual([
      ["work", null, "running", "failing"],
      // A shared account's sync is its owner's: never shown as running or failing.
      ["anna/work", "anna", "idle", "ok"],
    ]);
    expect(list[1]).toMatchObject({ kind: "import", enabled: false, removed: true });
  });

  it("labels an account= value as the user reads it", () => {
    expect(labelOfKey(data, "work")).toBe("work");
    expect(labelOfKey(data, "anna/work")).toBe(t.sharedName("work", "anna"));
    expect(t.sharedName("work", "anna")).toBe("work · anna");
    // Before the accounts load, or for a key that names none: the key itself.
    expect(labelOfKey(null, "anna/work")).toBe("anna/work");
    expect(labelOfKey(data, "ben/x")).toBe("ben/x");
    expect(accountLabel({ name: "x", owner: null })).toBe("x");
    expect(messagesFor("de").sharedLocation("work", "anna")).toBe("work (anna)");
  });
});
