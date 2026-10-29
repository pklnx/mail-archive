import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { UNAUTHORIZED_EVENT, type User } from "./api";
import { AuthGate } from "./Auth";
import { applySettings, messagesFor, t } from "./i18n";
import { expectLoaded, expectLoading } from "./test/loading";

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

/** The "Loading…" statuses on the page now. */
const loadingStatuses = () => screen.queryAllByRole("status").filter((s) => s.textContent?.includes(t.loading));

/** Records the most "Loading…" statuses the page had at once, at every DOM change. */
function countStatuses() {
  let most = loadingStatuses().length;
  const observer = new MutationObserver(() => {
    most = Math.max(most, loadingStatuses().length);
  });
  observer.observe(document.body, { childList: true, subtree: true, characterData: true, attributes: true });
  return {
    most: () => most,
    stop: () => observer.disconnect(),
  };
}

describe("start-up gate (#287)", () => {
  it("shows only a hidden status while the session is checked, no skeleton and no visible text", async () => {
    let answer!: (r: Response) => void;
    vi.spyOn(globalThis, "fetch").mockImplementation(() => new Promise<Response>((r) => (answer = r)));
    const { container } = renderGate();
    const status = screen.getByRole("status");
    expect(status.textContent).toBe(t.loading);
    expect(status.className).toContain("cds--visually-hidden");
    expect(container.querySelector(".cds--skeleton__text")).toBeNull();
    // No login-shaped placeholder: no heading, no field.
    expect(screen.queryAllByRole("heading")).toHaveLength(0);
    expect(screen.queryAllByRole("textbox")).toHaveLength(0);
    await act(async () => answer(new Response(JSON.stringify(noSession.body), { status: 401, headers: { "Content-Type": "application/json" } })));
    expect(await screen.findByLabelText(t.userName)).toBeTruthy();
    expect(screen.queryByRole("status")).toBeNull();
  });
});

// The forced screens after login bring their forms with the Profile chunk
// (#288): a skeleton first, then the form.
describe("forced screens after login", () => {
  it("shows the 2FA section's own status after the forced password screen, never two at once (#287)", async () => {
    let release!: () => void;
    const released = new Promise<void>((r) => (release = r));
    const json = (body: unknown, status = 200) => new Response(body === null ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const key = `${init?.method ?? "GET"} ${String(input)}`;
      if (key === "GET /api/session") return json({ user: { ...user, mustChangePassword: true, twoFactorEnabled: false, twoFactorRequired: true } });
      if (key === "PUT /api/profile/password") return json(null, 204);
      if (key === "GET /api/profile/2fa") {
        await released;
        return json({ enabled: false, required: true, admin: false, setupPending: false });
      }
      throw new Error(`unexpected request ${key}`);
    });
    const statuses = countStatuses();
    renderGate();
    // The forced password screen: the Profile chunk loads, then the form.
    const current = await screen.findByLabelText(t.currentPassword, { selector: "input" });
    expectLoaded();
    fireEvent.change(current, { target: { value: "old-password" } });
    fireEvent.change(screen.getByLabelText(t.newPassword, { selector: "input" }), { target: { value: "a-new-password-1" } });
    fireEvent.change(screen.getByLabelText(t.repeatPassword, { selector: "input" }), { target: { value: "a-new-password-1" } });
    fireEvent.click(screen.getByRole("button", { name: t.changePassword }));
    // Then the 2FA screen: the chunk is there, the section loads its state and says so.
    expect(await screen.findByRole("heading", { name: t.twoFactorSetup, level: 2 })).toBeTruthy();
    await waitFor(() => expect(document.querySelector(".app-view-fallback")).toBeNull());
    const status = expectLoading(document.querySelector(".auth-page__form")!)!;
    expect(status.closest(".app-view-fallback")).toBeNull();
    await act(async () => release());
    expect(await screen.findByRole("button", { name: t.twoFactorSetup })).toBeTruthy();
    expectLoaded();
    statuses.stop();
    expect(statuses.most()).toBe(1);
  });

  it("asks for an own password: the form loads, the change is sent, then the app shows", async () => {
    serve({
      "GET /api/session": [{ status: 200, body: { user: { ...user, mustChangePassword: true } } }],
      "PUT /api/profile/password": [{ status: 204 }],
    });
    renderGate();
    expect(await screen.findByRole("heading", { name: t.chooseOwnPassword })).toBeTruthy();
    const current = (await screen.findByLabelText(t.currentPassword, { selector: "input" })) as HTMLInputElement;
    // The skeleton and its status are gone once the form is there.
    expectLoaded();
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

describe("logout (#327)", () => {
  const session: Answer = { status: 200, body: { user } };
  const failed: Answer = { status: 500, body: { error: "store unavailable" } };

  /** The app stand-in: a Log out button and the reason the last Log out failed. */
  function renderApp() {
    return render(
      <AuthGate>
        {(u, logout, logoutError) => (
          <div>
            <p>app for {u.name}</p>
            <button onClick={logout}>{t.logOut}</button>
            {logoutError && <p role="alert">{logoutError}</p>}
          </div>
        )}
      </AuthGate>,
    );
  }
  const logOut = () => fireEvent.click(screen.getByRole("button", { name: t.logOut }));
  const deletes = () => calls.filter((c) => c.method === "DELETE" && c.path === "/api/session").length;

  it("shows the login page after a successful Log out, with one DELETE", async () => {
    serve({ "GET /api/session": [session], "DELETE /api/session": [{ status: 204 }] });
    renderApp();
    await screen.findByText("app for kim");
    logOut();
    expect(await screen.findByLabelText(t.userName)).toBeTruthy();
    expect(screen.queryByText("app for kim")).toBeNull();
    expect(deletes()).toBe(1);
  });

  it("stays signed in with the reason when the DELETE fails, and Log out again retries", async () => {
    serve({ "GET /api/session": [session], "DELETE /api/session": [failed, { status: 204 }] });
    renderApp();
    await screen.findByText("app for kim");
    logOut();
    expect((await screen.findByRole("alert")).textContent).toBe(t.logoutFailed("store unavailable"));
    expect(screen.getByText("app for kim")).toBeTruthy();
    expect(screen.queryByLabelText(t.userName)).toBeNull();
    expect(deletes()).toBe(1);
    expect(calls).toHaveLength(2);

    logOut();
    expect(await screen.findByLabelText(t.userName)).toBeTruthy();
    expect(deletes()).toBe(2);
  });

  it("clears the alert when Log out is tried again, and shows it again on a second failure", async () => {
    serve({ "GET /api/session": [session], "DELETE /api/session": [failed] });
    renderApp();
    await screen.findByText("app for kim");
    logOut();
    await screen.findByRole("alert");
    // The second attempt waits for its answer.
    let answer!: (r: Response) => void;
    vi.mocked(globalThis.fetch).mockImplementationOnce(() => new Promise<Response>((resolve) => (answer = resolve)));
    logOut();
    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
    answer(new Response(JSON.stringify({ error: "still unavailable" }), { status: 500, headers: { "Content-Type": "application/json" } }));
    expect((await screen.findByRole("alert")).textContent).toBe(t.logoutFailed("still unavailable"));
    expect(screen.getByText("app for kim")).toBeTruthy();
  });

  // Vitest fails the run on an unhandled rejection (vite.config.ts does not
  // turn that off), so this also proves the rejection is handled.
  it("stays signed in when the request itself fails (no answer)", async () => {
    serve({ "GET /api/session": [session] });
    renderApp();
    await screen.findByText("app for kim");
    logOut();
    expect((await screen.findByRole("alert")).textContent).toBe(t.logoutFailed("unexpected request DELETE /api/session"));
    expect(screen.getByText("app for kim")).toBeTruthy();
  });

  it("drops the alert on the next state change: a 401 elsewhere, then a new login", async () => {
    serve({
      "GET /api/session": [session, noSession],
      "DELETE /api/session": [failed],
      "POST /api/session": [{ status: 200, body: { user } }],
    });
    renderApp();
    await screen.findByText("app for kim");
    logOut();
    await screen.findByRole("alert");
    // Another request answered 401: the gate checks the session again.
    window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
    await logIn("kim", "kim-password");
    expect(await screen.findByText("app for kim")).toBeTruthy();
    expect(screen.queryByText(t.logoutFailed("store unavailable"))).toBeNull();
    expect(screen.queryAllByRole("alert").filter((a) => a.textContent)).toHaveLength(0);
  });

  it("keeps the own-password screen with an alert when the DELETE fails, then signs out", async () => {
    serve({
      "GET /api/session": [{ status: 200, body: { user: { ...user, mustChangePassword: true } } }],
      "DELETE /api/session": [failed, { status: 204 }],
    });
    renderGate();
    expect(await screen.findByRole("heading", { name: t.chooseOwnPassword })).toBeTruthy();
    logOut();
    await waitFor(() => expect(alertText()).toContain(t.logoutFailed("store unavailable")));
    expect(screen.getByRole("heading", { name: t.chooseOwnPassword })).toBeTruthy();
    logOut();
    expect(await screen.findByLabelText(t.userName)).toBeTruthy();
    expect(deletes()).toBe(2);
  });

  it("keeps the two-factor setup screen with an alert when the DELETE fails, then signs out", async () => {
    serve({
      "GET /api/session": [{ status: 200, body: { user: { ...user, twoFactorEnabled: false, twoFactorRequired: true } } }],
      "GET /api/profile/2fa": [{ status: 200, body: { enabled: false, required: true, admin: false, setupPending: false } }],
      "DELETE /api/session": [failed, { status: 204 }],
    });
    renderGate();
    expect(await screen.findByRole("heading", { name: t.twoFactorSetup, level: 2 })).toBeTruthy();
    await screen.findByText(t.twoFactorSetupText);
    logOut();
    await waitFor(() => expect(alertText()).toContain(t.logoutFailed("store unavailable")));
    expect(screen.getByRole("heading", { name: t.twoFactorSetup, level: 2 })).toBeTruthy();
    logOut();
    expect(await screen.findByLabelText(t.userName)).toBeTruthy();
    expect(deletes()).toBe(2);
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
