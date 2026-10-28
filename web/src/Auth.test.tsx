import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { User } from "./api";
import { AuthGate } from "./Auth";
import { applySettings, messagesFor, t } from "./i18n";

const user: User = {
  name: "kim", admin: false, mustChangePassword: false, twoFactorEnabled: true, twoFactorRequired: false, language: "auto", locale: "auto",
};

type Answer = { status: number; body?: unknown };
interface Call {
  method: string;
  path: string;
  body: unknown;
  contentType: string | null;
}

let calls: Call[];
let answers: Record<string, Answer[]>;

/** Answers requests from a queue per "METHOD path" and records them. */
function serve(queue: Record<string, Answer[]>) {
  answers = queue;
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const path = String(input);
    const method = init?.method ?? "GET";
    const headers = new Headers(init?.headers);
    calls.push({ method, path, body: init?.body ? JSON.parse(String(init.body)) : undefined, contentType: headers.get("Content-Type") });
    const next = answers[`${method} ${path}`]?.shift();
    if (!next) throw new Error(`unexpected request ${method} ${path}`);
    return new Response(next.body === undefined ? null : JSON.stringify(next.body), {
      status: next.status,
      headers: { "Content-Type": "application/json" },
    });
  });
}

const noSession: Answer = { status: 401, body: { error: "not logged in" } };

function renderGate() {
  return render(<AuthGate>{(u) => <p>app for {u.name}</p>}</AuthGate>);
}

const password = () => screen.getByLabelText(t.fieldPassword, { selector: "input" }) as HTMLInputElement;

/**
 * The error on screen. Carbon's TextInput has an empty role="alert" region
 * of its own (for its character counter), so only alerts with text count;
 * there must be exactly one.
 */
function alertText(): string {
  const shown = screen.queryAllByRole("alert").filter((a) => a.textContent);
  expect(shown).toHaveLength(1);
  return shown[0]!.textContent ?? "";
}

async function logIn(name: string, pw: string) {
  fireEvent.change(await screen.findByLabelText(t.userName), { target: { value: name } });
  fireEvent.change(password(), { target: { value: pw } });
  fireEvent.click(screen.getByRole("button", { name: t.logIn }));
}

beforeEach(() => {
  calls = [];
  applySettings({ language: "en", locale: "en" });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  applySettings({ language: "en", locale: "en" });
});

describe("login", () => {
  it("sends the password, then the code, as before", async () => {
    serve({
      "GET /api/session": [noSession],
      "POST /api/session": [{ status: 200, body: { twoFactorRequired: true, challenge: "ch-1" } }],
      "POST /api/session/2fa": [{ status: 200, body: { user } }],
    });
    renderGate();
    await logIn("kim", "kim-password");

    expect(await screen.findByRole("heading", { name: t.twoFactorLogin })).toBeTruthy();
    const code = screen.getByLabelText(t.twoFactorCode) as HTMLInputElement;
    // The password is not carried over into the visible code field.
    expect(code.value).toBe("");
    expect(code.type).toBe("text");
    expect(code.autocomplete).toBe("one-time-code");
    fireEvent.change(code, { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: t.logIn }));

    expect(await screen.findByText("app for kim")).toBeTruthy();
    expect(calls).toEqual([
      { method: "GET", path: "/api/session", body: undefined, contentType: null },
      { method: "POST", path: "/api/session", body: { username: "kim", password: "kim-password" }, contentType: "application/json" },
      { method: "POST", path: "/api/session/2fa", body: { challenge: "ch-1", code: "123456" }, contentType: "application/json" },
    ]);
  });

  it("logs in without a second step when the server sends the user", async () => {
    serve({ "GET /api/session": [noSession], "POST /api/session": [{ status: 200, body: { user } }] });
    renderGate();
    expect((await screen.findByLabelText(t.userName)).getAttribute("autocomplete")).toBe("username");
    expect(password().getAttribute("autocomplete")).toBe("current-password");
    expect(password().type).toBe("password");
    await logIn("kim", "kim-password");
    expect(await screen.findByText("app for kim")).toBeTruthy();
  });

  it("shows wrong logins, locked users and the lockout as an alert and clears the password", async () => {
    serve({
      "GET /api/session": [noSession],
      "POST /api/session": [
        { status: 401, body: { error: "wrong user name or password" } },
        { status: 403, body: { error: "user is locked" } },
        { status: 429, body: { error: "too many attempts", retryAfter: 30 } },
      ],
    });
    renderGate();
    await logIn("kim", "nope");
    await waitFor(() => expect(alertText()).toContain(t.wrongLogin));
    expect(password().value).toBe("");

    await logIn("lee", "lee-password");
    await waitFor(() => expect(alertText()).toContain(t.userLocked));
    expect(screen.queryByRole("heading", { name: t.twoFactorLogin })).toBeNull();

    await logIn("kim", "again");
    await waitFor(() => expect(alertText()).toContain(t.tooManyAttempts(30)));
  });

  it("returns to the password step when the code step expired, and rejects a used code", async () => {
    serve({
      "GET /api/session": [noSession],
      "POST /api/session": [
        { status: 200, body: { twoFactorRequired: true, challenge: "ch-1" } },
        { status: 200, body: { twoFactorRequired: true, challenge: "ch-2" } },
      ],
      "POST /api/session/2fa": [
        { status: 401, body: { error: "invalid code" } },
        { status: 401, body: { error: "login expired" } },
      ],
    });
    renderGate();
    await logIn("kim", "kim-password");
    const code = (await screen.findByLabelText(t.twoFactorCode)) as HTMLInputElement;
    fireEvent.change(code, { target: { value: "111111" } });
    fireEvent.click(screen.getByRole("button", { name: t.logIn }));
    await waitFor(() => expect(alertText()).toContain(t.invalidTwoFactor));
    expect(code.value).toBe("");

    fireEvent.change(code, { target: { value: "222222" } });
    fireEvent.click(screen.getByRole("button", { name: t.logIn }));
    await waitFor(() => expect(alertText()).toContain(t.twoFactorExpired));
    expect(screen.getByLabelText(t.userName)).toBeTruthy();
    expect(password().value).toBe("");
  });

  it("explains how to create the first admin", async () => {
    serve({ "GET /api/session": [{ status: 401, body: { error: "setup required", setupRequired: true } }] });
    renderGate();
    expect(await screen.findByRole("heading", { name: t.setupTitle })).toBeTruthy();
    expect(screen.getByText("./ma user add NAME --admin")).toBeTruthy();
    expect(screen.getByRole("button", { name: t.checkAgain })).toBeTruthy();
  });
});

// The forced screens after login bring their forms with the Profile chunk
// (#288): a skeleton first, then the form.
describe("forced screens after login", () => {
  it("asks for an own password: the form loads, the change is sent, then the app shows", async () => {
    serve({
      "GET /api/session": [{ status: 200, body: { user: { ...user, mustChangePassword: true } } }],
      "PUT /api/profile/password": [{ status: 204 }],
    });
    renderGate();
    expect(await screen.findByRole("heading", { name: t.chooseOwnPassword })).toBeTruthy();
    const current = (await screen.findByLabelText(t.currentPassword, { selector: "input" })) as HTMLInputElement;
    // The skeleton is gone once the form is there.
    expect(document.querySelector('[aria-busy="true"]')).toBeNull();
    fireEvent.change(current, { target: { value: "old-password" } });
    fireEvent.change(screen.getByLabelText(t.newPassword, { selector: "input" }), { target: { value: "a-new-password-1" } });
    fireEvent.change(screen.getByLabelText(t.repeatPassword, { selector: "input" }), { target: { value: "a-new-password-1" } });
    fireEvent.click(screen.getByRole("button", { name: t.changePassword }));
    expect(await screen.findByText("app for kim")).toBeTruthy();
    expect(calls.map((c) => `${c.method} ${c.path}`)).toEqual(["GET /api/session", "PUT /api/profile/password"]);
    expect(calls[1]!.body).toEqual({ current: "old-password", new: "a-new-password-1" });
  });

  it("asks for two-factor setup when it is required: the setup loads its state", async () => {
    serve({
      "GET /api/session": [{ status: 200, body: { user: { ...user, twoFactorEnabled: false, twoFactorRequired: true } } }],
      "GET /api/profile/2fa": [{ status: 200, body: { enabled: false, required: true, admin: false, setupPending: false } }],
    });
    renderGate();
    expect(await screen.findByRole("heading", { name: t.twoFactorSetup, level: 2 })).toBeTruthy();
    expect(await screen.findByText(t.twoFactorSetupText)).toBeTruthy();
    expect(screen.getByRole("button", { name: t.twoFactorSetup })).toBeTruthy();
    expect(screen.queryByText("app for kim")).toBeNull();
    expect(calls.map((c) => `${c.method} ${c.path}`)).toEqual(["GET /api/session", "GET /api/profile/2fa"]);
  });
});

describe("login in German", () => {
  // Carbon's English defaults that the login screens must not show.
  const english = ["Show password", "Hide password", "close notification", "error icon", "Copy to clipboard", "Copied", "Show more", "Focus sentinel"];
  const expectNoEnglish = () => {
    const html = document.body.innerHTML;
    for (const s of english) expect(html, s).not.toContain(s);
  };

  it("shows Carbon's texts in German", async () => {
    applySettings({ language: "de", locale: "de" });
    const de = messagesFor("de");
    serve({ "GET /api/session": [noSession], "POST /api/session": [{ status: 401, body: { error: "wrong" } }] });
    renderGate();
    await logIn("kim", "nope");
    await waitFor(() => expect(alertText()).toContain(de.wrongLogin));
    expect(screen.getByRole("button", { name: de.logIn })).toBeTruthy();
    expect(document.body.innerHTML).toContain(de.carbon.showPassword);
    expect(document.body.innerHTML).toContain(de.carbon.status.error);
    expectNoEnglish();

    cleanup();
    serve({ "GET /api/session": [{ status: 401, body: { error: "setup required", setupRequired: true } }] });
    renderGate();
    await screen.findByRole("heading", { name: de.setupTitle });
    expect(document.body.innerHTML).toContain(de.carbon.copy);
    expectNoEnglish();
  });
});
