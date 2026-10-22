import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { applySettings, settings, t } from "./i18n";
import { DisplaySettings, ProfilePage, RecoveryCodes, formatChoices, formatLabel } from "./Profile";

const codes = Array.from({ length: 10 }, (_, i) => `abcd-efgh-jkmn-pqr${i}`);
const sheet = { user: "patrick", origin: "https://mail.example.org", createdAt: new Date(2026, 9, 12), codes };

let blobs: Blob[];
let clicked: HTMLAnchorElement[];

beforeEach(() => {
  vi.useFakeTimers();
  blobs = [];
  clicked = [];
  // jsdom has no object URLs and does not navigate to blob: links.
  URL.createObjectURL = vi.fn((b: Blob | MediaSource) => {
    blobs.push(b as Blob);
    return `blob:test/${blobs.length}`;
  });
  URL.revokeObjectURL = vi.fn();
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
    clicked.push(this);
  });
  vi.spyOn(globalThis, "fetch");
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("RecoveryCodes", () => {
  it("shows the codes and downloads them without a request", async () => {
    const done = vi.fn();
    render(<RecoveryCodes sheet={sheet} done={done} />);
    expect(screen.getByText(/abcd-efgh-jkmn-pqr9/)).toBeTruthy();

    fireEvent.click(screen.getByText(t.twoFactorDownloadPdf));
    expect(blobs[0]!.type).toBe("application/pdf");
    expect(new TextDecoder("latin1").decode(await blobs[0]!.arrayBuffer())).toMatch(/^%PDF-1\.4\n/);
    expect(clicked[0]!.download).toBe("mail-archive-recovery-codes-patrick-2026-10-12.pdf");
    expect(clicked[0]!.href).toBe("blob:test/1");
    expect(clicked[0]!.isConnected).toBe(false);

    fireEvent.click(screen.getByText(t.twoFactorDownloadTxt));
    expect(blobs[1]!.type).toBe("text/plain;charset=utf-8");
    expect(await blobs[1]!.text()).toContain(" 1   abcd-efgh-jkmn-pqr0");
    expect(clicked[1]!.download).toBe("mail-archive-recovery-codes-patrick-2026-10-12.txt");

    expect(URL.revokeObjectURL).not.toHaveBeenCalled();
    vi.advanceTimersByTime(10_000);
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:test/1");
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:test/2");

    expect(fetch).not.toHaveBeenCalled();
    expect(done).not.toHaveBeenCalled();
    fireEvent.click(screen.getByText(t.twoFactorCodesSaved));
    expect(done).toHaveBeenCalledOnce();
  });
});

describe("DisplaySettings", () => {
  const auto = { language: "auto", locale: "auto" };
  let reload: () => void;
  let requests: { method: string; url: string; body: unknown }[];
  let fail: boolean;

  beforeEach(() => {
    vi.useRealTimers();
    applySettings(auto, ["en-US", "en"]);
    reload = vi.fn();
    requests = [];
    fail = false;
    vi.mocked(fetch).mockImplementation(async (url, init) => {
      requests.push({ method: init?.method ?? "GET", url: String(url), body: JSON.parse(String(init?.body)) });
      return fail
        ? new Response(JSON.stringify({ error: "boom" }), { status: 500, headers: { "Content-Type": "application/json" } })
        : new Response(null, { status: 204 });
    });
  });
  afterEach(() => {
    localStorage.clear();
    applySettings(auto);
  });

  const select = (label: string) => screen.getByLabelText(label) as HTMLSelectElement;
  const options = (label: string) => [...select(label).options].map((o) => o.value);

  it("lists the languages and formats", () => {
    applySettings({ language: "auto", locale: "de-LI" }, ["en-US"]);
    render(<DisplaySettings reload={reload} />);
    expect(options(t.language)).toEqual(["auto", "en", "de"]);
    expect([...select(t.language).options].map((o) => o.text)).toEqual([t.settingAuto, "English", "Deutsch"]);
    // A stored format outside the list is one more option.
    expect(options(t.formats)).toEqual(["auto", ...formatChoices, "de-LI"]);
    expect(select(t.formats).value).toBe("de-LI");
  });

  it("shows an unusable stored value as automatic", () => {
    applySettings({ language: "fr", locale: "x_y" }, ["en-US"]);
    render(<DisplaySettings reload={reload} />);
    expect(select(t.language).value).toBe("auto");
    expect(select(t.formats).value).toBe("auto");
    expect(options(t.formats)).toEqual(["auto", ...formatChoices]);
  });

  it("saves one setting and reloads only when the page changes", async () => {
    render(<DisplaySettings reload={reload} />);
    // English on an English browser looks the same: no reload.
    fireEvent.change(select(t.language), { target: { value: "en" } });
    await waitFor(() => expect(settings).toEqual({ language: "en", locale: "auto" }));
    expect(requests).toEqual([{ method: "PATCH", url: "/api/profile", body: { language: "en" } }]);
    expect(reload).not.toHaveBeenCalled();

    fireEvent.change(select(t.formats), { target: { value: "de-AT" } });
    await waitFor(() => expect(reload).toHaveBeenCalledTimes(1));
    expect(requests[1]!.body).toEqual({ locale: "de-AT" });
    expect(settings).toEqual({ language: "en", locale: "de-AT" });
  });

  it("shows an error and keeps the old value", async () => {
    fail = true;
    render(<DisplaySettings reload={reload} />);
    fireEvent.change(select(t.language), { target: { value: "de" } });
    expect((await screen.findByRole("alert")).textContent).toContain("boom");
    expect(select(t.language).value).toBe("auto");
    expect(settings).toEqual(auto);
    expect(reload).not.toHaveBeenCalled();
  });
});

describe("formatLabel", () => {
  it("names the format and shows a date and a number", () => {
    expect(formatLabel("de-DE", "de")).toBe("Deutsch (Deutschland) · 12.10.2026 · 1.234,5");
    expect(formatLabel("en-US", "en")).toBe("American English · Oct 12, 2026 · 1,234.5");
  });
  it("falls back to the tag without Intl.DisplayNames", () => {
    vi.spyOn(Intl, "DisplayNames").mockImplementation(() => {
      throw new TypeError("not supported");
    });
    expect(formatLabel("en-GB", "en")).toMatch(/^en-GB · /);
  });
});

describe("ProfilePage", () => {
  let twoFactorEnabled: boolean;
  let requests: { method: string; url: string }[];

  const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  const twoFactorState = () => ({ enabled: twoFactorEnabled, required: false, admin: false, setupPending: false });

  beforeEach(() => {
    vi.useRealTimers();
    twoFactorEnabled = false;
    requests = [];
    // The passkey form is shown only where the browser supports passkeys.
    vi.stubGlobal("PublicKeyCredential", function PublicKeyCredential() {});
    Object.defineProperty(navigator, "credentials", { value: {}, configurable: true });
    vi.mocked(fetch).mockImplementation(async (input, init) => {
      const method = init?.method ?? "GET";
      const url = String(input);
      requests.push({ method, url });
      switch (`${method} ${url}`) {
        case "GET /api/profile/2fa":
          return json(twoFactorState());
        case "POST /api/profile/2fa/setup":
          return json({ ...twoFactorState(), setupPending: true, secret: "JBSWY3DPEHPK3PXP" });
        case "POST /api/profile/2fa/confirm":
          twoFactorEnabled = true;
          return json({ enabled: true, recoveryCodes: codes });
        case "DELETE /api/profile/2fa":
          twoFactorEnabled = false;
          return new Response(null, { status: 204 });
        case "GET /api/profile/passkeys":
          return json({ available: true, origin: window.location.origin, max: 10, passkeys: [] });
        case "GET /api/profile/api-keys":
          return json({ max: 10, keys: [] });
        case "POST /api/profile/api-keys":
          return json({
            id: 1, name: "backup", hint: "wxyz", scopes: ["read"], createdAt: "2026-10-22T10:00:00Z",
            expiresAt: null, lastUsedAt: null, revokedAt: null, state: "active", key: "ma_secretkeyvaluewxyz",
          });
      }
      throw new Error(`unexpected request ${method} ${url}`);
    });
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    delete (navigator as { credentials?: unknown }).credentials;
  });

  const tab = (name: string) => screen.getByRole("tab", { name });
  const open = (name: string) => fireEvent.click(tab(name));
  /** The form that holds the field with this label. */
  const formOf = async (label: string) => (await screen.findByLabelText(label)).closest("form")!;
  /** Kept in the DOM but out of view, as a switch away from its tab leaves it. */
  const inHiddenPanel = (el: HTMLElement) => el.closest<HTMLElement>('[role="tabpanel"]')!.hidden;
  const codeField = (form: HTMLElement) => within(form).queryByLabelText(t.twoFactorCode);

  const renderPage = () => render(<ProfilePage name="patrick" close={() => undefined} />);

  it("starts on Display with three tabs", async () => {
    renderPage();
    expect(screen.getAllByRole("tab").map((x) => x.textContent)).toEqual([t.tabDisplay, t.tabSecurity, t.apiKeys]);
    expect(screen.getByRole("tablist", { name: t.profileSections })).toBeTruthy();
    expect(tab(t.tabDisplay).getAttribute("aria-selected")).toBe("true");
    expect(screen.getByRole("combobox", { name: t.language })).toBeTruthy();
    expect(screen.queryByRole("button", { name: t.changePassword })).toBeNull();
    expect(screen.queryByRole("button", { name: t.apiKeyCreate })).toBeNull();
    // The hidden sections are mounted and load anyway.
    await waitFor(() => expect(requests.map((r) => r.url)).toEqual(expect.arrayContaining(["/api/profile/passkeys", "/api/profile/api-keys"])));
  });

  it("shows each section in its tab", async () => {
    renderPage();
    open(t.tabSecurity);
    const panel = screen.getByRole("tabpanel", { name: t.tabSecurity });
    expect(within(panel).getByRole("button", { name: t.changePassword })).toBeTruthy();
    expect(within(panel).getByRole("heading", { name: t.twoFactor })).toBeTruthy();
    expect(await within(panel).findByRole("button", { name: t.twoFactorSetup })).toBeTruthy();
    expect(await within(panel).findByRole("button", { name: t.passkeyAdd })).toBeTruthy();
    expect(screen.queryByRole("combobox", { name: t.language })).toBeNull();

    open(t.apiKeys);
    expect(await within(screen.getByRole("tabpanel", { name: t.apiKeys })).findByRole("button", { name: t.apiKeyCreate })).toBeTruthy();
    expect(screen.queryByRole("button", { name: t.changePassword })).toBeNull();
  });

  const startSetup = async () => {
    open(t.tabSecurity);
    fireEvent.click(await screen.findByRole("button", { name: t.twoFactorSetup }));
    expect(await screen.findByText("JBSWY3DPEHPK3PXP")).toBeTruthy();
    fireEvent.change(screen.getByRole("textbox", { name: t.twoFactorCode }), { target: { value: "123456" } });
  };

  it("keeps a started 2FA setup when switching tabs", async () => {
    renderPage();
    await startSetup();
    open(t.tabDisplay);
    expect(inHiddenPanel(screen.getByText("JBSWY3DPEHPK3PXP"))).toBe(true);
    open(t.tabSecurity);
    expect(screen.getByText("JBSWY3DPEHPK3PXP")).toBeTruthy();
    expect((screen.getByRole("textbox", { name: t.twoFactorCode }) as HTMLInputElement).value).toBe("123456");
  });

  it("keeps the recovery codes when switching tabs", async () => {
    renderPage();
    await startSetup();
    fireEvent.click(screen.getByRole("button", { name: t.twoFactorConfirm }));
    expect(await screen.findByRole("button", { name: t.twoFactorCodesSaved })).toBeTruthy();
    open(t.tabDisplay);
    expect(screen.queryByRole("button", { name: t.twoFactorCodesSaved })).toBeNull();
    open(t.tabSecurity);
    expect(screen.getByRole("button", { name: t.twoFactorCodesSaved })).toBeTruthy();
    expect(screen.getByText(/abcd-efgh-jkmn-pqr9/)).toBeTruthy();
  });

  it("keeps a new API key until Done when switching tabs", async () => {
    renderPage();
    open(t.apiKeys);
    const form = await formOf(t.apiKeyName);
    fireEvent.change(within(form).getByLabelText(t.apiKeyName), { target: { value: "backup" } });
    fireEvent.change(within(form).getByLabelText(t.currentPassword), { target: { value: "correct horse battery" } });
    fireEvent.click(within(form).getByRole("button", { name: t.apiKeyCreate }));
    expect(await screen.findByText("ma_secretkeyvaluewxyz")).toBeTruthy();
    open(t.tabDisplay);
    expect(inHiddenPanel(screen.getByText("ma_secretkeyvaluewxyz"))).toBe(true);
    open(t.apiKeys);
    expect(screen.getByText("ma_secretkeyvaluewxyz")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: t.done }));
    expect(screen.queryByText("ma_secretkeyvaluewxyz")).toBeNull();
  });

  it("asks for the 2FA code for a new key and passkey after 2FA was enabled on the profile", async () => {
    renderPage();
    const keyForm = await formOf(t.apiKeyName);
    const passkeyForm = await formOf(t.passkeyName);
    await waitFor(() => expect(requests.filter((r) => r.url === "/api/profile/2fa")).toHaveLength(3));
    expect(codeField(keyForm)).toBeNull();
    expect(codeField(passkeyForm)).toBeNull();

    await startSetup();
    fireEvent.click(screen.getByRole("button", { name: t.twoFactorConfirm }));
    expect(await screen.findByRole("button", { name: t.twoFactorCodesSaved })).toBeTruthy();
    // The passkey form is in this tab, below the recovery codes.
    await waitFor(() => expect(codeField(passkeyForm)).not.toBeNull());
    expect(within(passkeyForm).getByText(t.passkeyConfirm2FA)).toBeTruthy();

    open(t.apiKeys);
    await waitFor(() => expect(codeField(keyForm)).not.toBeNull());
    expect(within(keyForm).getByText(t.apiKeyConfirm2FA)).toBeTruthy();
  });

  it("stops asking for the 2FA code after 2FA was disabled on the profile", async () => {
    twoFactorEnabled = true;
    renderPage();
    const keyForm = await formOf(t.apiKeyName);
    const passkeyForm = await formOf(t.passkeyName);
    await waitFor(() => expect(codeField(keyForm)).not.toBeNull());
    await waitFor(() => expect(codeField(passkeyForm)).not.toBeNull());

    open(t.tabSecurity);
    const disable = (await screen.findByRole("button", { name: t.twoFactorDisable })).closest("form")!;
    fireEvent.change(within(disable).getByLabelText(t.currentPassword), { target: { value: "correct horse battery" } });
    fireEvent.change(within(disable).getByLabelText(t.twoFactorCode), { target: { value: "123456" } });
    fireEvent.click(within(disable).getByRole("button", { name: t.twoFactorDisable }));
    expect(await screen.findByRole("button", { name: t.twoFactorSetup })).toBeTruthy();

    await waitFor(() => expect(codeField(passkeyForm)).toBeNull());
    await waitFor(() => expect(codeField(keyForm)).toBeNull());
    expect(within(keyForm).getByText(t.apiKeyConfirm)).toBeTruthy();
  });
});
