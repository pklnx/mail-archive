// SMTP secrets in the web UI (#395 part 3): the section on an smtp
// account's page (list, a new secret with the password, shown once, revoke,
// the grace choice), the mail server setup with a login, the admins' list
// on the Users page and the hint after a lock.
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Account, AccountsResponse, ManagedUser } from "./api";
import { AccountsPage } from "./AccountsPage";
import { shortDate } from "./format";
import { t } from "./i18n";
import type { AdminSMTPCredential, SMTPCredential } from "./smtpApi";
import { smtpText } from "./smtpText";
import { UsersPage } from "./UsersPage";

/** A secret of the SMTP format, made at run time so that no literal looks like one. */
function madeUpSecret(): string {
  const alphabet = "abcdefghijklmnopqrstuvwxyz234567";
  return "mas_" + Array.from(crypto.getRandomValues(new Uint8Array(52)), (b) => alphabet[b % 32]).join("");
}

function smtpAccount(more: Partial<Account> = {}): Account {
  return {
    name: "journal", kind: "smtp", enabled: true, removed: false, purging: false, purge: null, host: "", port: 0, tls: "tls", username: "",
    includedFolders: [], excludedFolders: [], syncInterval: "default", folders: [], goneMessages: 0, lastReconciledAt: null,
    addresses: ["archive@archive.local"], lastReceivedAt: null, fill: null, fills: [], firstReceived: null, sharedWith: [],
    sync: { state: "idle", lastRun: null, failureStreak: 0, failingSince: null, health: "ok", syncEvery: null, nextAt: null },
    ...more,
  };
}

const data = (smtp: AccountsResponse["smtp"], accounts = [smtpAccount()]): AccountsResponse => ({
  accounts, manage: true, shared: [], syncInterval: "6h0m0s", syncIntervalMin: "5m0s", reconcileInterval: "", alertAfter: 3, smtp,
});

function credential(id: number, hint: string, more: Partial<SMTPCredential> = {}): SMTPCredential {
  return { id, hint, createdAt: "2026-10-20T08:00:00Z", expiresAt: null, lastUsedAt: null, revokedAt: null, state: "active", ...more };
}

let requests: { method: string; url: string; body: unknown }[];
/** The account's secrets that GET answers. */
let listed: SMTPCredential[];
/** The admins' list. */
let all: AdminSMTPCredential[];
let twoFactor: boolean;
let secret: string;
/** How many usable secrets a lock answers with. */
let lockCount: number;
let createAnswer: () => Response;
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
const changes = () => requests.filter((r) => r.method !== "GET");

beforeEach(() => {
  requests = [];
  listed = [credential(3, "abcd")];
  all = [];
  twoFactor = false;
  secret = madeUpSecret();
  lockCount = 0;
  createAnswer = () =>
    json(
      {
        username: "smtp-7", secret, credential: credential(4, secret.slice(-4)),
        previous: [{ id: 3, expiresAt: "2026-10-30T08:00:00Z" }],
      },
      201,
    );
  vi.spyOn(globalThis, "fetch").mockImplementation(async (url, init) => {
    const method = init?.method ?? "GET";
    const u = String(url);
    requests.push({ method, url: u, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    if (method === "GET" && u === "/api/accounts/journal/smtp-credentials") return json({ username: "smtp-7", credentials: listed });
    if (method === "POST" && u === "/api/accounts/journal/smtp-credentials") return createAnswer();
    if (method === "GET" && u === "/api/profile/2fa") return json({ enabled: twoFactor });
    if (method === "GET" && u === "/api/admin/smtp-credentials") return json({ credentials: all });
    if (method === "GET" && u === "/api/users") return json({ users });
    if (method === "GET" && u === "/api/groups") return json({ groups: [] });
    if (method === "GET" && u === "/api/api-keys") return json({ keys: [] });
    if (method === "PATCH" && u.startsWith("/api/users/")) return json({ smtpCredentialsActive: lockCount });
    return new Response(null, { status: 204 });
  });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  window.history.replaceState(null, "", "/");
  localStorage.clear();
  sessionStorage.clear();
});

function accountPage(smtp: AccountsResponse["smtp"] = { auth: "required" }, admin = true) {
  window.history.replaceState(null, "", "/?view=accounts&id=journal");
  render(<AccountsPage accounts={{ data: data(smtp), error: "", reload: () => {} }} admin={admin} renamed={() => {}} showGone={() => {}} showAccount={() => {}} />);
}

const region = (name: string) => within(screen.getByRole("region", { name }));
const dialog = () => within(screen.getByRole("dialog"));

describe("the SMTP secrets of an smtp account", () => {
  it("lists the user name and the secrets, with Revoke only for those that still work", async () => {
    const st = smtpText();
    listed = [
      credential(5, "efgh"),
      credential(4, "abcd", { state: "expiring", expiresAt: "2026-10-27T08:00:00Z", lastUsedAt: "2026-10-21T08:00:00Z" }),
      credential(3, "wxyz", { state: "revoked", revokedAt: "2026-10-19T08:00:00Z" }),
      credential(2, "mnop", { state: "expired", expiresAt: "2026-10-01T08:00:00Z" }),
    ];
    accountPage();
    const section = region(st.title);
    expect(await section.findByText("smtp-7")).toBeTruthy();
    const rows = section.getAllByRole("row").slice(1);
    expect(rows.map((r) => within(r).getAllByRole("cell")[0]!.textContent)).toEqual(["mas_…efgh", "mas_…abcd", "mas_…wxyz", "mas_…mnop"]);
    expect(rows.map((r) => within(r).getAllByRole("cell")[1]!.textContent)).toEqual(["active", "expiring", "revoked", "expired"].map(st.state));
    expect(within(rows[0]!).getByText(st.noEnd)).toBeTruthy();
    expect(section.getAllByRole("button", { name: /^Revoke mas_/ }).map((b) => b.getAttribute("aria-label"))).toEqual([st.revokeLabel("efgh"), st.revokeLabel("abcd")]);
    // The setup's password map line names the account's user name.
    expect(region(t.mailServerSetup).getByText(/\[archive\.customer\.example\]:587\s+smtp-7:mas_…/)).toBeTruthy();
  });

  it("says when the answer has no list, and keeps the page and its setup", async () => {
    // JSON drops an undefined field: the server answers {"username": "smtp-7"}.
    listed = undefined as unknown as SMTPCredential[];
    accountPage();
    expect(await region(smtpText().title).findByText(/no list of SMTP secrets/)).toBeTruthy();
    expect(screen.getByRole("heading", { level: 1, name: "journal" })).toBeTruthy();
    expect(region(t.mailServerSetup).getByText(/smtp-…:mas_…/)).toBeTruthy();
  });

  it("is only on an admin's page", async () => {
    accountPage({ auth: "required" }, false);
    expect(screen.queryByRole("region", { name: smtpText().title })).toBeNull();
    // The setup still logs in, with placeholders.
    expect(region(t.mailServerSetup).getByText(/smtp-…:mas_…/)).toBeTruthy();
    expect(requests.filter((r) => r.url.includes("smtp-credentials"))).toEqual([]);
  });

  it("creates a secret with the password and shows it once, only in the dialog", async () => {
    const st = smtpText();
    accountPage();
    const section = region(st.title);
    await section.findByText("smtp-7");
    fireEvent.click(section.getByRole("button", { name: st.newSecret }));
    expect(dialog().getByText(st.newSecretTitle("journal"))).toBeTruthy();
    // The account has a current secret: how long it stays valid, 7 days by default.
    const grace = dialog().getByLabelText(st.grace) as HTMLSelectElement;
    expect(grace.value).toBe("7");
    expect([...grace.options].map((o) => o.value)).toEqual(["0", "1", "7", "14", "30"]);
    fireEvent.change(dialog().getByLabelText(t.currentPassword), { target: { value: "pw-of-the-admin" } });
    fireEvent.click(dialog().getByRole("button", { name: st.create }));

    await waitFor(() => expect(dialog().getByText(st.created)).toBeTruthy());
    expect(changes()).toEqual([
      { method: "POST", url: "/api/accounts/journal/smtp-credentials", body: { currentPassword: "pw-of-the-admin", code: "", graceDays: 7 } },
    ]);
    expect(dialog().getByText(secret)).toBeTruthy();
    expect(dialog().getByText(new RegExp(`^\\[archive\\.customer\\.example\\]:587\\s+smtp-7:${secret}$`))).toBeTruthy();
    expect(dialog().getByText(st.previousExpires(shortDate("2026-10-30T08:00:00Z")))).toBeTruthy();
    // The list reloads behind the dialog.
    await waitFor(() => expect(requests.filter((r) => r.method === "GET" && r.url.endsWith("/smtp-credentials"))).toHaveLength(2));
    // Neither the page nor the URL nor the storage have the secret.
    const outside = [...document.body.children].filter((el) => !el.querySelector("[role=dialog]"));
    expect(outside.map((el) => el.textContent).join("")).not.toContain(secret);
    expect(window.location.href).not.toContain(secret);

    fireEvent.click(dialog().getByRole("button", { name: t.done }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(document.body.textContent).not.toContain(secret);
    expect(document.body.innerHTML).not.toContain(secret);
    const stored = [localStorage, sessionStorage].flatMap((s) => Object.keys(s).map((k) => s.getItem(k) ?? ""));
    expect(stored.join("")).not.toContain(secret);
    expect(window.location.href).not.toContain(secret);
    // Opening the dialog again starts empty.
    fireEvent.click(section.getByRole("button", { name: st.newSecret }));
    expect(screen.getByRole("dialog").textContent).not.toContain(secret);
  });

  it("drops the secret when the dialog closes with Escape", async () => {
    const st = smtpText();
    accountPage();
    await region(st.title).findByText("smtp-7");
    fireEvent.click(region(st.title).getByRole("button", { name: st.newSecret }));
    fireEvent.change(dialog().getByLabelText(t.currentPassword), { target: { value: "pw-of-the-admin" } });
    fireEvent.click(dialog().getByRole("button", { name: st.create }));
    await waitFor(() => expect(dialog().getByText(secret)).toBeTruthy());
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(document.body.innerHTML).not.toContain(secret);
  });

  it("sends the grace choice, and asks for a code with 2FA", async () => {
    const st = smtpText();
    twoFactor = true;
    accountPage();
    await region(st.title).findByText("smtp-7");
    fireEvent.click(region(st.title).getByRole("button", { name: st.newSecret }));
    fireEvent.change(dialog().getByLabelText(st.grace), { target: { value: "0" } });
    expect(dialog().getByRole("option", { name: st.graceDays(0) })).toBeTruthy();
    fireEvent.change(dialog().getByLabelText(t.currentPassword), { target: { value: "pw-of-the-admin" } });
    fireEvent.change(await dialog().findByLabelText(t.twoFactorCode), { target: { value: "123456" } });
    fireEvent.click(dialog().getByRole("button", { name: st.create }));
    await waitFor(() => expect(changes()).toHaveLength(1));
    expect(changes()[0]!.body).toEqual({ currentPassword: "pw-of-the-admin", code: "123456", graceDays: 0 });
  });

  it("offers no grace without a current secret", async () => {
    const st = smtpText();
    listed = [];
    accountPage();
    expect(await region(st.title).findByText(st.none)).toBeTruthy();
    fireEvent.click(region(st.title).getByRole("button", { name: st.newSecret }));
    expect(dialog().queryByLabelText(st.grace)).toBeNull();
  });

  it("says a wrong password and keeps the form, without the password", async () => {
    const st = smtpText();
    createAnswer = () => json({ error: "current password is wrong" }, 401);
    accountPage();
    await region(st.title).findByText("smtp-7");
    fireEvent.click(region(st.title).getByRole("button", { name: st.newSecret }));
    fireEvent.change(dialog().getByLabelText(t.currentPassword), { target: { value: "not-it" } });
    fireEvent.click(dialog().getByRole("button", { name: st.create }));
    expect(await dialog().findByText(t.wrongCurrentPassword)).toBeTruthy();
    expect((dialog().getByLabelText(t.currentPassword) as HTMLInputElement).value).toBe("");
    expect(dialog().queryByText(st.created)).toBeNull();
  });

  it("revokes a secret after asking", async () => {
    const st = smtpText();
    accountPage();
    const section = region(st.title);
    await section.findByText("smtp-7");
    fireEvent.click(section.getByRole("button", { name: st.revokeLabel("abcd") }));
    expect(dialog().getByText(st.revokeTitle("abcd"))).toBeTruthy();
    expect(changes()).toEqual([]);
    fireEvent.click(dialog().getByRole("button", { name: st.revoke }));
    await waitFor(() => expect(changes()).toEqual([{ method: "DELETE", url: "/api/accounts/journal/smtp-credentials/3", body: undefined }]));
    expect(await section.findByText(st.revoked("abcd"))).toBeTruthy();
    expect(document.activeElement).toBe(screen.getByRole("heading", { level: 2, name: st.title }));
  });

  it("shows the lines without a login when the server runs with off", async () => {
    const st = smtpText();
    accountPage({ auth: "off" });
    expect(await region(st.title).findByText(st.authOff)).toBeTruthy();
    const setup = region(t.mailServerSetup);
    expect(setup.getByText(st.setupIntro)).toBeTruthy();
    expect(setup.getByText(/archive\.local\s+smtp:\[127\.0\.0\.1\]:2525/)).toBeTruthy();
    expect(setup.queryByText(/archive_sasl/)).toBeNull();
  });
});

function user(name: string, more: Partial<ManagedUser> = {}): ManagedUser {
  return {
    name, admin: false, locked: false, mustChangePassword: false, twoFactorEnabled: false, passkeys: 0, apiKeys: 0, accounts: 1,
    failingAccounts: 0, createdAt: "2026-10-01T00:00:00Z", lastLoginAt: null, self: false, group: { id: 1, name: "Default" }, ...more,
  };
}
const users = [user("alice", { admin: true, self: true }), user("bob")];

function admin(id: number, hint: string, more: Partial<AdminSMTPCredential> = {}): AdminSMTPCredential {
  return { ...credential(id, hint), account: "journal", username: "smtp-7", owner: "bob", accountRemoved: false, ...more };
}

describe("the admins' list of SMTP secrets", () => {
  function usersPage(path = "/?view=users") {
    window.history.replaceState(null, "", path);
    render(<UsersPage />);
  }

  it("lists every smtp account's secrets and revokes any of them", async () => {
    const st = smtpText();
    all = [admin(3, "abcd"), admin(8, "qrst", { account: "legal", username: "smtp-9", owner: "", state: "revoked", revokedAt: "2026-10-19T08:00:00Z" })];
    usersPage();
    const section = within(await screen.findByRole("region", { name: st.title }));
    const rows = section.getAllByRole("row").slice(1);
    expect(within(rows[0]!).getByRole("rowheader").textContent).toContain("journal");
    expect(within(rows[0]!).getAllByRole("cell").map((c) => c.textContent)).toContain("bob");
    expect(within(rows[1]!).getAllByRole("cell").map((c) => c.textContent)).toContain(st.noOwner);
    expect(section.getAllByRole("button", { name: /^Revoke mas_/ })).toHaveLength(1);
    fireEvent.click(section.getByRole("button", { name: st.revokeLabel("abcd") }));
    fireEvent.click(dialog().getByRole("button", { name: st.revoke }));
    await waitFor(() => expect(changes()).toEqual([{ method: "DELETE", url: "/api/admin/smtp-credentials/3", body: undefined }]));
    expect(await section.findByText(st.revoked("abcd"))).toBeTruthy();
  });

  it("is not there without secrets", async () => {
    usersPage();
    await screen.findByRole("rowheader", { name: /bob/ });
    expect(screen.queryByRole("region", { name: smtpText().title })).toBeNull();
  });

  it("leaves the list out, and keeps the users, when the answer has no list", async () => {
    // JSON drops an undefined field: the server answers {}. The page once
    // read its length and crashed as a whole (CI on #555).
    all = undefined as unknown as AdminSMTPCredential[];
    usersPage();
    expect(await screen.findByRole("button", { name: t.userActions("bob") })).toBeTruthy();
    await waitFor(() => expect(requests.some((r) => r.url === "/api/admin/smtp-credentials")).toBe(true));
    expect(screen.queryByRole("region", { name: smtpText().title })).toBeNull();
  });

  it("names the secrets that still work after a lock, with a link to the list", async () => {
    const st = smtpText();
    all = [admin(3, "abcd"), admin(4, "efgh")];
    lockCount = 2;
    usersPage("/?view=users&id=bob");
    await screen.findByRole("heading", { level: 1, name: "bob" });
    fireEvent.click(screen.getByRole("button", { name: t.userActions("bob") }));
    fireEvent.click(screen.getByRole("menuitem", { name: t.lock }));
    fireEvent.click(dialog().getByRole("button", { name: t.lock }));
    expect(await screen.findByText(st.lockHint("bob", 2))).toBeTruthy();
    fireEvent.click(screen.getByRole("link", { name: st.lockHintLink }));
    // The list replaces the user's page, and its SMTP secrets get the focus.
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("heading", { level: 2, name: st.title })));
    expect(window.location.search).toBe("?view=users");
  });

  it("says nothing more after a lock when no secret works", async () => {
    const st = smtpText();
    usersPage();
    await screen.findByRole("rowheader", { name: /bob/ });
    fireEvent.click(screen.getByRole("button", { name: t.userActions("bob") }));
    fireEvent.click(screen.getByRole("menuitem", { name: t.lock }));
    fireEvent.click(dialog().getByRole("button", { name: t.lock }));
    await waitFor(() => expect(changes()).toEqual([{ method: "PATCH", url: "/api/users/bob", body: { locked: true } }]));
    await waitFor(() => expect(requests.filter((r) => r.url === "/api/users")).toHaveLength(2));
    expect(screen.queryByText(st.lockHint("bob", 0))).toBeNull();
    expect(screen.queryByRole("link", { name: st.lockHintLink })).toBeNull();
  });
});
