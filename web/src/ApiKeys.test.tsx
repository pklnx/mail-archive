import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, type ApiKey } from "./api";
import { apiKeyDetails, apiKeyError, ApiKeyItems, ApiKeyTable, NewKeyPanel } from "./ApiKeys";
import { t } from "./i18n";

const key: ApiKey = {
  id: 1, name: "backup", hint: "abcd", scopes: ["read", "sync"], createdAt: "2026-10-01T00:00:00Z",
  expiresAt: null, lastUsedAt: null, revokedAt: null, state: "active",
};

afterEach(cleanup);

describe("NewKeyPanel", () => {
  it("shows the key once, with a copy button, and hides it when done", () => {
    const done = vi.fn();
    const secret = "ma_" + "a".repeat(52);
    render(<NewKeyPanel created={{ ...key, key: secret }} done={done} />);
    expect(screen.getByText(secret)).toBeTruthy();
    expect(screen.getByRole("status").textContent).toContain(t.apiKeyCreated("backup"));
    expect(screen.getByRole("button", { name: t.carbon.copy })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: t.done }));
    expect(done).toHaveBeenCalled();
  });
});

describe("ApiKeyTable", () => {
  it("shows scopes, state, expiry and use, and revokes only active keys after asking", () => {
    const revoke = vi.fn();
    const expired = { ...key, id: 2, name: "old", state: "expired" as const, expiresAt: "2026-01-01T00:00:00Z" };
    render(<ApiKeyTable keys={[key, expired]} revoke={revoke} />);
    const rows = screen.getAllByRole("row").slice(1);
    expect(within(rows[0]!).getByText("read, sync")).toBeTruthy();
    expect(within(rows[0]!).getByText(t.apiKeyState("active"))).toBeTruthy();
    expect(within(rows[0]!).getByText(t.apiKeyExpiryNever)).toBeTruthy();
    expect(within(rows[0]!).getByText(t.apiKeyNeverUsed)).toBeTruthy();
    expect(within(rows[1]!).getByText(t.apiKeyState("expired"))).toBeTruthy();
    expect(within(rows[1]!).queryByRole("button")).toBeNull();
    // Only the hint of a key, never more.
    expect(screen.getAllByText("ma_…abcd")).toHaveLength(2);

    fireEvent.click(within(rows[0]!).getByRole("button", { name: t.apiKeyRevoke }));
    expect(revoke).not.toHaveBeenCalled();
    const dialog = screen.getByRole("dialog", { name: t.apiKeyRevokeTitle("backup") });
    expect(dialog.closest(".cds--modal")!.className).toContain("cds--modal--danger");
    fireEvent.click(within(dialog).getByRole("button", { name: t.cancel }));
    expect(revoke).not.toHaveBeenCalled();

    fireEvent.click(within(rows[0]!).getByRole("button", { name: t.apiKeyRevoke }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: t.apiKeyRevoke }));
    expect(revoke).toHaveBeenCalledTimes(1);
    expect(revoke).toHaveBeenCalledWith(key);
  });
});

describe("ApiKeyItems", () => {
  it("offers revoking only active keys and never shows more than the hint", () => {
    const revoke = vi.fn();
    render(<ApiKeyItems keys={[key, { ...key, id: 2, name: "old", state: "revoked" }]} revoke={revoke} />);
    expect(screen.getAllByRole("button", { name: t.apiKeyRevoke })).toHaveLength(1);
    expect(screen.getAllByText(/ma_…abcd/)).toHaveLength(2);
    fireEvent.click(screen.getByRole("button", { name: t.apiKeyRevoke }));
    // Asks first, in a danger Modal; nothing is revoked yet.
    expect(revoke).not.toHaveBeenCalled();
    const dialog = screen.getByRole("dialog", { name: t.apiKeyRevokeTitle("backup") });
    expect(dialog.className).toContain("cds--modal-container");
    expect(dialog.closest(".cds--modal")!.className).toContain("cds--modal--danger");
    fireEvent.click(within(dialog).getByRole("button", { name: t.apiKeyRevoke }));
    expect(revoke).toHaveBeenCalledTimes(1);
    expect(revoke).toHaveBeenCalledWith(key);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("revokes nothing when the confirmation is cancelled", () => {
    const revoke = vi.fn();
    render(<ApiKeyItems keys={[key]} revoke={revoke} />);
    fireEvent.click(screen.getByRole("button", { name: t.apiKeyRevoke }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: t.cancel }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(revoke).not.toHaveBeenCalled();
  });
});

describe("apiKeyDetails", () => {
  it("names the state of keys that do not work", () => {
    expect(apiKeyDetails(key)).toContain("read, sync");
    expect(apiKeyDetails({ ...key, state: "expired" })).toContain(t.apiKeyState("expired"));
    expect(apiKeyDetails(key)).toContain(t.apiKeyNeverUsed);
  });
});

describe("apiKeyError", () => {
  it("maps the server's answers", () => {
    expect(apiKeyError(new ApiError(401, "the current password is wrong"), 20)).toBe(t.wrongCurrentPassword);
    expect(apiKeyError(new ApiError(401, "invalid code"), 20)).toBe(t.invalidTwoFactor);
    expect(apiKeyError(new ApiError(409, "you already have an API key with this name"), 20)).toBe(t.apiKeyNameTaken);
    expect(apiKeyError(new ApiError(409, "you already have 20 API keys; revoke one first"), 20)).toBe(t.apiKeyLimit(20));
    expect(apiKeyError(new ApiError(429, "too many", { retryAfter: 30 }), 20)).toBe(t.tooManyAttempts(30));
  });
});
