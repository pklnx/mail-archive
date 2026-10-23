// The message view on Carbon. The HTML of a mail stays in the sandboxed
// frame exactly as before #168 (the regression test below), remote images
// stay blocked until asked for, and journal mail shows its envelope in an
// accordion. The API is a stub; no network.
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MessageView } from "./MessageView";
import type { Location, MessageDetail } from "./api";
import { applySettings, messagesFor, t } from "./i18n";

const id = "a".repeat(64);

function location(over: Partial<Location> = {}): Location {
  return {
    account: "personal", folder: "INBOX", uid: 7, flags: [], internalDate: null, superseded: false, goneAt: null,
    lastSeenAt: "2026-10-01T10:00:00Z", envelope: null, filled: false, fill: null, import: null, retainUntil: null, ...over,
  };
}

function message(over: Partial<MessageDetail> = {}): MessageDetail {
  return {
    id, size: 1000, subject: "Photos from the weekend", from: "Sam Taylor <sam@example.org>", sentAt: "2026-10-01T09:00:00Z",
    sortAt: "2026-10-01T09:00:00Z", hasAttachment: false, messageId: "<m@example.org>", to: "alex@example.org", cc: "",
    dateHeader: "", text: "Hi Alex,\n\nsee you.\n\n> older\n>> oldest", hasHtml: true, truncated: false, parts: [],
    locations: [location()], retainUntil: null, ...over,
  };
}

let detail: MessageDetail;
beforeEach(() => {
  applySettings({ language: "en", locale: "en" });
  detail = message();
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      const body = path.endsWith("/conversation") ? { messages: [], total: 0, truncated: false } : detail;
      return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  applySettings({ language: "en", locale: "en" });
});

async function show() {
  render(<MessageView id={id} open={() => {}} />);
  return screen.findByRole("article");
}

/** The sandbox exactly as before the move to Carbon: no scripts, no same origin, no forms, no top navigation. */
const sandbox = "allow-popups allow-popups-to-escape-sandbox";

describe("the HTML frame (regression, #168 part 4b)", () => {
  it("keeps its sandbox, referrer policy and src, and blocks remote images until the button", async () => {
    const article = await show();
    const frame = within(article).getByTitle(t.messageContent) as HTMLIFrameElement;
    expect(frame.tagName).toBe("IFRAME");
    expect(frame.getAttribute("sandbox")).toBe(sandbox);
    for (const token of ["allow-scripts", "allow-same-origin", "allow-forms", "allow-top-navigation", "allow-modals"]) {
      expect(frame.getAttribute("sandbox")!.split(/\s+/), token).not.toContain(token);
    }
    expect(frame.getAttribute("referrerpolicy")).toBe("no-referrer");
    // No remote images yet: the server's CSP for this URL allows only data: images.
    expect(frame.getAttribute("src")).toBe(`/api/messages/${id}/html`);
    // Nothing else the frame could inherit or be given.
    expect(frame.hasAttribute("srcdoc")).toBe(false);
    expect(frame.hasAttribute("allow")).toBe(false);
    expect(frame.className).toBe("message__frame");

    fireEvent.click(within(article).getByRole("button", { name: t.loadRemoteImages }));
    const withImages = within(article).getByTitle(t.messageContent) as HTMLIFrameElement;
    // A new frame, not a new src: no history entry for the images.
    expect(withImages).not.toBe(frame);
    expect(withImages.getAttribute("src")).toBe(`/api/messages/${id}/html?images=1`);
    expect(withImages.getAttribute("sandbox")).toBe(sandbox);
    expect(withImages.getAttribute("referrerpolicy")).toBe("no-referrer");
    expect(within(article).queryByRole("button", { name: t.loadRemoteImages })).toBeNull();
  });

  it("explains the remote-image button in a tooltip that does not rename it", async () => {
    const article = await show();
    const button = within(article).getByRole("button", { name: t.loadRemoteImages });
    const hint = document.getElementById(button.getAttribute("aria-describedby") ?? "");
    expect(hint?.textContent).toBe(t.remoteImagesHint);
  });

  it("starts with the images blocked again for the next message and for text/HTML switches", async () => {
    const article = await show();
    fireEvent.click(within(article).getByRole("button", { name: t.showText }));
    expect(within(article).queryByTitle(t.messageContent)).toBeNull();
    expect(within(article).getByText(/see you\./)).toBeTruthy();
    fireEvent.click(within(article).getByRole("button", { name: t.showHTML }));
    expect(within(article).getByTitle(t.messageContent).getAttribute("src")).toBe(`/api/messages/${id}/html`);
  });

  it("shows text-only mail without a frame, with the quote folding", async () => {
    detail = message({ hasHtml: false });
    const article = await show();
    expect(within(article).queryByTitle(t.messageContent)).toBeNull();
    expect(within(article).queryByRole("button", { name: t.loadRemoteImages })).toBeNull();
    expect(within(article).getByText(t.showQuote)).toBeTruthy();
  });
});

describe("the message header", () => {
  it("shows the envelope of journal mail and the origin of imported mail in an accordion, closed", async () => {
    detail = message({
      locations: [
        location({ account: "journal", envelope: { mailFrom: "sam@example.org", rcptTo: ["alex@example.org", "bcc@example.org"], receivedAt: "2026-10-01T09:00:01Z", clientIp: "192.0.2.1", helo: "mx.example.org" } }),
        location({ account: "archive", folder: "INBOX", uid: 8, filled: true, import: { id: 1, label: "old-mails" } }),
      ],
    });
    const article = await show();
    const envelope = within(article).getByRole("button", { name: t.envelope("journal") });
    const origin = within(article).getByRole("button", { name: t.origin("archive") });
    expect(envelope.getAttribute("aria-expanded")).toBe("false");
    expect(origin.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(envelope);
    expect(envelope.getAttribute("aria-expanded")).toBe("true");
    const panel = document.getElementById(envelope.getAttribute("aria-controls")!)!;
    expect(panel.textContent).toContain("bcc@example.org");
    expect(panel.textContent).toContain(t.envelopeHint);
    fireEvent.click(origin);
    expect(document.getElementById(origin.getAttribute("aria-controls")!)!.textContent).toContain(t.originImportedFrom("old-mails"));
  });

  it("explains renumbered and gone locations in tooltips", async () => {
    detail = message({
      locations: [location(), location({ folder: "Old", superseded: true }), location({ folder: "Gone", goneAt: "2026-10-02T00:00:00Z" })],
    });
    const article = await show();
    const hints = [...article.querySelectorAll("[aria-describedby]")].map((el) => document.getElementById(el.getAttribute("aria-describedby")!)?.textContent);
    expect(hints).toContain(t.renumberedHint);
    expect(hints).toContain(t.goneFromServerHint);
    expect(within(article).getByText("personal / INBOX")).toBeTruthy();
  });

  it("speaks German, Carbon's texts included", async () => {
    applySettings({ language: "de", locale: "de" });
    const de = messagesFor("de");
    detail = message({
      locations: [
        location({ account: "journal", envelope: { mailFrom: "", rcptTo: ["alex@example.org"], receivedAt: "2026-10-01T09:00:01Z", clientIp: "", helo: "" } }),
        location({ folder: "Old", superseded: true }),
      ],
      parts: [{ index: 2, contentType: "application/pdf", filename: "Lease.pdf", size: 2048, attachment: true, inline: false }],
      truncated: true,
    });
    const article = await show();
    expect(within(article).getByRole("button", { name: de.loadRemoteImages })).toBeTruthy();
    expect(within(article).getByRole("button", { name: de.envelope("journal") })).toBeTruthy();
    expect(within(article).getByRole("list", { name: de.attachments })).toBeTruthy();
    const html = document.body.innerHTML;
    for (const s of ["Load remote images", "Show text", "Download", "Envelope", "Found in", "Attachments", "renumbered", "title", "Close", "Dismiss"]) {
      expect(html, s).not.toContain(`>${s}`);
    }
    for (const s of ["Remote images can", "The server renumbered", "very large"]) expect(html, s).not.toContain(s);
  });
});
