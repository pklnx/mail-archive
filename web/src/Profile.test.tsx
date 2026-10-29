import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { applySettings, messagesFor, settings, t } from "./i18n";
import { DisplaySettings, ProfilePage, RecoveryCodes, TwoFactorSetup, formatChoices, formatLabel } from "./Profile";
import { expectLoaded, expectLoading } from "./test/loading";

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

  it("is a Modal that only “I have saved the codes” leaves", () => {
    const done = vi.fn();
    render(<RecoveryCodes sheet={sheet} done={done} />);
    const dialog = screen.getByRole("dialog", { name: t.twoFactorRecovery });
    expect(within(dialog).getByText(/abcd-efgh-jkmn-pqr0/)).toBeTruthy();
    expect(within(dialog).getByRole("button", { name: t.carbon.copy })).toBeTruthy();
    // No Cancel; the close button is hidden by layout.css and does nothing.
    expect(within(dialog).getAllByRole("button").map((b) => b.textContent)).not.toContain(t.cancel);
    fireEvent.keyDown(dialog, { key: "Escape" });
    fireEvent.click(within(dialog).getByRole("button", { name: t.carbon.closeModal }));
    fireEvent.mouseDown(dialog.closest(".cds--modal")!);
    expect(done).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog", { name: t.twoFactorRecovery })).toBeTruthy();
    expect(dialog.closest(".cds--modal")!.className).toContain("recovery-modal");
    fireEvent.click(within(dialog).getByRole("button", { name: t.twoFactorCodesSaved }));
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
  let requests: { method: string; url: string; body?: unknown }[];
  let passkeys: { id: number; name: string; createdAt: string; lastUsedAt: string | null }[];
  let apiKeys: unknown[];

  const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  const twoFactorState = () => ({ enabled: twoFactorEnabled, required: false, admin: false, setupPending: false });

  beforeEach(() => {
    vi.useRealTimers();
    twoFactorEnabled = false;
    requests = [];
    passkeys = [];
    apiKeys = [];
    // The passkey form is shown only where the browser supports passkeys.
    vi.stubGlobal("PublicKeyCredential", function PublicKeyCredential() {});
    Object.defineProperty(navigator, "credentials", { value: {}, configurable: true });
    vi.mocked(fetch).mockImplementation(async (input, init) => {
      const method = init?.method ?? "GET";
      const url = String(input);
      requests.push({ method, url, ...(method === "GET" ? {} : { body: init?.body ? JSON.parse(String(init.body)) : undefined }) });
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
          return json({ available: true, origin: window.location.origin, max: 10, passkeys });
        case "POST /api/profile/passkeys/begin":
          return json({ token: "tok", publicKey: { challenge: "AQ", user: { id: "Ag", name: "patrick", displayName: "patrick" } } });
        case "POST /api/profile/passkeys/finish":
          return new Response(null, { status: 204 });
        case "DELETE /api/profile/passkeys/7":
          return json({ credentialId: "AQ" });
        case "PUT /api/profile/password":
          return new Response(null, { status: 204 });
        case "DELETE /api/profile/api-keys/3":
          return new Response(null, { status: 204 });
        case "GET /api/profile/api-keys":
          return json({ max: 10, keys: apiKeys });
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

  const renderPage = () => render(<ProfilePage name="patrick" />);

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

  it("shows skeletons while the sections load, with one status per tab (#287)", async () => {
    let release!: () => void;
    const released = new Promise<void>((r) => {
      release = r;
    });
    const answer = vi.mocked(fetch).getMockImplementation()!;
    vi.mocked(fetch).mockImplementation(async (input, init) => {
      if (/^\/api\/profile\/(2fa|passkeys|api-keys)$/.test(String(input)) && (init?.method ?? "GET") === "GET") await released;
      return answer(input, init);
    });
    renderPage();
    open(t.tabSecurity);
    const security = screen.getByRole("tabpanel", { name: t.tabSecurity });
    // 2FA announces; the passkeys below it load at the same time and only show their skeleton.
    const status = expectLoading(security)!;
    expect(within(security).getByRole("heading", { name: t.twoFactor }).closest("section")!.contains(status)).toBe(true);
    const passkeys = within(security).getByRole("heading", { name: t.passkeys }).closest("section")!;
    expectLoading(passkeys, { announce: false });
    open(t.apiKeys);
    const keys = screen.getByRole("tabpanel", { name: t.apiKeys });
    expect(expectLoading(keys)!.querySelector(".cds--skeleton__heading")).not.toBeNull();
    await act(async () => release());
    expect(await within(keys).findByRole("button", { name: t.apiKeyCreate })).toBeTruthy();
    expectLoaded(keys);
    open(t.tabSecurity);
    expect(await within(security).findByRole("button", { name: t.passkeyAdd })).toBeTruthy();
    expectLoaded(security);
  });

  it("shows each section in its tab", async () => {
    renderPage();
    open(t.tabSecurity);
    const panel = screen.getByRole("tabpanel", { name: t.tabSecurity });
    expect(within(panel).getByRole("button", { name: t.changePassword })).toBeTruthy();
    expect(within(panel).getByRole("heading", { name: t.twoFactor })).toBeTruthy();
    expect(await within(panel).findByRole("button", { name: t.twoFactorSetup })).toBeTruthy();
    expect(await within(panel).findByRole("button", { name: t.passkeyAdd })).toBeTruthy();
    expect(within(panel).getByText(t.twoFactorStateOff)).toBeTruthy();
    expect(screen.queryByRole("combobox", { name: t.language })).toBeNull();

    open(t.apiKeys);
    expect(await within(screen.getByRole("tabpanel", { name: t.apiKeys })).findByRole("button", { name: t.apiKeyCreate })).toBeTruthy();
    expect(screen.queryByRole("button", { name: t.changePassword })).toBeNull();
  });

  it("keeps one primary button in the Security tab, and the 2FA steps primary on their own (#211)", async () => {
    const kind = (name: string) => screen.getByRole("button", { name }).className;
    renderPage();
    await startSetup();
    const panel = screen.getByRole("tabpanel", { name: t.tabSecurity });
    expect(kind(t.changePassword)).toContain("cds--btn--primary");
    expect(kind(t.twoFactorConfirm)).toContain("cds--btn--tertiary");
    // The CodeSnippet's copy button has the primary class too, but Carbon styles it as a copy button.
    expect(panel.querySelectorAll(".cds--btn--primary:not(.cds--copy-btn)")).toHaveLength(1);
    cleanup();

    // The forced setup after login (Auth) shows TwoFactorSetup without status:
    // its steps are the screen's main action.
    render(<TwoFactorSetup user="patrick" />);
    expect(await screen.findByRole("button", { name: t.twoFactorSetup })).toBeTruthy();
    expect(kind(t.twoFactorSetup)).toContain("cds--btn--primary");
    fireEvent.click(screen.getByRole("button", { name: t.twoFactorSetup }));
    expect(await screen.findByText("JBSWY3DPEHPK3PXP")).toBeTruthy();
    expect(kind(t.twoFactorConfirm)).toContain("cds--btn--primary");
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

  it("shows the recovery codes in a Modal until they were saved", async () => {
    renderPage();
    await startSetup();
    fireEvent.click(screen.getByRole("button", { name: t.twoFactorConfirm }));
    const dialog = await screen.findByRole("dialog", { name: t.twoFactorRecovery });
    expect(within(dialog).getByText(/abcd-efgh-jkmn-pqr9/)).toBeTruthy();
    expect(requests.filter((r) => r.method !== "GET")).toEqual([
      { method: "POST", url: "/api/profile/2fa/setup", body: undefined },
      { method: "POST", url: "/api/profile/2fa/confirm", body: { code: "123456" } },
    ]);
    // The section behind it shows the new state already.
    expect(screen.getByText(t.twoFactorStateOn)).toBeTruthy();
    fireEvent.keyDown(dialog, { key: "Escape" });
    expect(screen.getByRole("dialog", { name: t.twoFactorRecovery })).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: t.twoFactorCodesSaved }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByText(/abcd-efgh-jkmn-pqr9/)).toBeNull();
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

  /** Opens the Add passkey Modal (on the security tab) and returns it. */
  const passkeyDialog = async () => {
    open(t.tabSecurity);
    fireEvent.click(await screen.findByRole("button", { name: t.passkeyAdd }));
    return screen.getByRole("dialog", { name: t.passkeyAdd });
  };
  const closeDialog = (dialog: HTMLElement) => {
    fireEvent.click(within(dialog).getByRole("button", { name: t.cancel }));
    expect(screen.queryByRole("dialog")).toBeNull();
  };

  it("asks for the 2FA code for a new key and passkey after 2FA was enabled on the profile", async () => {
    renderPage();
    const keyForm = await formOf(t.apiKeyName);
    await waitFor(() => expect(requests.filter((r) => r.method === "GET" && r.url === "/api/profile/2fa")).toHaveLength(3));
    expect(codeField(keyForm)).toBeNull();
    let dialog = await passkeyDialog();
    expect(codeField(dialog)).toBeNull();
    closeDialog(dialog);

    await startSetup();
    fireEvent.click(screen.getByRole("button", { name: t.twoFactorConfirm }));
    fireEvent.click(await screen.findByRole("button", { name: t.twoFactorCodesSaved }));
    await waitFor(() => expect(requests.filter((r) => r.method === "GET" && r.url === "/api/profile/2fa")).toHaveLength(5));
    dialog = await passkeyDialog();
    expect(codeField(dialog)).not.toBeNull();
    expect(within(dialog).getByText(t.passkeyConfirm2FA)).toBeTruthy();
    closeDialog(dialog);

    open(t.apiKeys);
    await waitFor(() => expect(codeField(keyForm)).not.toBeNull());
    expect(within(keyForm).getByText(t.apiKeyConfirm2FA)).toBeTruthy();
  });

  it("stops asking for the 2FA code after 2FA was disabled on the profile", async () => {
    twoFactorEnabled = true;
    renderPage();
    const keyForm = await formOf(t.apiKeyName);
    await waitFor(() => expect(codeField(keyForm)).not.toBeNull());
    await waitFor(() => expect(requests.filter((r) => r.method === "GET" && r.url === "/api/profile/2fa")).toHaveLength(3));
    let dialog = await passkeyDialog();
    expect(codeField(dialog)).not.toBeNull();
    closeDialog(dialog);

    const disable = (await screen.findByRole("button", { name: t.twoFactorDisable })).closest("form")!;
    fireEvent.change(within(disable).getByLabelText(t.currentPassword), { target: { value: "correct horse battery" } });
    fireEvent.change(within(disable).getByLabelText(t.twoFactorCode), { target: { value: "123456" } });
    fireEvent.click(within(disable).getByRole("button", { name: t.twoFactorDisable }));
    expect(await screen.findByRole("button", { name: t.twoFactorSetup })).toBeTruthy();
    expect(requests.filter((r) => r.method === "DELETE")).toEqual([
      { method: "DELETE", url: "/api/profile/2fa", body: { currentPassword: "correct horse battery", code: "123456" } },
    ]);

    await waitFor(() => expect(requests.filter((r) => r.method === "GET" && r.url === "/api/profile/2fa")).toHaveLength(5));
    dialog = await passkeyDialog();
    expect(codeField(dialog)).toBeNull();
    closeDialog(dialog);
    await waitFor(() => expect(codeField(keyForm)).toBeNull());
    expect(within(keyForm).getByText(t.apiKeyConfirm)).toBeTruthy();
  });

  it("changes the password with the same request and says so", async () => {
    renderPage();
    open(t.tabSecurity);
    const form = (await screen.findByRole("button", { name: t.changePassword })).closest("form")!;
    const field = (label: string) => within(form).getByLabelText(label) as HTMLInputElement;
    fireEvent.change(field(t.currentPassword), { target: { value: "old password 1" } });
    fireEvent.change(field(t.newPassword), { target: { value: "short" } });
    fireEvent.change(field(t.repeatPassword), { target: { value: "short" } });
    fireEvent.submit(form);
    expect(within(form).getByRole("alert").textContent).toBe(t.passwordRules);
    fireEvent.change(field(t.newPassword), { target: { value: "a new long password" } });
    fireEvent.submit(form);
    expect(within(form).getByRole("alert").textContent).toBe(t.passwordsDiffer);
    expect(requests.filter((r) => r.method !== "GET")).toEqual([]);

    fireEvent.change(field(t.repeatPassword), { target: { value: "a new long password" } });
    fireEvent.submit(form);
    expect((await screen.findByRole("status")).textContent).toContain(t.passwordChanged);
    expect(requests.filter((r) => r.method !== "GET")).toEqual([
      { method: "PUT", url: "/api/profile/password", body: { current: "old password 1", new: "a new long password" } },
    ]);
    expect(field(t.currentPassword).value).toBe("");
    expect(field(t.newPassword).value).toBe("");
    expect(field(t.repeatPassword).value).toBe("");
  });

  it("adds a passkey through the Modal with the same requests", async () => {
    const create = vi.fn(async () => ({
      type: "public-key",
      id: "AQ",
      rawId: new Uint8Array([1]).buffer,
      response: { clientDataJSON: new Uint8Array([2]).buffer, attestationObject: new Uint8Array([3]).buffer },
    }));
    Object.defineProperty(navigator, "credentials", { value: { create }, configurable: true });
    renderPage();
    const dialog = await passkeyDialog();
    const submit = within(dialog).getByRole("button", { name: t.passkeyAdd }) as HTMLButtonElement;
    // Nothing to send before the name and the password are there.
    expect(submit.disabled).toBe(true);
    fireEvent.change(within(dialog).getByLabelText(t.passkeyName), { target: { value: " iPhone " } });
    fireEvent.change(within(dialog).getByLabelText(t.currentPassword), { target: { value: "correct horse battery" } });
    expect(submit.disabled).toBe(false);
    fireEvent.click(submit);
    expect((await screen.findByRole("status")).textContent).toContain(t.passkeyAdded("iPhone"));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(create).toHaveBeenCalledOnce();
    expect(requests.filter((r) => r.method !== "GET")).toEqual([
      { method: "POST", url: "/api/profile/passkeys/begin", body: { name: "iPhone", currentPassword: "correct horse battery", code: "" } },
      {
        method: "POST",
        url: "/api/profile/passkeys/finish",
        body: { token: "tok", credential: expect.objectContaining({ id: "AQ", response: expect.objectContaining({ clientDataJSON: "Ag", attestationObject: "Aw" }) }) },
      },
    ]);
  });

  it("keeps the passkey Modal open and clears the password when the browser cancels", async () => {
    const create = vi.fn(async () => {
      throw new DOMException("cancelled", "NotAllowedError");
    });
    Object.defineProperty(navigator, "credentials", { value: { create }, configurable: true });
    renderPage();
    const dialog = await passkeyDialog();
    fireEvent.change(within(dialog).getByLabelText(t.passkeyName), { target: { value: "iPhone" } });
    fireEvent.change(within(dialog).getByLabelText(t.currentPassword), { target: { value: "correct horse battery" } });
    fireEvent.click(within(dialog).getByRole("button", { name: t.passkeyAdd }));
    expect((await within(dialog).findByText(t.passkeyCancelled)).getAttribute("role")).toBe("alert");
    expect((within(dialog).getByLabelText(t.currentPassword) as HTMLInputElement).value).toBe("");
    expect((within(dialog).getByLabelText(t.passkeyName) as HTMLInputElement).value).toBe("iPhone");
    expect(requests.some((r) => r.url === "/api/profile/passkeys/finish")).toBe(false);
  });

  it("removes a passkey only after asking", async () => {
    passkeys = [{ id: 7, name: "YubiKey", createdAt: "2026-10-01T00:00:00Z", lastUsedAt: null }];
    renderPage();
    open(t.tabSecurity);
    const row = (await screen.findByText("YubiKey")).closest("tr")!;
    fireEvent.click(within(row).getByRole("button", { name: t.passkeyRemove }));
    let dialog = screen.getByRole("dialog", { name: t.passkeyRemoveTitle("YubiKey") });
    expect(dialog.closest(".cds--modal")!.className).toContain("cds--modal--danger");
    fireEvent.click(within(dialog).getByRole("button", { name: t.cancel }));
    expect(requests.filter((r) => r.method === "DELETE")).toEqual([]);

    fireEvent.click(within(row).getByRole("button", { name: t.passkeyRemove }));
    dialog = screen.getByRole("dialog", { name: t.passkeyRemoveTitle("YubiKey") });
    fireEvent.click(within(dialog).getByRole("button", { name: t.passkeyRemove }));
    await waitFor(() =>
      expect(requests.filter((r) => r.method === "DELETE")).toEqual([{ method: "DELETE", url: "/api/profile/passkeys/7", body: undefined }]),
    );
  });

  it("revokes an API key only after asking", async () => {
    apiKeys = [
      { id: 3, name: "backup", hint: "wxyz", scopes: ["read"], createdAt: "2026-10-01T00:00:00Z", expiresAt: null, lastUsedAt: null, revokedAt: null, state: "active" },
    ];
    renderPage();
    open(t.apiKeys);
    const row = (await screen.findByText("backup")).closest("tr")!;
    fireEvent.click(within(row).getByRole("button", { name: t.apiKeyRevoke }));
    const dialog = screen.getByRole("dialog", { name: t.apiKeyRevokeTitle("backup") });
    expect(requests.filter((r) => r.method === "DELETE")).toEqual([]);
    fireEvent.click(within(dialog).getByRole("button", { name: t.apiKeyRevoke }));
    await waitFor(() =>
      expect(requests.filter((r) => r.method === "DELETE")).toEqual([{ method: "DELETE", url: "/api/profile/api-keys/3", body: undefined }]),
    );
  });

  describe("German", () => {
    // Carbon's own English defaults that must not show in German.
    const english = [/\bClose\b/, /Copy to clipboard/, /Copied/, /Show password/, /Hide password/, /\bLoading\b/, /Focus sentinel/,
      /\bdanger\b/, /Scroll (left|right)/, /Show (more|less)/, /\bsuccess\b/i, /\bOptions\b/];

    function noEnglish() {
      const attrs = ["aria-label", "title", "placeholder", "alt", "aria-description"];
      const values = [...document.body.querySelectorAll("*")].flatMap((el) => attrs.map((a) => el.getAttribute(a) ?? ""));
      const text = [document.body.textContent ?? "", ...values].join("\n");
      for (const e of english) expect(text, String(e)).not.toMatch(e);
    }

    it("shows the tabs, forms, tables and Modals in German", async () => {
      passkeys = [{ id: 7, name: "YubiKey", createdAt: "2026-10-01T00:00:00Z", lastUsedAt: null }];
      apiKeys = [
        { id: 3, name: "skript", hint: "wxyz", scopes: ["read"], createdAt: "2026-10-01T00:00:00Z", expiresAt: null, lastUsedAt: null, revokedAt: null, state: "active" },
      ];
      applySettings({ language: "de", locale: "de" });
      try {
        const de = messagesFor("de");
        renderPage();
        expect(screen.getByRole("heading", { level: 1, name: de.profile })).toBeTruthy();
        expect(screen.getAllByRole("tab").map((x) => x.textContent)).toEqual([de.tabDisplay, de.tabSecurity, de.apiKeys]);
        expect(screen.getByRole("combobox", { name: de.language })).toBeTruthy();
        await screen.findByText("YubiKey");
        await screen.findByText("skript");
        noEnglish();

        fireEvent.click(screen.getByRole("tab", { name: de.tabSecurity }));
        expect(screen.getAllByRole("button", { name: de.carbon.showPassword }).length).toBeGreaterThan(0);
        expect(await screen.findByText(de.twoFactorStateOff)).toBeTruthy();
        fireEvent.click(screen.getByRole("button", { name: de.twoFactorSetup }));
        expect(await screen.findByText("JBSWY3DPEHPK3PXP")).toBeTruthy();
        noEnglish();
        fireEvent.change(screen.getByRole("textbox", { name: de.twoFactorCode }), { target: { value: "123456" } });
        fireEvent.click(screen.getByRole("button", { name: de.twoFactorConfirm }));
        const codes = await screen.findByRole("dialog", { name: de.twoFactorRecovery });
        expect(within(codes).getByRole("button", { name: de.twoFactorCodesSaved })).toBeTruthy();
        noEnglish();
        fireEvent.click(within(codes).getByRole("button", { name: de.twoFactorCodesSaved }));

        fireEvent.click(await screen.findByRole("button", { name: de.passkeyAdd }));
        expect(screen.getByRole("dialog", { name: de.passkeyAdd })).toBeTruthy();
        noEnglish();
        fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: de.cancel }));
        fireEvent.click(screen.getByRole("button", { name: de.passkeyRemove }));
        expect(screen.getByRole("dialog", { name: de.passkeyRemoveTitle("YubiKey") })).toBeTruthy();
        noEnglish();
        fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: de.cancel }));

        fireEvent.click(screen.getByRole("tab", { name: de.apiKeys }));
        expect(screen.getByRole("columnheader", { name: de.colLastUsed })).toBeTruthy();
        const form = (await screen.findByLabelText(de.apiKeyName)).closest("form")!;
        fireEvent.change(within(form).getByLabelText(de.apiKeyName), { target: { value: "backup" } });
        fireEvent.change(within(form).getByLabelText(de.currentPassword), { target: { value: "correct horse battery" } });
        fireEvent.submit(form);
        expect(await screen.findByText("ma_secretkeyvaluewxyz")).toBeTruthy();
        expect(screen.getAllByRole("button", { name: de.carbon.copy }).length).toBeGreaterThan(0);
        noEnglish();
        await act(async () => {});
      } finally {
        applySettings({ language: "en", locale: "en" });
      }
    });
  });
});
