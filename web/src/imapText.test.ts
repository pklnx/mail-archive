import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, accountsApi } from "./api";
import { applySettings } from "./i18n";
import { errorMessage, imapCheckText, imapCodes, imapTextFor } from "./imapText";

describe("IMAP login check texts", () => {
  const reply = (status: number, body: unknown) =>
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }),
    );
  afterEach(() => {
    vi.restoreAllMocks();
    applySettings({ language: "en", locale: "en" });
  });

  it("has a text for every category, in English and German", () => {
    for (const lang of ["en", "de"] as const) {
      const texts = imapCodes.map((c) => imapTextFor(lang)[c]);
      expect(texts.every((s) => s.length > 0)).toBe(true);
      expect(new Set(texts).size).toBe(imapCodes.length);
    }
    expect(imapTextFor("de").imap_login).not.toBe(imapTextFor("en").imap_login);
  });

  it("shows the text of the code, never the server's", async () => {
    for (const code of imapCodes) {
      reply(422, { error: "the IMAP server failed SERVER-TEXT", code });
      const err = await accountsApi.create({ name: "x", host: "h", port: 993, tls: "tls", username: "u", password: "p" }).catch((e: unknown) => e);
      expect(err).toBeInstanceOf(ApiError);
      expect(errorMessage(err)).toBe(imapTextFor("en")[code]);
      expect(errorMessage(err)).not.toContain("SERVER-TEXT");
    }
  });

  it("follows the UI language", () => {
    applySettings({ language: "de", locale: "de" });
    expect(imapCheckText(new ApiError(422, "x", { code: "imap_tls" }))).toBe(imapTextFor("de").imap_tls);
    expect(imapCheckText(new ApiError(429, "x", { code: "too_many_checks", retryAfter: 15 }))).toBe(imapTextFor("de").tooManyChecks(15));
  });

  it("says how long a refused check waits", async () => {
    reply(429, { error: "too many server checks; try again later", code: "too_many_checks", retryAfter: 15 });
    const err = await accountsApi.fillFolders("j", { host: "h", port: 993, tls: "tls", username: "u", password: "p" }).catch((e: unknown) => e);
    expect(errorMessage(err)).toBe(imapTextFor("en").tooManyChecks(15));
    expect(imapTextFor("en").tooManyChecks(15)).toContain("15 seconds");
    expect(imapTextFor("en").tooManyChecks(600)).toContain("10 minutes");
  });

  it("keeps the server's text for other errors", () => {
    expect(errorMessage(new ApiError(409, "already exists", { error: "already exists", code: "conflict" }))).toBe("already exists");
    expect(errorMessage(new ApiError(422, "x", { code: "toString" }))).toBe("x");
    expect(errorMessage(new ApiError(400, "bad", { code: "too_many_checks" }))).toBe("bad");
    expect(errorMessage(new Error("network"))).toBe("network");
    expect(errorMessage("odd")).toBe("odd");
  });
});
