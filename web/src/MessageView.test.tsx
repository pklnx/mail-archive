// The message view on Carbon. The HTML of a mail stays in the sandboxed
// frame exactly as before #168 (the regression test below), remote images
// stay blocked until asked for, and journal mail shows its envelope in an
// accordion. The API is a stub; no network.
// #200, #203: the header is compact (the rest behind Details) and the
// conversation opens as an overlay over the message.
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
    fireEvent.click(within(article).getByRole("button", { name: t.details }));
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
    fireEvent.click(within(article).getByRole("button", { name: de.details }));
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

const other = (n: number) => String(n).repeat(64).slice(0, 64);
/** A conversation of six with the open message 4th. */
const six = (): ConversationEntry[] =>
  [1, 2, 3, 4, 5, 6].map((n) => ({
    id: n === 4 ? id : other(n),
    subject: `Message ${n}`,
    from: "Sam Taylor <sam@example.org>",
    sentAt: `2026-10-0${n}T09:00:00Z`,
    sortAt: `2026-10-0${n}T09:00:00Z`,
    relation: n === 4 ? "self" : "thread",
  }));

const journal = () =>
  message({
    cc: "robin@example.org",
    locations: [location({ account: "journal", envelope: { mailFrom: "sam@example.org", rcptTo: ["alex@example.org"], receivedAt: "2026-10-01T09:00:01Z", clientIp: "", helo: "" } })],
  });

/** Whether the text shows: in the article, and not behind a closed Details. */
const shown = (article: HTMLElement, text: string) => {
  const el = within(article).queryByText(text);
  return el !== null && el.closest("[hidden]") === null;
};

describe("the compact header on phones (#200, #203)", () => {
  beforeEach(() => twoPane(false));

  it("shows the clamped subject and a byline, and puts the rest behind a closed Details toggle", async () => {
    detail = journal();
    const article = await show();
    const h1 = within(article).getByRole("heading", { level: 1 });
    expect(h1.textContent).toBe(detail.subject);
    expect(h1.className).toContain("message__subject--clamped");
    const byline = article.querySelector(".message__byline")!;
    const time = byline.querySelector("time")!;
    expect(byline.textContent).toBe(`Sam Taylor · ${time.textContent}`);
    expect(time.getAttribute("dateTime")).toBe(detail.sentAt);
    expect(time.textContent).not.toBe("");

    const terms = () => within(article).queryAllByRole("term").map((el) => el.textContent);
    expect(terms()).toEqual([]);
    const toggle = within(article).getByRole("button", { name: t.details });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    const panel = document.getElementById(toggle.getAttribute("aria-controls")!)!;
    expect(panel.hidden).toBe(true);
    expect(within(article).queryByRole("button", { name: t.envelope("journal") })).toBeNull();
    expect(shown(article, "journal / INBOX")).toBe(false);
    expect(shown(article, "Sam Taylor <sam@example.org>")).toBe(false);

    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(panel.hidden).toBe(false);
    expect(h1.className).not.toContain("message__subject--clamped");
    expect(terms()).toEqual([t.from, t.to, t.cc, t.date, t.foundIn, t.retentionEnds]);
    expect(shown(article, "Sam Taylor <sam@example.org>")).toBe(true);
    expect(shown(article, "alex@example.org")).toBe(true);
    expect(shown(article, "journal / INBOX")).toBe(true);
    expect(within(article).getByRole("button", { name: t.envelope("journal") })).toBeTruthy();
    // Each field once: no hidden copy for the other layout.
    expect(within(article).getAllByText("robin@example.org")).toHaveLength(1);
  });

  it("has the actions in one row: icon buttons named as before, remote images as text, Details and Conversation as icons", async () => {
    conversation = six();
    const article = await show();
    const toggle = within(article).getByRole("button", { name: t.showText });
    expect(within(article).getByRole("button", { name: t.loadRemoteImages }).textContent).toBe(t.loadRemoteImages);
    const download = within(article).getByRole("link", { name: t.downloadEml });
    expect(download.getAttribute("href")).toBe(`/api/messages/${id}/raw`);
    expect(download.hasAttribute("download")).toBe(true);
    expect(within(article).getByRole("button", { name: t.details }).textContent).toBe("");
    const conv = await within(article).findByRole("button", { name: "Conversation (6)" });
    // The icon and the count; the full name is the tooltip.
    expect(conv.textContent).toBe("6");
    expect(conv.getAttribute("aria-haspopup")).toBe("dialog");
    fireEvent.click(toggle);
    expect(within(article).getByRole("button", { name: t.showHTML })).toBeTruthy();
    expect(within(article).queryByTitle(t.messageContent)).toBeNull();
  });
});

describe("the compact header on desktop (#203)", () => {
  it("shows From and Date, the rest behind Details; the icon buttons are named as before", async () => {
    detail = journal();
    conversation = six();
    const article = await show();
    const terms = () => within(article).queryAllByRole("term").map((el) => el.textContent);
    expect(terms()).toEqual([t.from, t.date]);
    expect(shown(article, "Sam Taylor <sam@example.org>")).toBe(true);
    expect(within(article).getByRole("heading", { level: 1 }).className).not.toContain("clamped");
    expect(shown(article, "alex@example.org")).toBe(false);
    expect(shown(article, "journal / INBOX")).toBe(false);
    expect(within(article).queryByRole("button", { name: t.envelope("journal") })).toBeNull();
    expect(within(article).getByRole("button", { name: t.showText }).textContent).toBe("");
    expect(within(article).getByRole("link", { name: t.downloadEml })).toBeTruthy();
    expect((await within(article).findByRole("button", { name: "Conversation (6)" })).textContent).toBe("Conversation (6)");

    const toggle = within(article).getByRole("button", { name: t.details });
    expect(toggle.textContent).toBe(t.details);
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(terms()).toEqual([t.from, t.date, t.to, t.cc, t.foundIn, t.retentionEnds]);
    expect(shown(article, "journal / INBOX")).toBe(true);
    expect(within(article).getByRole("button", { name: t.envelope("journal") })).toBeTruthy();
  });

  it("names the toggle in German", async () => {
    applySettings({ language: "de", locale: "de" });
    conversation = six();
    const article = await show();
    expect(await within(article).findByRole("button", { name: "Konversation (6)" })).toBeTruthy();
  });
});

describe("the conversation overlay (#203)", () => {
  async function toggle() {
    const article = await show();
    return { article, button: await within(article).findByRole("button", { name: "Conversation (6)" }) };
  }

  it("is closed by default", async () => {
    conversation = six();
    const { button } = await toggle();
    expect(button.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByText("Message 1")).toBeNull();
  });

  it("opens as a modal dialog with all messages, the focus on the open one; Escape closes it and focuses the toggle", async () => {
    conversation = six();
    const { article, button } = await toggle();
    fireEvent.click(button);
    const dialog = screen.getByRole("dialog", { name: t.conversation });
    expect(dialog.getAttribute("aria-modal")).toBe("true");
    expect(button.getAttribute("aria-expanded")).toBe("true");
    // No fold: all six.
    expect(within(dialog).getAllByRole("listitem")).toHaveLength(6);
    expect(within(dialog).queryByRole("button", { name: /^Show/ })).toBeNull();
    expect(document.activeElement?.getAttribute("aria-current")).toBe("true");
    expect(document.activeElement?.textContent).toContain("Message 4");
    // Everything outside the panel is inert.
    expect(article.closest("[inert]")).not.toBeNull();
    expect(dialog.closest("[inert]")).toBeNull();

    fireEvent.keyDown(document.activeElement!, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(article.closest("[inert]")).toBeNull();
    expect(document.activeElement).toBe(button);
    expect(button.getAttribute("aria-expanded")).toBe("false");
  });

  it("keeps Tab and the arrow keys inside", async () => {
    conversation = six();
    const { button } = await toggle();
    fireEvent.click(button);
    const dialog = screen.getByRole("dialog");
    const focused = () => document.activeElement?.textContent ?? "";
    fireEvent.keyDown(document.activeElement!, { key: "ArrowDown" });
    expect(focused()).toContain("Message 5");
    fireEvent.keyDown(document.activeElement!, { key: "End" });
    expect(focused()).toContain("Message 6");
    fireEvent.keyDown(document.activeElement!, { key: "Tab" });
    expect(focused()).toContain("Message 1");
    fireEvent.keyDown(document.activeElement!, { key: "Tab", shiftKey: true });
    expect(focused()).toContain("Message 6");
    fireEvent.keyDown(document.activeElement!, { key: "Home" });
    expect(focused()).toContain("Message 1");
    fireEvent.keyDown(document.activeElement!, { key: "ArrowUp" });
    expect(focused()).toContain("Message 6");
    expect(dialog.contains(document.activeElement)).toBe(true);
  });

  // A click on the title, the padding or a date focuses the dialog itself, not body (#311): its keys still work.
  it("takes the focus itself; Escape from there closes it and focuses the toggle", async () => {
    conversation = six();
    const { button } = await toggle();
    fireEvent.click(button);
    const dialog = screen.getByRole("dialog", { name: t.conversation });
    expect(dialog.getAttribute("tabindex")).toBe("-1");
    dialog.focus();
    expect(document.activeElement).toBe(dialog);
    // The focus inside the panel does not close it.
    expect(screen.getByRole("dialog")).toBe(dialog);
    fireEvent.keyDown(dialog, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(document.activeElement).toBe(button);
  });

  it("goes from the dialog itself to the first entry with Tab and ArrowDown, to the last with Shift+Tab and ArrowUp", async () => {
    conversation = six();
    const { button } = await toggle();
    fireEvent.click(button);
    const dialog = screen.getByRole("dialog");
    const focused = () => document.activeElement?.textContent ?? "";
    for (const [k, shiftKey, to] of [
      ["Tab", false, "Message 1"],
      ["ArrowDown", false, "Message 1"],
      ["Home", false, "Message 1"],
      ["Tab", true, "Message 6"],
      ["ArrowUp", false, "Message 6"],
      ["End", false, "Message 6"],
    ] as const) {
      dialog.focus();
      fireEvent.keyDown(dialog, { key: k, shiftKey });
      expect(focused(), `${shiftKey ? "Shift+" : ""}${k}`).toContain(to);
    }
  });

  it("closes on a scrim click (focus back on the toggle) and on a click elsewhere", async () => {
    conversation = six();
    const { button } = await toggle();
    fireEvent.click(button);
    fireEvent.click(document.querySelector(".conversation-overlay__scrim")!);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(document.activeElement).toBe(button);

    fireEvent.click(button);
    expect(screen.getByRole("dialog")).toBeTruthy();
    const elsewhere = document.createElement("button");
    document.body.append(elsewhere);
    fireEvent.pointerDown(elsewhere);
    expect(screen.queryByRole("dialog")).toBeNull();
    elsewhere.remove();
  });

  it("opens a picked message and focuses its subject", async () => {
    conversation = six();
    const open = vi.fn();
    const { rerender } = render(<MessageView id={id} open={open} />);
    fireEvent.click(await screen.findByRole("button", { name: "Conversation (6)" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: /Message 2/ }));
    expect(open).toHaveBeenCalledWith(other(2));
    detail = message({ id: other(2), subject: "Message 2" });
    rerender(<MessageView id={other(2)} open={open} />);
    const h1 = await screen.findByRole("heading", { level: 1, name: "Message 2" });
    expect(document.activeElement).toBe(h1);
  });

  it("shows the server's note on messages not returned", async () => {
    conversation = six();
    vi.stubGlobal(
      "fetch",
      vi.fn(async (path: string) => {
        const body = path.endsWith("/conversation") ? { messages: conversation, total: 9, truncated: true } : detail;
        return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
      }),
    );
    const article = await show();
    fireEvent.click(await within(article).findByRole("button", { name: "Conversation (9)" }));
    expect(within(screen.getByRole("dialog")).getByText(t.earlierNotShown(3))).toBeTruthy();
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
});
