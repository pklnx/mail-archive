import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, UNAUTHORIZED_EVENT, folderQuestion, listAccounts, profileApi, retryAfter, sessionApi, usersApi } from "./api";

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
