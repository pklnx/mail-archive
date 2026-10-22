import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Account, AccountsResponse, Fill } from "./api";
import { AccountsPage } from "./AccountsPage";
import { fill } from "./fillFixture";
import { t } from "./i18n";

function smtpAccount(f: Fill | null, more: Partial<Account> = {}): Account {
  return {
    name: "journal", kind: "smtp", enabled: true, removed: false, purging: false, host: "", port: 0, tls: "tls", username: "",
    includedFolders: [], excludedFolders: [], syncInterval: "default", folders: [], goneMessages: 0, lastReconciledAt: null,
    addresses: ["archive@archive.local"], lastReceivedAt: null, fill: f, fills: f ? [f] : [], firstReceived: null,
    sync: { state: f?.status === "running" ? "running" : "idle", lastRun: null, failureStreak: 0, failingSince: null, health: "ok", syncEvery: null, nextAt: null },
    ...more,
  };
}

const data = (accounts: Account[]): AccountsResponse => ({
  accounts, manage: true, syncInterval: "6h0m0s", syncIntervalMin: "5m0s", reconcileInterval: "", alertAfter: 3, smtp: {},
});

let requests: { method: string; url: string; body: unknown }[];
let answer: (url: string) => Response;

beforeEach(() => {
  requests = [];
  answer = () => new Response(null, { status: 204 });
  vi.spyOn(globalThis, "fetch").mockImplementation(async (url, init) => {
    requests.push({ method: init?.method ?? "GET", url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined });
    return answer(String(url));
  });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function page(accounts: Account[]) {
  const reload = vi.fn();
  render(<AccountsPage accounts={{ data: data(accounts), error: "", reload }} admin={false} close={() => {}} renamed={() => {}} showGone={() => {}} />);
  return reload;
}

describe("the fill on the smtp card", () => {
  it("shows a running fill and cancels it", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const reload = page([smtpAccount(fill())]);
    expect(screen.getByText(t.fillRunning(15, 12, 1, 2))).toBeTruthy();
    expect(screen.getByText(t.fillSource("alice@example.com", "imap.example.com", "JOURNAL"))).toBeTruthy();
    expect(screen.getByText(/^Sent/)).toBeTruthy();
    expect((screen.getByRole("button", { name: t.importFromIMAP }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: t.remove }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: t.cancelFill }));
    await waitFor(() => expect(reload).toHaveBeenCalled());
    expect(requests).toEqual([{ method: "POST", url: "/api/accounts/journal/fills/3/cancel", body: undefined }]);
  });

  it("shows the folders of an older fill with a prefix", () => {
    const folders = fill().folders.map((ff) => ({ ...ff, folder: `alice/${ff.source}` }));
    page([smtpAccount(fill({ status: "ok", finishedAt: "2026-10-13T09:00:00Z", folderPrefix: "alice", folders }))]);
    expect(screen.getByText(t.fillSource("alice@example.com", "imap.example.com", "alice/…"))).toBeTruthy();
    expect(screen.getByText(/^alice\/Sent/)).toBeTruthy();
  });

  it("resumes a failed fill with the password", async () => {
    answer = () => new Response(JSON.stringify({ id: 3 }), { status: 202 });
    page([smtpAccount(fill({ status: "failed", error: "connection reset", finishedAt: "2026-10-13T09:00:00Z" }))]);
    expect(screen.getByText("connection reset")).toBeTruthy();
    expect(screen.queryByRole("button", { name: t.cancelFill })).toBeNull();
    fireEvent.change(screen.getByLabelText(`${t.fieldPassword} (alice@example.com)`), { target: { value: "secret" } });
    fireEvent.click(screen.getByRole("button", { name: t.resumeFill }));
    await waitFor(() => expect(requests).toHaveLength(1));
    expect(requests[0]).toEqual({ method: "POST", url: "/api/accounts/journal/fills/3/resume", body: { password: "secret" } });
  });

  it("starts a fill after the folder list, with trash and spam unticked", async () => {
    answer = (url) =>
      url.endsWith("/fills/folders")
        ? new Response(
            JSON.stringify({
              folders: [{ name: "INBOX", selected: true }, { name: "Junk", specialUse: "\\Junk", selected: true }, { name: "Sent", selected: true }],
              suggestedExclusions: ["Junk"],
            }),
            { status: 200 },
          )
        : new Response(JSON.stringify({ id: 4 }), { status: 202 });
    const reload = page([smtpAccount(null, { firstReceived: { at: "2026-10-01T08:00:00Z", subject: "Hello" } })]);
    fireEvent.click(screen.getByRole("button", { name: t.importFromIMAP }));
    expect(screen.getByText(/Hello/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText(t.fieldHost), { target: { value: "imap.example.com" } });
    fireEvent.change(screen.getByLabelText(t.fieldUsername), { target: { value: "alice@example.com" } });
    fireEvent.change(screen.getByLabelText(t.fieldPassword), { target: { value: "secret" } });
    expect(screen.queryByLabelText(/prefix/i)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: t.loadFillFolders }));
    expect(await screen.findByText(t.fillTrashSpam("Junk"))).toBeTruthy();
    expect((screen.getByRole("checkbox", { name: /Junk/ }) as HTMLInputElement).checked).toBe(false);
    fireEvent.click(screen.getByRole("checkbox", { name: "Sent" }));
    fireEvent.change(screen.getByLabelText(t.fieldCutoff), { target: { value: "2026-10-01T08:30" } });
    expect(screen.getByText(t.cutoffGap)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: t.startFill }));
    await waitFor(() => expect(reload).toHaveBeenCalled());
    expect(requests[0]).toEqual({
      method: "POST", url: "/api/accounts/journal/fills/folders",
      body: { host: "imap.example.com", tls: "tls", port: 993, username: "alice@example.com", password: "secret" },
    });
    const start = requests[1]!.body as Record<string, unknown>;
    expect(requests[1]!.url).toBe("/api/accounts/journal/fills");
    expect(start).toMatchObject({ host: "imap.example.com", username: "alice@example.com", password: "secret", excludedFolders: ["Junk", "Sent"], confirmFolders: true });
    expect(start.folderPrefix).toBeUndefined();
    expect(Date.parse(start.before as string)).toBe(new Date("2026-10-01T08:30").getTime());
  });

  it("loads the folders again when the login changes", async () => {
    answer = () => new Response(JSON.stringify({ folders: [{ name: "INBOX", selected: true }], suggestedExclusions: [] }), { status: 200 });
    page([smtpAccount(null)]);
    fireEvent.click(screen.getByRole("button", { name: t.importFromIMAP }));
    expect(screen.getByText(t.noneReceived)).toBeTruthy();
    fireEvent.change(screen.getByLabelText(t.fieldHost), { target: { value: "imap.example.com" } });
    fireEvent.change(screen.getByLabelText(t.fieldUsername), { target: { value: "alice" } });
    fireEvent.change(screen.getByLabelText(t.fieldPassword), { target: { value: "secret" } });
    fireEvent.click(screen.getByRole("button", { name: t.loadFillFolders }));
    expect(await screen.findByRole("button", { name: t.startFill })).toBeTruthy();
    fireEvent.change(screen.getByLabelText(t.fieldPassword), { target: { value: "other" } });
    expect(screen.getByRole("button", { name: t.loadFillFolders })).toBeTruthy();
  });
});

describe("recurring fills on the smtp card", () => {
  const recurring = (more: Partial<Fill> = {}) =>
    fill({
      id: 5, status: "ok", keepRunning: true, recurringSince: "2026-10-01T08:00:00Z", runStartedAt: "2026-10-13T07:55:00Z",
      finishedAt: "2026-10-13T08:00:00Z", before: null, interval: "default", every: "6h0m0s", nextAt: "2026-10-13T13:55:00Z",
      health: { state: "ok", failureStreak: 0, failingSince: null, lastSuccessAt: "2026-10-13T08:00:00Z" }, ...more,
    });

  it("shows every fill, stops a recurring one and changes its interval", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const once = fill({ status: "ok", finishedAt: "2026-10-12T09:00:00Z", username: "bob@example.com" });
    const reload = page([smtpAccount(once, { fills: [once, recurring({ unconfirmedFolders: ["Deleted"] })] })]);
    expect(screen.getByText(t.fillSource("bob@example.com", "imap.example.com", "JOURNAL"))).toBeTruthy();
    expect(screen.getByText(/^Imports new mail every 6 hours/)).toBeTruthy();
    expect(screen.getByText(t.fillUnconfirmed("Deleted"))).toBeTruthy();
    // Another mailbox can be added while no fill runs.
    expect((screen.getByRole("button", { name: t.importFromIMAP }) as HTMLButtonElement).disabled).toBe(false);
    fireEvent.change(screen.getByLabelText(`${t.fillInterval} (alice@example.com)`), { target: { value: "12h0m0s" } });
    fireEvent.click(screen.getByRole("button", { name: t.saveFillInterval }));
    await waitFor(() => expect(requests).toHaveLength(1));
    expect(requests[0]).toEqual({ method: "PATCH", url: "/api/accounts/journal/fills/5", body: { interval: "12h0m0s" } });
    fireEvent.click(screen.getByRole("button", { name: t.stopFill }));
    await waitFor(() => expect(requests).toHaveLength(2));
    expect(requests[1]).toEqual({ method: "POST", url: "/api/accounts/journal/fills/5/cancel", body: undefined });
    expect(reload).toHaveBeenCalled();
  });

  it("shows the health of a failing fill", () => {
    const f = recurring({ status: "failed", error: "login failed", health: { state: "failing", failureStreak: 4, failingSince: null, lastSuccessAt: null } });
    page([smtpAccount(f)]);
    expect(screen.getByText(t.fillFailing(4))).toBeTruthy();
    expect(screen.getByText("login failed")).toBeTruthy();
    // It is stopped first, then resumed with a new password.
    expect(screen.queryByRole("button", { name: t.resumeFill })).toBeNull();
  });

  it("switches an ended one-time fill on with the password, after asking about trash and spam", async () => {
    let calls = 0;
    answer = () =>
      ++calls === 1
        ? new Response(JSON.stringify({ error: "confirm", suggestedExclusions: ["Junk"] }), { status: 409 })
        : new Response(JSON.stringify({ id: 3 }), { status: 202 });
    page([smtpAccount(fill({ status: "ok", finishedAt: "2026-10-13T09:00:00Z" }))]);
    expect(screen.queryByRole("button", { name: t.resumeFill })).toBeNull();
    fireEvent.change(screen.getByLabelText(`${t.fieldPassword} (alice@example.com)`), { target: { value: "secret" } });
    fireEvent.change(screen.getByLabelText(`${t.fillInterval} (alice@example.com)`), { target: { value: "24h0m0s" } });
    fireEvent.click(screen.getByRole("button", { name: t.keepRunningFill }));
    expect(await screen.findByText(t.keepRunningTrashSpam("Junk"))).toBeTruthy();
    fireEvent.change(screen.getByLabelText(`${t.fieldPassword} (alice@example.com)`), { target: { value: "secret" } });
    fireEvent.click(screen.getByRole("button", { name: t.keepRunningCopy }));
    await waitFor(() => expect(requests).toHaveLength(2));
    expect(requests[0]).toEqual({ method: "PATCH", url: "/api/accounts/journal/fills/3", body: { keepRunning: true, password: "secret", interval: "24h0m0s" } });
    expect(requests[1]!.body).toEqual({ keepRunning: true, password: "secret", interval: "24h0m0s", confirmFolders: true });
  });

  it("resumes a stopped recurring fill", async () => {
    answer = () => new Response(JSON.stringify({ id: 5 }), { status: 202 });
    page([smtpAccount(recurring({ status: "cancelled", keepRunning: false, every: null, nextAt: null, health: null }))]);
    expect(screen.getByText(t.resumeRecurringHint)).toBeTruthy();
    expect(screen.queryByRole("button", { name: t.keepRunningFill })).toBeNull();
    fireEvent.change(screen.getByLabelText(`${t.fieldPassword} (alice@example.com)`), { target: { value: "secret" } });
    fireEvent.click(screen.getByRole("button", { name: t.resumeFill }));
    await waitFor(() => expect(requests).toHaveLength(1));
    expect(requests[0]).toEqual({ method: "POST", url: "/api/accounts/journal/fills/5/resume", body: { password: "secret" } });
  });

  it("starts a recurring fill without a cut-off", async () => {
    answer = (url) =>
      url.endsWith("/fills/folders")
        ? new Response(JSON.stringify({ folders: [{ name: "INBOX", selected: true }], suggestedExclusions: [] }), { status: 200 })
        : new Response(JSON.stringify({ id: 4 }), { status: 202 });
    page([smtpAccount(null)]);
    fireEvent.click(screen.getByRole("button", { name: t.importFromIMAP }));
    fireEvent.change(screen.getByLabelText(t.fieldHost), { target: { value: "imap.example.com" } });
    fireEvent.change(screen.getByLabelText(t.fieldUsername), { target: { value: "alice@example.com" } });
    fireEvent.change(screen.getByLabelText(t.fieldPassword), { target: { value: "secret" } });
    fireEvent.click(screen.getByRole("button", { name: t.loadFillFolders }));
    await screen.findByRole("button", { name: t.startFill });
    const keep = screen.getByRole("checkbox", { name: t.keepImporting }) as HTMLInputElement;
    expect(keep.checked).toBe(false);
    fireEvent.click(keep);
    expect(screen.queryByLabelText(t.fieldCutoff)).toBeNull();
    expect(screen.getByText(t.keepImportingHint)).toBeTruthy();
    fireEvent.change(screen.getByLabelText(t.fillInterval), { target: { value: "12h0m0s" } });
    fireEvent.click(screen.getByRole("button", { name: t.startFill }));
    await waitFor(() => expect(requests).toHaveLength(2));
    const start = requests[1]!.body as Record<string, unknown>;
    expect(start).toMatchObject({ keepRunning: true, interval: "12h0m0s", confirmFolders: true });
    expect(start.before).toBeUndefined();
  });
});
