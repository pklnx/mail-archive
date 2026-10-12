import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { applySettings, settings, t } from "./i18n";
import { DisplaySettings, RecoveryCodes, formatChoices, formatLabel } from "./Profile";

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
