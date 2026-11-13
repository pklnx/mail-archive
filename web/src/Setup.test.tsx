import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { User } from "./api";
import { AuthGate } from "./Auth";
import { applySettings, t } from "./i18n";
import { resetSetupLinkForTests } from "./setupLink";
import { setupTextFor } from "./setupText";

const st = setupTextFor("en");

// The setup link of a new instance (#396): `/#setup=<token>`.

type Answer = { status: number; body?: unknown };
interface Call {
  method: string;
  path: string;
  body: unknown;
}

let calls: Call[];

/** Answers requests from a queue per "METHOD path" and records them. */
function serve(queue: Record<string, Answer[]>) {
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const path = String(input);
    const method = init?.method ?? "GET";
    calls.push({ method, path, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    const next = queue[`${method} ${path}`]?.shift();
    if (!next) throw new Error(`unexpected request ${method} ${path}`);
    return new Response(next.body === undefined ? null : JSON.stringify(next.body), {
      status: next.status,
      headers: { "Content-Type": "application/json" },
    });
  });
}

/** A token of the shape `setup token` prints, made at run time. */
function newToken(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(32));
  return btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

/** Opens the page at path, as a browser that followed a link would. */
function openAt(path: string) {
  window.history.replaceState(null, "", path);
  resetSetupLinkForTests();
}

const noUser = (setup: string): Answer => ({ status: 401, body: { error: "login required", setupRequired: setup !== "complete", setup } });

const admin: User = {
  name: "root", admin: true, mustChangePassword: false, twoFactorEnabled: false, twoFactorRequired: true, language: "auto", locale: "auto",
};

function renderGate() {
  return render(<AuthGate>{(s) => <p>app for {s.user.name}</p>}</AuthGate>);
}

async function fillForm(name: string, pw: string, again = pw) {
  fireEvent.change(await screen.findByLabelText(t.userName), { target: { value: name } });
  fireEvent.change(screen.getByLabelText(t.fieldPassword, { selector: "input" }), { target: { value: pw } });
  fireEvent.change(screen.getByLabelText(st.repeatPassword, { selector: "input" }), { target: { value: again } });
  fireEvent.click(screen.getByRole("button", { name: st.create }));
}

/** The error on screen: only alerts with text count, exactly one. */
function alertText(): string {
  const shown = screen.queryAllByRole("alert").filter((a) => a.textContent);
  expect(shown).toHaveLength(1);
  return shown[0]!.textContent ?? "";
}

beforeEach(() => {
  calls = [];
  applySettings({ language: "en", locale: "en" });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  openAt("/");
});

describe("setup link", () => {
  it("takes the token from the fragment, removes it at once and creates the admin, who sets up 2FA next", async () => {
    const token = newToken();
    openAt(`/?view=x#setup=${token}`);
    serve({
      "GET /api/session": [noUser("token")],
      "POST /api/setup": [{ status: 201, body: { user: admin } }],
      "GET /api/profile/2fa": [{ status: 200, body: { enabled: false, required: true, admin: true, setupPending: false } }],
    });
    renderGate();
    // Gone from the address bar before anything else, the rest of the URL kept.
    expect(window.location.hash).toBe("");
    expect(window.location.href).not.toContain(token);
    expect(window.location.search).toBe("?view=x");
    expect(await screen.findByRole("heading", { name: setupTextFor("en").title })).toBeTruthy();

    await fillForm(" Root ", "correct horse battery");
    expect(await screen.findByRole("heading", { name: t.twoFactorSetup, level: 2 })).toBeTruthy();
    expect(calls.slice(0, 2)).toEqual([
      { method: "GET", path: "/api/session", body: undefined },
      { method: "POST", path: "/api/setup", body: { token, username: "root", password: "correct horse battery" } },
    ]);
    expect(document.body.innerHTML).not.toContain(token);
  });

  it("checks name and passwords before it sends anything", async () => {
    openAt(`/#setup=${newToken()}`);
    serve({ "GET /api/session": [noUser("token")] });
    renderGate();
    await fillForm("Not Valid!", "correct horse battery");
    expect(alertText()).toContain(st.nameInvalid);
    await fillForm("root", "too short");
    expect(alertText()).toContain(st.passwordShort);
    await fillForm("root", "correct horse battery", "correct horse battery!");
    expect(alertText()).toContain(st.passwordsDiffer);
    expect(calls.map((c) => c.path)).toEqual(["/api/session"]);
  });

  it("says so when the server refuses the token, and when the link was used", async () => {
    openAt(`/#setup=${newToken()}`);
    serve({
      "GET /api/session": [noUser("token"), noUser("token"), noUser("complete")],
      "POST /api/setup": [
        { status: 429, body: { error: "too many failed attempts; try again later", retryAfter: 30 } },
        { status: 401, body: { error: "wrong or expired setup token" } },
      ],
    });
    renderGate();
    await fillForm("root", "correct horse battery");
    expect(await screen.findByText(t.tooManyAttempts(30))).toBeTruthy();
    await fillForm("root", "correct horse battery");
    expect(await screen.findByText(st.linkInvalid)).toBeTruthy();
    expect(screen.queryByRole("button", { name: st.create })).toBeNull();
    // To the login page; the token is dropped.
    fireEvent.click(screen.getByRole("button", { name: st.toLogin }));
    expect(await screen.findByRole("button", { name: setupTextFor("en").checkAgain })).toBeTruthy();

    cleanup();
    openAt(`/#setup=${newToken()}`);
    serve({ "GET /api/session": [noUser("token")], "POST /api/setup": [{ status: 404, body: { error: "setup not available" } }] });
    renderGate();
    await fillForm("root", "correct horse battery");
    expect(await screen.findByText(st.linkUsed)).toBeTruthy();
  });

  it("shows an expired, a used and an unknown link without a form", async () => {
    for (const [setup, text] of [
      ["expired", st.linkExpired],
      ["complete", st.linkUsed],
      ["cli", st.linkUnknown],
    ] as const) {
      openAt(`/#setup=${newToken()}`);
      serve({ "GET /api/session": [noUser(setup), { status: 401, body: { error: "login required", setupRequired: false, setup: "complete" } }] });
      renderGate();
      expect(await screen.findByText(text)).toBeTruthy();
      expect(screen.queryByLabelText(t.userName)).toBeNull();
      fireEvent.click(screen.getByRole("button", { name: st.toLogin }));
      expect(await screen.findByRole("button", { name: t.logIn })).toBeTruthy();
      cleanup();
      vi.restoreAllMocks();
    }
  });

  it("ignores the link in a browser that is logged in", async () => {
    openAt(`/#setup=${newToken()}`);
    serve({ "GET /api/session": [{ status: 200, body: { user: { ...admin, twoFactorEnabled: true } } }] });
    renderGate();
    expect(await screen.findByText("app for root")).toBeTruthy();
    expect(window.location.hash).toBe("");
  });
});

describe("without a setup link", () => {
  it("points to the operator's link while a setup token is set, to the command line otherwise", async () => {
    openAt("/");
    serve({ "GET /api/session": [noUser("token")] });
    renderGate();
    expect(await screen.findByText(setupTextFor("en").linkWaiting)).toBeTruthy();
    expect(screen.queryByText("./ma user add NAME --admin")).toBeNull();

    cleanup();
    serve({ "GET /api/session": [noUser("cli")] });
    renderGate();
    expect(await screen.findByText(setupTextFor("en").cliText)).toBeTruthy();
    expect(screen.getByText("./ma user add NAME --admin")).toBeTruthy();
  });

  it("has German texts", async () => {
    applySettings({ language: "de", locale: "de" });
    const deSetup = setupTextFor("de");
    openAt(`/#setup=${newToken()}`);
    serve({ "GET /api/session": [noUser("token")] });
    renderGate();
    expect(await screen.findByRole("heading", { name: deSetup.title })).toBeTruthy();
    expect(screen.getByRole("button", { name: deSetup.create })).toBeTruthy();
    expect(screen.getByText(deSetup.formText)).toBeTruthy();
  });
});
