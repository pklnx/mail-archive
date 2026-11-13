// The Users page's requests (moved from api.test.ts with them, #470 part 2)
// and the customer group requests.
import { afterEach, describe, expect, it, vi } from "vitest";
import { groupsApi, userAdminApi } from "./userAdminApi";

afterEach(() => vi.restoreAllMocks());

const respond = (body: unknown, status = 200) =>
  vi.spyOn(globalThis, "fetch").mockResolvedValue(body === null ? new Response(null, { status }) : new Response(JSON.stringify(body), { status }));
const sent = (fetch: ReturnType<typeof respond>, n = 0) => {
  const [path, init] = fetch.mock.calls[n]!;
  return { path, method: init?.method ?? "GET", body: init?.body === undefined ? undefined : JSON.parse(String(init.body)) };
};

describe("user admin requests", () => {
  it("creates a user and returns the generated password", async () => {
    const fetch = respond({ name: "kim", password: "abcde-fghjk-mnpqr-stuvw" }, 201);
    expect(await userAdminApi.create("kim", true)).toEqual({ name: "kim", password: "abcde-fghjk-mnpqr-stuvw" });
    expect(sent(fetch)).toEqual({ path: "/api/users", method: "POST", body: { name: "kim", admin: true } });
  });

  it("creates a user in a customer group (#470)", async () => {
    const fetch = respond({ name: "kim", password: "abcde-fghjk-mnpqr-stuvw" }, 201);
    await userAdminApi.create("kim", false, 7);
    expect(sent(fetch).body).toEqual({ name: "kim", admin: false, group: 7 });
  });

  it("encodes user names in paths", async () => {
    const fetch = respond({ smtpCredentialsActive: 0 });
    await userAdminApi.setLocked("a.b", true);
    expect(sent(fetch)).toEqual({ path: "/api/users/a.b", method: "PATCH", body: { locked: true } });
  });

  it("locks a user and resolves to the SMTP secrets that stay usable (#395)", async () => {
    const fetch = respond({ smtpCredentialsActive: 2 });
    expect(await userAdminApi.setLocked("jo", true)).toBe(2);
    expect(sent(fetch)).toEqual({ path: "/api/users/jo", method: "PATCH", body: { locked: true } });
    respond({ smtpCredentialsActive: 0 });
    expect(await userAdminApi.setLocked("jo", false)).toBe(0);
  });

  it("moves a user into a group and resolves to the numbers of ended shares and auditor grants", async () => {
    const fetch = respond({ sharesEnded: 2, grantsRevoked: 1 });
    expect(await userAdminApi.setGroup("jo", 3)).toEqual({ sharesEnded: 2, grantsRevoked: 1 });
    expect(sent(fetch)).toEqual({ path: "/api/users/jo", method: "PATCH", body: { group: 3 } });
  });
});

describe("group requests", () => {
  it("lists the groups", async () => {
    const g = { id: 1, name: "Default", users: 2, accounts: 1, createdAt: "2026-10-01T00:00:00Z" };
    const fetch = respond({ groups: [g] });
    expect(await groupsApi.list()).toEqual([g]);
    expect(sent(fetch).path).toBe("/api/groups");
  });

  it("adds, renames and removes a group", async () => {
    const fetch = respond({ id: 4, name: "Meier", users: 0, accounts: 0, createdAt: "2026-10-01T00:00:00Z" }, 201);
    expect((await groupsApi.create("Meier")).id).toBe(4);
    expect(sent(fetch)).toEqual({ path: "/api/groups", method: "POST", body: { name: "Meier" } });
    fetch.mockResolvedValue(new Response(null, { status: 204 }));
    await groupsApi.rename(4, "Meier & Co");
    expect(sent(fetch, 1)).toEqual({ path: "/api/groups/4", method: "PATCH", body: { name: "Meier & Co" } });
    fetch.mockResolvedValue(new Response(null, { status: 204 }));
    await groupsApi.remove(4);
    expect(sent(fetch, 2)).toEqual({ path: "/api/groups/4", method: "DELETE", body: undefined });
  });

  it("throws the server's answer of a refused change", async () => {
    respond({ error: "a group named \"B\" already exists" }, 409);
    await expect(groupsApi.create("b")).rejects.toMatchObject({ status: 409, message: 'a group named "B" already exists' });
  });
});
