import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Account, AccountsResponse, Fill } from "./api";
import { AccountsPage } from "./AccountsPage";
import { fill } from "./fillFixture";
import { t } from "./i18n";

function smtpAccount(f: Fill | null, more: Partial<Account> = {}): Account {
  return {
    name: "journal", kind: "smtp", enabled: true, removed: false, host: "", port: 0, tls: "tls", username: "",
    includedFolders: [], excludedFolders: [], syncInterval: "default", folders: [], goneMessages: 0, lastReconciledAt: null,
    addresses: ["archive@archive.local"], lastReceivedAt: null, fill: f, firstReceived: null,
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
