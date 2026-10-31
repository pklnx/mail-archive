import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, UNAUTHORIZED_EVENT, accountsApi, auditQuery, folderQuestion, listAccounts, listMessages, passkeyApi, profileApi, retentionApi, retryAfter, sessionApi, unknownPasskey, usersApi } from "./api";

describe("folderQuestion", () => {
  it("returns the folders the server asks about", () => {
    const err = new ApiError(409, "confirm", { error: "confirm", suggestedExclusions: ["Trash", "Spam"] });
    expect(folderQuestion(err)).toEqual(["Trash", "Spam"]);
  });
  it("ignores other errors", () => {
    expect(folderQuestion(new ApiError(409, "an account named x already exists", { error: "…" }))).toBeNull();
    expect(folderQuestion(new ApiError(422, "login failed", { suggestedExclusions: ["Trash"] }))).toBeNull();
    expect(folderQuestion(new ApiError(409, "x", { suggestedExclusions: [1] }))).toBeNull();
    expect(folderQuestion(new Error("network"))).toBeNull();
  });
});

describe("session", () => {
  const reply = (status: number, body: unknown) =>
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }),
    );
  afterEach(() => vi.restoreAllMocks());

  it("reads the logged-in user", async () => {
    reply(200, { user: { name: "patrick", admin: true } });
    expect(await sessionApi.get()).toEqual({ user: { name: "patrick", admin: true } });
  });
  it("reports a missing session and whether setup is needed", async () => {
    reply(401, { error: "login required", setupRequired: true });
    expect(await sessionApi.get()).toEqual({ user: null, setupRequired: true });
    reply(401, { error: "login required", setupRequired: false });
    expect(await sessionApi.get()).toEqual({ user: null, setupRequired: false });
  });
  it("announces a 401 from other endpoints, but not a failed login", async () => {
    const seen = vi.fn();
    window.addEventListener(UNAUTHORIZED_EVENT, seen);
    try {
      reply(401, { error: "login required" });
      await expect(listAccounts()).rejects.toThrow("login required");
      expect(seen).toHaveBeenCalledTimes(1);
      reply(401, { error: "wrong user name or password" });
      await expect(sessionApi.login("a", "b")).rejects.toBeInstanceOf(ApiError);
      expect(seen).toHaveBeenCalledTimes(1);
    } finally {
      window.removeEventListener(UNAUTHORIZED_EVENT, seen);
    }
  });
  it("reads where passkeys work", async () => {
    reply(401, { error: "login required", setupRequired: false, passkeyOrigin: "https://archive.example.test" });
    expect(await sessionApi.get()).toEqual({ user: null, setupRequired: false, passkeyOrigin: "https://archive.example.test" });
  });
  it("does not announce a failed passkey login", async () => {
    const seen = vi.fn();
    window.addEventListener(UNAUTHORIZED_EVENT, seen);
    try {
      reply(401, { error: "the passkey was not accepted" });
      await expect(passkeyApi.finishLogin("t", {})).rejects.toBeInstanceOf(ApiError);
      expect(seen).not.toHaveBeenCalled();
    } finally {
      window.removeEventListener(UNAUTHORIZED_EVENT, seen);
    }
  });
  it("recognizes an unknown passkey", () => {
    expect(unknownPasskey(new ApiError(401, "x", { unknownCredential: true, rpId: "a.test", credentialId: "AQ" }))).toEqual({ rpId: "a.test", credentialId: "AQ" });
    expect(unknownPasskey(new ApiError(401, "x", {}))).toBeNull();
  });
  it("reads the wait time after too many attempts", () => {
    expect(retryAfter(new ApiError(429, "x", { retryAfter: 120 }))).toBe(120);
    expect(retryAfter(new ApiError(429, "x", {}))).toBe(60);
    expect(retryAfter(new ApiError(401, "x", { retryAfter: 120 }))).toBeNull();
  });
});

describe("users and profile", () => {
  afterEach(() => vi.restoreAllMocks());

  it("creates a user and returns the generated password", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ name: "kim", password: "abcde-fghjk-mnpqr-stuvw" }), { status: 201 }),
    );
    expect(await usersApi.create("kim", true)).toEqual({ name: "kim", password: "abcde-fghjk-mnpqr-stuvw" });
    const [path, init] = fetch.mock.calls[0]!;
    expect(path).toBe("/api/users");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({ name: "kim", admin: true });
  });

  it("encodes user names in paths", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(null, { status: 204 }));
    await usersApi.setLocked("a.b", true);
    expect(fetch.mock.calls[0]![0]).toBe("/api/users/a.b");
    expect(JSON.parse(String(fetch.mock.calls[0]![1]?.body))).toEqual({ locked: true });
  });

  it("sends the password change", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(null, { status: 204 }));
    await profileApi.changePassword("old", "new one");
    expect(fetch.mock.calls[0]![0]).toBe("/api/profile/password");
    expect(fetch.mock.calls[0]![1]?.method).toBe("PUT");
    expect(JSON.parse(String(fetch.mock.calls[0]![1]?.body))).toEqual({ current: "old", new: "new one" });
  });

  it("asks the login gate to check again when a password change is required", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ error: "choose your own password first", passwordChangeRequired: true }), { status: 403 }),
    );
    const seen = vi.fn();
    window.addEventListener(UNAUTHORIZED_EVENT, seen);
    try {
      await expect(listAccounts()).rejects.toBeInstanceOf(ApiError);
      expect(seen).toHaveBeenCalledTimes(1);
    } finally {
      window.removeEventListener(UNAUTHORIZED_EVENT, seen);
    }
  });
});

describe("two-factor", () => {
  afterEach(() => vi.restoreAllMocks());

  it("asks the login gate to check again when 2FA setup is required", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ error: "set up two-factor authentication first", twoFactorSetupRequired: true }), { status: 403 }),
    );
    const seen = vi.fn();
    window.addEventListener(UNAUTHORIZED_EVENT, seen);
    try {
      await expect(listAccounts()).rejects.toBeInstanceOf(ApiError);
      expect(seen).toHaveBeenCalledTimes(1);
    } finally {
      window.removeEventListener(UNAUTHORIZED_EVENT, seen);
    }
  });

  it("returns the challenge of a password login with 2FA", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ twoFactorRequired: true, challenge: "abc" }), { status: 200 }));
    expect(await sessionApi.login("alice", "pw")).toEqual({ twoFactorRequired: true, challenge: "abc" });
  });
});

describe("listMessages", () => {
  afterEach(() => vi.restoreAllMocks());

  it("sends the search filters", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ messages: [], nextCursor: null }), { headers: { "Content-Type": "application/json" } }),
    );
    await listMessages(
      { q: "steuer", to: "Finanzamt Köln", from: "", hasAttachment: true, after: "2024-01-01T00:00:00+01:00" },
      null,
    );
    const url = new URL(String(fetch.mock.calls[0]?.[0]), "http://x");
    expect(Object.fromEntries(url.searchParams)).toEqual({
      q: "steuer",
      to: "Finanzamt Köln",
      has: "attachment",
      after: "2024-01-01T00:00:00+01:00",
      limit: "50",
    });
  });

  it("asks for grouped rows and for one conversation", async () => {
    const fetch = vi
      .spyOn(globalThis, "fetch")
      .mockImplementation(async () => new Response(JSON.stringify({ messages: [], nextCursor: null }), { headers: { "Content-Type": "application/json" } }));
    await listMessages({ group: true }, null);
    await listMessages({ q: "x", thread: "a@x", group: false }, null, undefined, 200);
    const params = fetch.mock.calls.map((c) => Object.fromEntries(new URL(String(c[0]), "http://x").searchParams));
    expect(params).toEqual([
      { group: "1", limit: "50" },
      { q: "x", thread: "a@x", limit: "200" },
    ]);
  });

  it("asks for messages only in the archive", async () => {
    const fetch = vi
      .spyOn(globalThis, "fetch")
      .mockImplementation(async () => new Response(JSON.stringify({ messages: [], nextCursor: null }), { headers: { "Content-Type": "application/json" } }));
    await listMessages({ account: "work", gone: true }, null);
    await listMessages({ gone: false }, null);
    const params = fetch.mock.calls.map((c) => Object.fromEntries(new URL(String(c[0]), "http://x").searchParams));
    expect(params).toEqual([{ account: "work", gone: "1", limit: "50" }, { limit: "50" }]);
  });
});

describe("write responses", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("reads a 202 without a body as null (the retention confirm answer)", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(null, { status: 202 }));
    await expect(retentionApi.confirm(1, 3)).resolves.toBeNull();
  });

  it("still returns the parsed body of a 202 with JSON", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ queued: 1 }), { status: 202 }));
    await expect(accountsApi.sync("demo")).resolves.toEqual({ queued: 1 });
  });

  it("still throws on a non-empty body that is not JSON", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("<html>", { status: 202 }));
    await expect(accountsApi.sync("demo")).rejects.toBeInstanceOf(SyntaxError);
  });
});

// The Log's From/To are local days; the request carries the instants of
// local midnight, since inclusive, until the start of the day after To (#402).
describe("auditQuery", () => {
  afterEach(() => vi.unstubAllEnvs());
  const filter = { since: "", until: "", group: "", who: "" };

  it.each([
    { tz: "Europe/Berlin", since: "2026-10-25", until: "2026-10-25", want: { since: "2026-10-24T22:00:00.000Z", until: "2026-10-25T23:00:00.000Z" }, day: "25 h" },
    { tz: "Europe/Berlin", since: "2026-03-29", until: "2026-03-29", want: { since: "2026-03-28T23:00:00.000Z", until: "2026-03-29T22:00:00.000Z" }, day: "23 h" },
    { tz: "Europe/Berlin", since: "", until: "2026-12-31", want: { until: "2026-12-31T23:00:00.000Z" }, day: "year end" },
    { tz: "America/Los_Angeles", since: "2026-10-20", until: "", want: { since: "2026-10-20T07:00:00.000Z" }, day: "west of UTC" },
    { tz: "America/Santiago", since: "2026-09-06", until: "", want: { since: "2026-09-06T04:00:00.000Z" }, day: "no midnight" },
    { tz: "Europe/Berlin", since: "", until: "", want: {}, day: "no days" },
  ])("$tz $since – $until ($day)", ({ tz, since, until, want }) => {
    vi.stubEnv("TZ", tz);
    const p = new URLSearchParams(auditQuery({ ...filter, since, until }));
    expect(Object.fromEntries(p)).toEqual(want);
  });
});
