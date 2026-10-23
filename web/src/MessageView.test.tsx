// The message view on Carbon. The HTML of a mail stays in the sandboxed
// frame exactly as before #168 (the regression test below), remote images
// stay blocked until asked for, and journal mail shows its envelope in an
// accordion. The API is a stub; no network.
// #200: on phones the header is compact (the rest behind Details) and the
// conversation is folded around the open message.
// #203: the frame allows the same origin (never scripts), so the app can
// size it to its content (frameFit.ts); there is no gate any more.
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MessageView } from "./MessageView";
import type { ConversationEntry, Location, MessageDetail } from "./api";
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

/** matchMedia that answers the two-pane query (48rem): the desktop layout, or the phone one. */
function twoPane(on: boolean) {
  vi.stubGlobal("matchMedia", (query: string) =>
    ({
      matches: on && query.includes("48rem"),
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }) as MediaQueryList,
  );
}

let detail: MessageDetail;
let conversation: ConversationEntry[];
beforeEach(() => {
  applySettings({ language: "en", locale: "en" });
  detail = message();
  conversation = [];
  // The desktop layout unless a test says otherwise.
  twoPane(true);
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      const body = path.endsWith("/conversation") ? { messages: conversation, total: conversation.length, truncated: false } : detail;
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

/** The sandbox exactly: the same origin (#203, measuring) and popups (links); no scripts, no forms, no top navigation. */
const sandbox = "allow-same-origin allow-popups allow-popups-to-escape-sandbox";
/** Never in the sandbox (regression): scripts would undo it together with the same origin. */
const forbidden = ["allow-scripts", "allow-forms", "allow-top-navigation", "allow-top-navigation-by-user-activation", "allow-modals", "allow-downloads"];

describe("the HTML frame (regression, #168 part 4b)", () => {
  it("keeps its sandbox, referrer policy and src, and blocks remote images until the button", async () => {
    const article = await show();
    const frame = within(article).getByTitle(t.messageContent) as HTMLIFrameElement;
    expect(frame.tagName).toBe("IFRAME");
    expect(frame.getAttribute("sandbox")).toBe(sandbox);
    for (const token of forbidden) expect(frame.getAttribute("sandbox")!.split(/\s+/), token).not.toContain(token);
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
    for (const token of forbidden) expect(withImages.getAttribute("sandbox")!.split(/\s+/), token).not.toContain(token);
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

describe("the compact header on phones (#200)", () => {
  beforeEach(() => twoPane(false));

  it("shows subject, From and Date, and puts the rest behind a closed Details toggle", async () => {
    detail = message({
      cc: "robin@example.org",
      locations: [location({ account: "journal", envelope: { mailFrom: "sam@example.org", rcptTo: ["alex@example.org"], receivedAt: "2026-10-01T09:00:01Z", clientIp: "", helo: "" } })],
    });
    const article = await show();
    expect(within(article).getByRole("heading", { level: 1 }).textContent).toBe(detail.subject);
    const terms = () => within(article).queryAllByRole("term").map((el) => el.textContent);
    expect(terms()).toEqual([t.from, t.date]);
    const toggle = within(article).getByRole("button", { name: t.details });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    const panel = document.getElementById(toggle.getAttribute("aria-controls")!)!;
    expect(panel.hidden).toBe(true);
    expect(within(article).queryByRole("button", { name: t.envelope("journal") })).toBeNull();
    const values = () => within(article).queryAllByRole("definition").map((el) => el.textContent);
    expect(values()).not.toContain("alex@example.org");

    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(panel.hidden).toBe(false);
    expect(terms()).toEqual([t.from, t.date, t.to, t.cc, t.foundIn, t.retentionEnds]);
    expect(values()).toContain("alex@example.org");
    expect(within(article).getByText("journal / INBOX")).toBeTruthy();
    expect(within(article).getByRole("button", { name: t.envelope("journal") })).toBeTruthy();
    // Each field once: no hidden copy for the other layout.
    expect(within(article).getAllByText("robin@example.org")).toHaveLength(1);
  });

  it("has the actions in one row: icon buttons named as before, remote images as text", async () => {
    const article = await show();
    const toggle = within(article).getByRole("button", { name: t.showText });
    expect(within(article).getByRole("button", { name: t.loadRemoteImages }).textContent).toBe(t.loadRemoteImages);
    const download = within(article).getByRole("link", { name: t.downloadEml });
    expect(download.getAttribute("href")).toBe(`/api/messages/${id}/raw`);
    expect(download.hasAttribute("download")).toBe(true);
    fireEvent.click(toggle);
    expect(within(article).getByRole("button", { name: t.showHTML })).toBeTruthy();
    expect(within(article).queryByTitle(t.messageContent)).toBeNull();
  });

  it("keeps the desktop header without Details", async () => {
    twoPane(true);
    const article = await show();
    expect(within(article).queryByRole("button", { name: t.details })).toBeNull();
    expect(within(article).getAllByRole("term").map((el) => el.textContent)).toEqual([t.from, t.to, t.date, t.foundIn, t.retentionEnds]);
    expect(within(article).getByRole("button", { name: t.showText }).textContent).toBe(t.showText);
  });
});

describe("the conversation fold (#200)", () => {
  const entry = (n: number, relation: ConversationEntry["relation"] = "thread"): ConversationEntry => ({
    id: relation === "self" ? id : String(n).repeat(64).slice(0, 64),
    subject: `Message ${n}`,
    from: "Sam Taylor <sam@example.org>",
    sentAt: `2026-10-0${n}T09:00:00Z`,
    sortAt: `2026-10-0${n}T09:00:00Z`,
    relation,
  });
  const rows = async () => {
    const section = await screen.findByRole("region", { name: t.conversation });
    return { section, entries: () => within(section).getAllByRole("listitem").map((li) => li.textContent) };
  };

  it("shows the open message with one neighbour on each side, the rest behind the toggles", async () => {
    conversation = [1, 2, 3, 4, 5, 6].map((n) => entry(n, n === 4 ? "self" : "thread"));
    await show();
    const { section, entries } = await rows();
    expect(entries()).toHaveLength(3);
    expect(entries()[0]).toContain("Message 3");
    expect(entries()[2]).toContain("Message 5");
    const earlier = within(section).getByRole("button", { name: "Show 2 earlier messages" });
    expect(within(section).getByRole("button", { name: "Show 1 later message" })).toBeTruthy();

    fireEvent.click(earlier);
    expect(entries()).toHaveLength(5);
    expect(entries()[0]).toContain("Message 1");
    expect(within(section).queryByRole("button", { name: /earlier/ })).toBeNull();
    // The toggle is gone; the focus moves to the first message it revealed.
    expect(document.activeElement?.textContent).toContain("Message 1");

    fireEvent.click(within(section).getByRole("button", { name: "Show 1 later message" }));
    expect(entries()).toHaveLength(6);
    expect(within(section).queryByRole("button", { name: /^Show/ })).toBeNull();
  });

  it("shows a conversation of 3 in full, without toggles", async () => {
    conversation = [1, 2, 3].map((n) => entry(n, n === 3 ? "self" : "thread"));
    await show();
    const { section, entries } = await rows();
    expect(entries()).toHaveLength(3);
    expect(within(section).queryByRole("button", { name: /^Show/ })).toBeNull();
  });
});

describe("no frame gate (#203)", () => {
  it("has no hint and no pointer-events style: the frame is as tall as the mail", async () => {
    const article = await show();
    const frame = within(article).getByTitle(t.messageContent) as HTMLIFrameElement;
    expect(frame.getAttribute("style") ?? "").not.toContain("pointer-events");
    expect(frame.parentElement!.getAttribute("style") ?? "").not.toContain("pointer-events");
    expect(within(article).queryByRole("button", { name: /full message/i })).toBeNull();
    expect(within(article).queryByText("Show full message")).toBeNull();
  });

  it("does not take the focus back to a revealed conversation entry when the message re-renders (regression)", async () => {
    const n = (i: number) => String(i).repeat(64).slice(0, 64);
    conversation = [1, 2, 3, 4, 5, 6].map((i) => ({
      id: i === 4 ? id : n(i), subject: `Message ${i}`, from: "Sam Taylor <sam@example.org>",
      sentAt: `2026-10-0${i}T09:00:00Z`, sortAt: `2026-10-0${i}T09:00:00Z`, relation: i === 4 ? "self" : "thread",
    }));
    const article = await show();
    const section = await screen.findByRole("region", { name: t.conversation });
    fireEvent.click(within(section).getByRole("button", { name: "Show 2 earlier messages" }));
    const entry = document.activeElement;
    expect(entry?.textContent).toContain("Message 1");

    // The user moves on: the message re-renders with a new frame, twice.
    const images = within(article).getByRole("button", { name: t.loadRemoteImages });
    images.focus();
    fireEvent.click(images);
    expect(within(article).getByTitle(t.messageContent).getAttribute("src")).toContain("images=1");
    expect(document.activeElement).not.toBe(entry);
    const text = within(article).getByRole("button", { name: t.showText });
    text.focus();
    fireEvent.click(text);
    expect(within(article).queryByTitle(t.messageContent)).toBeNull();
    expect(document.activeElement).not.toBe(entry);
  });
});
