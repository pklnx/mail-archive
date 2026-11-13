import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "./i18n";
import type { Notifications } from "./notificationsApi";
import { notifyText } from "./notifyText";
import { ProfilePage } from "./Profile";

const n = notifyText("en");

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const empty: Notifications = { available: true, email: null, confirmed: false, confirmedAt: null, bounced: false, codeExpiresAt: null };

describe("Profile → Notifications (#397, part 5)", () => {
  let state: Notifications;
  let requests: { method: string; url: string; body?: unknown }[];

  beforeEach(() => {
    state = { ...empty };
    requests = [];
    vi.stubGlobal("PublicKeyCredential", function PublicKeyCredential() {});
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const method = init?.method ?? "GET";
      const url = String(input);
      const body = init?.body ? JSON.parse(String(init.body)) : undefined;
      requests.push({ method, url, body });
      switch (`${method} ${url}`) {
        case "GET /api/profile/notifications":
          return json(state);
        case "PUT /api/profile/notifications":
          state = { ...state, email: body.email, confirmed: false, codeExpiresAt: "2026-11-13T10:30:00Z" };
          return json(state);
        case "POST /api/profile/notifications/confirm":
          if (body.code !== "ABC DEF") return json({ error: "wrong code", code: "wrong_code" }, 400);
          state = { ...state, confirmed: true, confirmedAt: "2026-11-13T10:05:00Z", codeExpiresAt: null };
          return json(state);
        case "DELETE /api/profile/notifications":
          return new Response(null, { status: 204 });
        case "GET /api/profile/2fa":
          return json({ enabled: true, required: true, admin: true, setupPending: false });
        case "GET /api/profile/passkeys":
          return json({ available: false, origin: "", max: 10, passkeys: [] });
        case "GET /api/profile/api-keys":
          return json({ max: 10, keys: [] });
      }
      throw new Error(`unexpected request ${method} ${url}`);
    });
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("is not asked for or shown for members", async () => {
    render(<ProfilePage name="kim" />);
    await screen.findByRole("tab", { name: t.apiKeys });
    expect(screen.queryByRole("tab", { name: n.tab })).toBeNull();
    expect(requests.some((r) => r.url === "/api/profile/notifications")).toBe(false);
  });

  it("stays hidden for admins while alert mail is off on the server", async () => {
    state = { ...empty, available: false };
    render(<ProfilePage name="admin" admin />);
    await waitFor(() => expect(requests.some((r) => r.url === "/api/profile/notifications")).toBe(true));
    expect(screen.queryByRole("tab", { name: n.tab })).toBeNull();
  });

  it("lets an admin set, confirm and remove the address", async () => {
    render(<ProfilePage name="admin" admin />);
    fireEvent.click(await screen.findByRole("tab", { name: n.tab }));
    const email = screen.getByLabelText(n.email);
    fireEvent.change(email, { target: { value: " admin@example.com " } });
    fireEvent.click(screen.getByRole("button", { name: n.sendCode }));
    await screen.findByText(n.codeSentNow("admin@example.com"));
    const put = requests.find((r) => r.method === "PUT");
    expect(put?.body).toEqual({ email: "admin@example.com", language: "en" });
    expect(screen.getByText(n.stateUnconfirmed)).toBeTruthy();

    const code = screen.getByLabelText(n.code);
    const form = code.closest("form")!;
    // One primary action in the tab: confirming, while a code waits.
    expect(form.closest('[role="tabpanel"]')!.querySelectorAll(".cds--btn--primary")).toHaveLength(1);
    fireEvent.change(code, { target: { value: "ZZZ ZZZ" } });
    fireEvent.click(within(form).getByRole("button", { name: n.confirm }));
    await screen.findByText(n.wrongCode);
    fireEvent.change(code, { target: { value: "ABC DEF" } });
    fireEvent.click(within(form).getByRole("button", { name: n.confirm }));
    await screen.findByText(n.confirmedNow);
    expect(screen.getByText(n.stateConfirmed)).toBeTruthy();
    expect(screen.queryByLabelText(n.code)).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: n.remove }));
    await screen.findByText(n.removed);
    expect(requests.some((r) => r.method === "DELETE" && r.url === "/api/profile/notifications")).toBe(true);
    expect(screen.queryByRole("button", { name: n.remove })).toBeNull();
  });

  it("shows when the relay refused the address", async () => {
    state = { ...empty, email: "admin@example.com", bounced: true };
    render(<ProfilePage name="admin" admin />);
    fireEvent.click(await screen.findByRole("tab", { name: n.tab }));
    expect(screen.getByText(n.bounced)).toBeTruthy();
    expect((screen.getByLabelText(n.email) as HTMLInputElement).value).toBe("admin@example.com");
  });
});
