import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "./i18n";
import { RecoveryCodes } from "./Profile";

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
