// The owner's Auditor access page (#393 part 3): the grants with their
// states and last access, the grant dialog with its step-up and the
// server's refusals, Extend and Revoke. The server decides every rule; the
// page sends the owner's choice and shows the answer.
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Account, AccountsResponse } from "./api";
import type { Grant } from "./auditorApi";
import { AuditorAccess, extensionDays } from "./AuditorAccess";
import { auditorTextFor } from "./auditorText";
import { dateOfDay, day, dayOfDate, relativeTime, shiftDay } from "./format";
import { applySettings, t } from "./i18n";

const a = auditorTextFor("en");
const local = (y: number, m: number, d: number) => new Date(y, m - 1, d).toISOString();
const minutesAgo = (n: number) => new Date(Date.now() - n * 60_000).toISOString();

function grant(id: number, more: Partial<Grant> = {}): Grant {
  return {
    id, owner: "pat", auditor: "stb-meyer", label: `Case ${id}`, periodFrom: null, periodUntil: null,
    startsAt: local(2026, 10, 1), endsAt: local(2026, 12, 1), createdAt: local(2026, 10, 1), revokedAt: null, revokeReason: null,
    state: "active", accounts: [{ name: "team", folders: null }], ...more,
  };
}

const grants: Grant[] = [
  grant(1, { label: "Betriebsprüfung 2021–2023", periodFrom: local(2021, 1, 1), periodUntil: local(2024, 1, 1), accounts: [{ name: "team", folders: ["INBOX", "Sent"] }] }),
  grant(2, { label: "Removed auditor", auditor: null, state: "revoked", revokedAt: local(2026, 10, 20), revokeReason: "user_removed" }),
  grant(3, { label: "Moved auditor", auditor: null, state: "revoked", revokedAt: local(2026, 10, 21), revokeReason: "group_change" }),
  grant(4, { label: "Locked", state: "suspended" }),
];

function imap(name: string, folders: string[], more: Partial<Account> = {}): Account {
  return {
    name, kind: "imap", enabled: true, removed: false, folders: folders.map((f) => ({ name: f, delimiter: "/", messages: 1, lastSyncedAt: null, lastReconciledAt: null })),
    ...more,
  } as Account;
}

const accounts: AccountsResponse = {
  accounts: [imap("team", ["INBOX", "Sent"]), imap("info", ["INBOX"]), imap("journal", ["JOURNAL"], { kind: "smtp" }), imap("old", ["INBOX"], { removed: true })],
  shared: [], manage: true, syncInterval: "", syncIntervalMin: "", reconcileInterval: "", alertAfter: 3, smtp: null,
};

let requests: { method: string; url: string; body: unknown }[];
/** The next answer to POST/PATCH of a grant, if not a success. */
let refusal: { status: number; error: string } | null;
const changes = () => requests.filter((r) => r.method !== "GET");

const json = (v: unknown, status = 200) => new Response(v === null ? null : JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });

beforeEach(() => {
  applySettings({ language: "en", locale: "en" });
  requests = [];
  refusal = null;
  vi.spyOn(globalThis, "fetch").mockImplementation(async (url, init) => {
    const method = init?.method ?? "GET";
    const u = String(url);
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    requests.push({ method, url: u, body });
    if (method === "GET" && u === "/api/auditor-grants") return json({ grants });
    if (method === "GET" && u.startsWith("/api/audit?grant=1&")) {
      // The operator's entry is newer; the auditor's own read is the last access.
      return json({ entries: [{ id: 2, at: minutesAgo(1), user: "operator", viaAuditor: true, viaShare: true }, { id: 1, at: minutesAgo(180), user: "stb-meyer", viaAuditor: true, viaShare: true }] });
    }
    if (method === "GET" && u.startsWith("/api/audit?grant=")) return json({ entries: [] });
    if ((method === "POST" || method === "PATCH") && refusal) return json({ error: refusal.error }, refusal.status);
    if (method === "POST") return json({ ...grant(9), ...body, accounts: body.accounts }, 201);
    if (method === "PATCH") return json({ ...grant(1), endsAt: body.endsAt });
    return json(null, 204);
  });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const dialog = () => within(screen.getByRole("dialog"));
const row = (label: string) => screen.getByText(label, { selector: ".users-table__name" }).closest("tr")!;

async function show(twoFactor = false) {
  render(<AuditorAccess twoFactor={twoFactor} accounts={accounts} />);
  await screen.findByText("Betriebsprüfung 2021–2023");
}

describe("the grant list", () => {
  it("shows each grant's accounts, period, window, state and the auditor's last access", async () => {
    await show();
    const first = within(row("Betriebsprüfung 2021–2023"));
    expect(first.getByText("stb-meyer")).toBeTruthy();
    expect(first.getByText("team: INBOX, Sent", { selector: ".app-break-words" })).toBeTruthy();
    expect(first.getByText(`${day(local(2021, 1, 1))} – ${day(local(2023, 12, 31))}`)).toBeTruthy();
    expect(first.getAllByText(`${day(local(2026, 10, 1))} – ${day(local(2026, 11, 30))}`).length).toBeGreaterThan(0);
    expect(first.getByText(a.state("active"))).toBeTruthy();
    // The auditor's read, not the operator's later entry.
    await waitFor(() => expect(requests.some((r) => r.url === "/api/audit?grant=1&action=login&action=search&action=read")).toBe(true));
    expect(await first.findByText(relativeTime(minutesAgo(180)))).toBeTruthy();
    expect(row("Locked").textContent).toContain(a.suspendedHint);
    await waitFor(() => expect(within(row("Locked")).getByText(a.neverAccessed)).toBeTruthy());
  });

  it("keeps ended grants as history with their reason, and without the name of an auditor who left or was removed", async () => {
    await show();
    const removed = within(row("Removed auditor"));
    expect(removed.getByText(a.state("revoked"))).toBeTruthy();
    expect(removed.getByText(a.reason("user_removed"))).toBeTruthy();
    expect(removed.getByText(a.auditorGone("user_removed"))).toBeTruthy();
    const moved = within(row("Moved auditor"));
    expect(moved.getByText(a.reason("group_change"))).toBeTruthy();
    expect(moved.getByText(a.auditorGone("group_change"))).toBeTruthy();
    // Nothing to do with an ended grant.
    expect(removed.queryByRole("button", { name: a.actions("Removed auditor") })).toBeNull();
    expect(within(row("Locked")).getByRole("button", { name: a.actions("Locked") })).toBeTruthy();
  });
});

describe("the grant dialog", () => {
  it("offers the own imap accounts only, and sends the chosen scope with the step-up", async () => {
    await show(true);
    fireEvent.click(screen.getByRole("button", { name: a.grant }));
    const d = dialog();
    // No journal (M1) and no removed account.
    expect(d.queryByLabelText("journal")).toBeNull();
    expect(d.queryByLabelText("old")).toBeNull();
    // Nothing is sent without a name, a case and an account.
    fireEvent.click(d.getByRole("button", { name: a.grant }));
    expect(changes()).toEqual([]);
    expect(d.getByText(a.auditorRequired)).toBeTruthy();
    expect(d.getByText(a.accountsRequired)).toBeTruthy();

    fireEvent.change(d.getByLabelText(a.auditorName), { target: { value: " stb-meyer " } });
    fireEvent.change(d.getByLabelText(a.label), { target: { value: "Betriebsprüfung 2021–2023" } });
    fireEvent.click(d.getByLabelText("team"));
    fireEvent.click(d.getByLabelText("info"));
    // team: only INBOX; info: all folders, also later ones.
    fireEvent.click(d.getAllByLabelText(a.allFoldersLater)[0]!);
    fireEvent.click(d.getByRole("button", { name: a.grant }));
    expect(d.getByText(a.foldersRequired)).toBeTruthy();
    expect(changes()).toEqual([]);
    fireEvent.click(d.getByLabelText("INBOX", { selector: `[id$="-folder-team-INBOX"]` }));
    fireEvent.change(d.getByLabelText(a.currentPassword, { selector: "input" }), { target: { value: "pat-password" } });
    fireEvent.change(d.getByLabelText(a.code), { target: { value: "123456" } });
    fireEvent.click(d.getByRole("button", { name: a.grant }));

    await waitFor(() => expect(changes()).toHaveLength(1));
    const today = dayOfDate(new Date());
    expect(changes()[0]).toEqual({
      method: "POST",
      url: "/api/auditor-grants",
      body: {
        auditor: "stb-meyer",
        label: "Betriebsprüfung 2021–2023",
        accounts: [
          { name: "team", folders: ["INBOX"] },
          { name: "info", folders: null },
        ],
        periodFrom: null,
        periodUntil: null,
        // Starts now; ends after the 28th day (decision 6), at the owner's local midnight.
        startsAt: null,
        endsAt: dateOfDay(shiftDay(today, 28)).toISOString(),
        currentPassword: "pat-password",
        code: "123456",
      },
    });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.getByText(a.created("Betriebsprüfung 2021–2023", "stb-meyer"))).toBeTruthy();
  });

  it("says that there is no such auditor in the group, as the server answers for every unknown or foreign name", async () => {
    refusal = { status: 404, error: "no such auditor in your group" };
    await show();
    fireEvent.click(screen.getByRole("button", { name: a.grant }));
    const d = dialog();
    fireEvent.change(d.getByLabelText(a.auditorName), { target: { value: "kim" } });
    fireEvent.change(d.getByLabelText(a.label), { target: { value: "Audit" } });
    fireEvent.click(d.getByLabelText("team"));
    fireEvent.change(d.getByLabelText(a.currentPassword, { selector: "input" }), { target: { value: "pat-password" } });
    fireEvent.click(d.getByRole("button", { name: a.grant }));
    expect(await d.findByText(a.noAuditor)).toBeTruthy();
    // No 2FA on: no code field, and none sent.
    expect(d.queryByLabelText(a.code)).toBeNull();
    expect((changes()[0]!.body as { code: string }).code).toBe("");
    // The password is asked again.
    expect((d.getByLabelText(a.currentPassword, { selector: "input" }) as HTMLInputElement).value).toBe("");
  });

  it("shows the server's other refusals in words", async () => {
    refusal = { status: 401, error: "the current password is wrong" };
    await show();
    fireEvent.click(screen.getByRole("button", { name: a.grant }));
    const d = dialog();
    fireEvent.change(d.getByLabelText(a.auditorName), { target: { value: "stb-meyer" } });
    fireEvent.change(d.getByLabelText(a.label), { target: { value: "Audit" } });
    fireEvent.click(d.getByLabelText("team"));
    fireEvent.click(d.getByRole("button", { name: a.grant }));
    expect(await d.findByText(t.wrongCurrentPassword)).toBeTruthy();
    refusal = { status: 400, error: "invalid grant: a grant lasts at most 365 days" };
    fireEvent.click(d.getByRole("button", { name: a.grant }));
    expect(await d.findByText(a.tooLong)).toBeTruthy();
  });
});

describe("extend and revoke", () => {
  it("extends to a chosen later day with the step-up", async () => {
    await show();
    fireEvent.click(within(row("Betriebsprüfung 2021–2023")).getByRole("button", { name: a.actions("Betriebsprüfung 2021–2023") }));
    fireEvent.click(screen.getByRole("menuitem", { name: a.extend }));
    const d = dialog();
    expect(d.getByText(a.extendText(day(local(2026, 11, 30))))).toBeTruthy();
    const select = d.getByLabelText(a.newLastDay) as HTMLSelectElement;
    const days = extensionDays(grants[0]!.endsAt);
    expect([...select.options].map((o) => o.value)).toEqual(days);
    fireEvent.change(select, { target: { value: days[1] } });
    fireEvent.change(d.getByLabelText(a.currentPassword, { selector: "input" }), { target: { value: "pat-password" } });
    fireEvent.click(d.getByRole("button", { name: a.extend }));
    await waitFor(() =>
      expect(changes()).toEqual([
        { method: "PATCH", url: "/api/auditor-grants/1", body: { endsAt: dateOfDay(shiftDay(days[1]!, 1)).toISOString(), currentPassword: "pat-password", code: "" } },
      ]),
    );
  });

  it("explains a refused extension of a revoked grant", async () => {
    refusal = { status: 409, error: "the grant was revoked; create a new one" };
    await show();
    fireEvent.click(within(row("Locked")).getByRole("button", { name: a.actions("Locked") }));
    fireEvent.click(screen.getByRole("menuitem", { name: a.extend }));
    fireEvent.click(dialog().getByRole("button", { name: a.extend }));
    expect(await dialog().findByText(a.revokedAlready)).toBeTruthy();
  });

  it("revokes only after the confirmation", async () => {
    await show();
    fireEvent.click(within(row("Betriebsprüfung 2021–2023")).getByRole("button", { name: a.actions("Betriebsprüfung 2021–2023") }));
    fireEvent.click(screen.getByRole("menuitem", { name: a.revoke }));
    const d = dialog();
    expect(d.getByText(a.revokeText("stb-meyer"))).toBeTruthy();
    expect(changes()).toEqual([]);
    fireEvent.click(d.getByRole("button", { name: a.revoke }));
    await waitFor(() => expect(changes()).toEqual([{ method: "DELETE", url: "/api/auditor-grants/1", body: undefined }]));
    expect(await screen.findByText(a.revoked("Betriebsprüfung 2021–2023"))).toBeTruthy();
  });
});

describe("extensionDays", () => {
  const now = new Date(2026, 10, 4, 12);
  it("offers four weeks, three and six months after the end, and the latest day 365 days from today", () => {
    const end = new Date(2026, 11, 1).toISOString(); // last day Nov 30
    expect(extensionDays(end, now)).toEqual(["2026-12-28", "2027-03-01", "2027-05-31", "2027-11-03"]);
  });
  it("never offers more than 365 days from today, nor a day before the current end", () => {
    const end = new Date(2027, 9, 1).toISOString(); // last day Sep 30, 2027
    expect(extensionDays(end, now)).toEqual(["2027-10-28", "2027-11-03"]);
    expect(extensionDays(new Date(2027, 10, 4).toISOString(), now)).toEqual([]);
  });
  it("counts from today for an expired grant", () => {
    expect(extensionDays(new Date(2026, 9, 1).toISOString(), now)[0]).toBe("2026-12-02");
  });
});
