// An auditor in the app (#393 part 3): the setup at the first sign-in sends
// nothing but its own steps until a case is chosen (#441, coordinator note:
// the server refuses everything else then and records each refusal), the
// grant step at login, and the auditor's shell: the nav, the banner, the
// switch to another case, and an account list that is not polled.
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AccountsResponse, Session, SessionGrant, User } from "./api";
import { auditorTextFor } from "./auditorText";
import { applySettings, t } from "./i18n";
import { Root } from "./Root";

const a = auditorTextFor("en");

const g1: SessionGrant = {
  id: 11, label: "Betriebsprüfung 2021–2023", owner: "pat",
  // Local midnights, as the owner's browser sends them (decision 5).
  periodFrom: new Date(2021, 0, 1).toISOString(), periodUntil: new Date(2024, 0, 1).toISOString(),
  startsAt: new Date(2026, 9, 1).toISOString(), endsAt: new Date(2026, 11, 1).toISOString(),
};
const g2: SessionGrant = { ...g1, id: 12, label: "Umsatzsteuer 2024", periodFrom: null, periodUntil: null };

const auditor: User = {
  name: "stb-meyer", admin: false, auditor: true, mustChangePassword: false, twoFactorEnabled: true, twoFactorRequired: true, language: "en", locale: "en",
};
/** A fresh auditor: a generated password, no 2FA yet, two grants and none chosen. */
const fresh: Session = { user: { ...auditor, mustChangePassword: true, twoFactorEnabled: false }, grants: [g1, g2], grantSelectionRequired: true };
const bound = (g: SessionGrant): Session => ({ user: auditor, grant: g, grants: [g1, g2] });

const granted: AccountsResponse = {
  accounts: [],
  // An auditor's accounts: no enabled flag, folders without counts (part 2).
  shared: [{ key: "pat/team", name: "team", owner: "pat", kind: "imap", removed: false, folders: [{ name: "INBOX", delimiter: "/" }] } as never],
  manage: false, syncInterval: "", syncIntervalMin: "", reconcileInterval: "", alertAfter: 0, smtp: null,
};

let session: Session;
let calls: string[];

const json = (body: unknown, status = 200) =>
  new Response(body === null ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

/** The server of an auditor's session, as part 2 answers it; it records every request. */
function serve() {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      const key = `${init?.method ?? "GET"} ${String(input)}`;
      calls.push(key);
      const body = init?.body ? (JSON.parse(String(init.body)) as Record<string, unknown>) : {};
      if (key === "GET /api/session") return json(session);
      if (key === "PUT /api/profile/password") {
        session = { ...session, user: { ...session.user, mustChangePassword: false } };
        return json(null, 204);
      }
      if (key === "GET /api/profile/2fa") return json({ enabled: session.user.twoFactorEnabled, required: true, admin: false, setupPending: false });
      if (key === "POST /api/profile/2fa/setup") return json({ enabled: false, required: true, admin: false, setupPending: true, secret: "JBSWY3DP", qrDataUrl: "" });
      if (key === "POST /api/profile/2fa/confirm") {
        session = { ...session, user: { ...session.user, twoFactorEnabled: true } };
        return json({ enabled: true, recoveryCodes: ["aaaa-bbbb", "cccc-dddd"] });
      }
      if (key === "PUT /api/session/grant") {
        const g = [g1, g2].find((x) => x.id === body.id);
        if (!g) return json({ error: "no such grant" }, 404);
        session = bound(g);
        return json(session);
      }
      // Until a case is chosen, part 2 refuses everything else.
      if (!session.grant) return json({ error: "choose a grant first", grantSelectionRequired: true }, 403);
      if (key === "GET /api/accounts") return json(granted);
      if (key.startsWith("GET /api/messages?")) return json({ messages: [], nextCursor: null });
      if (key.startsWith("GET /api/audit")) return json({ entries: [] });
      return json({ error: "not available to auditors" }, 403);
    }),
  );
}

/** The requests an auditor's session may send before it has a case: the setup steps and the choice. */
const setupRequests = new Set([
  "GET /api/session",
  "PUT /api/profile/password",
  "GET /api/profile/2fa",
  "POST /api/profile/2fa/setup",
  "POST /api/profile/2fa/confirm",
  "PUT /api/session/grant",
]);

const original = window.matchMedia;
beforeEach(() => {
  calls = [];
  applySettings({ language: "en", locale: "en" });
  window.history.replaceState(null, "", "/");
  window.matchMedia = (query: string) =>
    ({ matches: query.includes("66rem") || query.includes("48rem"), media: query, onchange: null, addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false }) as MediaQueryList;
  serve();
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  window.matchMedia = original;
  localStorage.clear();
});

async function chooseCase(label: string) {
  expect(await screen.findByRole("heading", { name: a.chooseTitle })).toBeTruthy();
  fireEvent.click(screen.getByLabelText(new RegExp(`^${label}`)));
  fireEvent.click(screen.getByRole("button", { name: a.chooseAction }));
}

describe("an auditor's first sign-in (#441)", () => {
  it("sends only the setup steps until a case is chosen, then starts the app on it", async () => {
    session = fresh;
    render(<Root />);
    // The own password.
    const current = await screen.findByLabelText(t.currentPassword, { selector: "input" });
    fireEvent.change(current, { target: { value: "Generated-1" } });
    fireEvent.change(screen.getByLabelText(t.newPassword, { selector: "input" }), { target: { value: "a-new-password-1" } });
    fireEvent.change(screen.getByLabelText(t.repeatPassword, { selector: "input" }), { target: { value: "a-new-password-1" } });
    fireEvent.click(screen.getByRole("button", { name: t.changePassword }));
    // The mandatory 2FA, with its recovery codes.
    fireEvent.click(await screen.findByRole("button", { name: t.twoFactorSetup }));
    fireEvent.change(await screen.findByLabelText(t.twoFactorCode), { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: t.twoFactorConfirm }));
    fireEvent.click(await screen.findByRole("button", { name: t.twoFactorCodesSaved }));
    // The case: two grants, so the auditor picks one.
    await chooseCase(g2.label);
    expect(await screen.findByText(a.bannerTitle)).toBeTruthy();
    await waitFor(() => expect(calls).toContain("GET /api/accounts"));

    const chosen = calls.indexOf("PUT /api/session/grant");
    expect(chosen).toBeGreaterThan(0);
    // Before the choice: the setup's own requests only, never status, accounts or messages.
    expect(calls.slice(0, chosen).filter((c) => !setupRequests.has(c))).toEqual([]);
    expect(calls.slice(0, chosen)).toContain("POST /api/profile/2fa/confirm");
    expect(screen.getByText(new RegExp(`“${g2.label}” of pat`))).toBeTruthy();
  });

  it("keeps the grants across the password screen when 2FA is already on", async () => {
    // An admin reset the password but not the 2FA: the grant step still comes.
    session = { ...fresh, user: { ...fresh.user, twoFactorEnabled: true } };
    render(<Root />);
    const current = await screen.findByLabelText(t.currentPassword, { selector: "input" });
    fireEvent.change(current, { target: { value: "Generated-1" } });
    fireEvent.change(screen.getByLabelText(t.newPassword, { selector: "input" }), { target: { value: "a-new-password-1" } });
    fireEvent.change(screen.getByLabelText(t.repeatPassword, { selector: "input" }), { target: { value: "a-new-password-1" } });
    fireEvent.click(screen.getByRole("button", { name: t.changePassword }));
    await chooseCase(g1.label);
    await screen.findByText(a.bannerTitle);
    const chosen = calls.indexOf("PUT /api/session/grant");
    expect(calls.slice(0, chosen).filter((c) => !setupRequests.has(c))).toEqual([]);
  });

  it("asks for the case after a login whose answer has none chosen", async () => {
    session = { user: null, setupRequired: false } as never;
    const fetch = vi.mocked(globalThis.fetch).getMockImplementation()!;
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      if (`${init?.method ?? "GET"} ${String(input)}` === "POST /api/session") {
        calls.push("POST /api/session");
        session = { user: auditor, grants: [g1, g2], grantSelectionRequired: true };
        return json(session);
      }
      if (String(input) === "/api/session" && !session.user) {
        calls.push("GET /api/session");
        return json({ error: "login required" }, 401);
      }
      return fetch(input, init);
    });
    render(<Root />);
    fireEvent.change(await screen.findByLabelText(t.userName), { target: { value: "stb-meyer" } });
    fireEvent.change(screen.getByLabelText(t.fieldPassword, { selector: "input" }), { target: { value: "pw" } });
    fireEvent.click(screen.getByRole("button", { name: t.logIn }));
    await chooseCase(g1.label);
    await screen.findByText(a.bannerTitle);
    expect(calls.slice(0, calls.indexOf("PUT /api/session/grant"))).toEqual(["GET /api/session", "POST /api/session"]);
  });

  it("goes back to the grant step when the server says a case must be chosen", async () => {
    session = bound(g1);
    render(<Root />);
    await screen.findByText(a.bannerTitle);
    // The session lost its grant (the server's gate answers 403 with grantSelectionRequired).
    session = { user: auditor, grants: [g1, g2], grantSelectionRequired: true };
    fireEvent.click(screen.getByRole("link", { name: t.log }));
    expect(await screen.findByRole("heading", { name: a.chooseTitle })).toBeTruthy();
  });
});

describe("the auditor's shell (#393)", () => {
  it("has the mail, the Log and the profile only, and a banner with the case", async () => {
    session = bound(g1);
    render(<Root />);
    const banner = await screen.findByText(a.bannerTitle);
    const box = banner.closest(".auditor-banner")!;
    expect(box.textContent).toContain("Read-only access to “Betriebsprüfung 2021–2023” of pat until Nov 30, 2026.");
    expect(box.textContent).toContain("Messages from Jan 1, 2021 to Dec 31, 2023.");
    const nav = screen.getByRole("navigation", { name: t.mainNavigation });
    expect(within(nav).getAllByRole("link").map((l) => l.textContent)).toEqual([t.allMail, t.log, t.profile]);
    // The granted account reads as enabled, without counts.
    const tree = await screen.findByRole("treeitem", { name: /team/, hidden: true });
    expect(tree.textContent).not.toContain(t.disabled);
    expect(tree.querySelector(".folder-tree__count")).toBeNull();
  });

  it("switches the session to another case and starts afresh on its mail", async () => {
    session = bound(g1);
    window.history.replaceState(null, "", "/?account=pat%2Fteam&folder=INBOX");
    render(<Root />);
    await screen.findByText(a.bannerTitle);
    const select = screen.getByLabelText(a.switchCase) as HTMLSelectElement;
    expect([...select.options].map((o) => o.text)).toEqual([a.caseOf(g1.label, "pat"), a.caseOf(g2.label, "pat")]);
    const before = calls.filter((c) => c === "GET /api/accounts").length;
    fireEvent.change(select, { target: { value: "12" } });
    await waitFor(() => expect(screen.getByText(/“Umsatzsteuer 2024” of pat/)).toBeTruthy());
    expect(window.location.search).toBe("");
    // The app started again: the accounts of the new case are read.
    await waitFor(() => expect(calls.filter((c) => c === "GET /api/accounts").length).toBe(before + 1));
  });

  it("loads the granted accounts once, without the polls of an owner", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    session = bound(g1);
    render(<Root />);
    await screen.findByText(a.bannerTitle);
    await waitFor(() => expect(calls.filter((c) => c === "GET /api/accounts")).toHaveLength(1));
    // An owner's list is read again after 30 s; an auditor's never (each read is a logged account.list).
    await act(async () => {
      vi.advanceTimersByTime(65_000);
    });
    expect(calls.filter((c) => c === "GET /api/accounts")).toHaveLength(1);
    expect(calls.filter((c) => !c.startsWith("GET /api/accounts") && !c.startsWith("GET /api/messages") && c !== "GET /api/session")).toEqual([]);
  });
});
