import { useEffect, useId, useLayoutEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type RefObject } from "react";
import { createPortal } from "react-dom";
import { Accordion, AccordionItem, Button, IconButton, SkeletonText, Tag, Toggletip, ToggletipButton, ToggletipContent, Tooltip } from "@carbon/react";
import { ChevronDown, ChevronUp, Document, Download, Forum, Html, Image, TextAlignLeft } from "@carbon/icons-react";
import { getConversation, getMessage, messageURL, type Conversation, type ConversationEntry, type Location, type MessageDetail, type Part } from "./api";
import { fileSize, locationLabel, longDate, originText, retentionDay, senderName, shortDate } from "./format";
import { attachmentKind, parseMailText, splitLinks, type TextBlock } from "./mailText";
import { useAsync } from "./useAsync";
import { twoPane, useMediaQuery } from "./breakpoints";
import { attachFrameFit } from "./frameFit";
import { t } from "./i18n";

interface Props {
  id: string;
  /** Opens another message, from the conversation. */
  open: (id: string) => void;
  /**
   * A focus request: while it is true, the next message to show (or its
   * load error) takes the focus. The app sets it when the reader replaces
   * the list on phones (#311).
   */
  focusRequest?: RefObject<boolean>;
}

/**
 * Whether the focus is lost: on body, or nowhere. A message that shows
 * then takes it; focus the user put somewhere is never taken away (#311).
 */
export function focusLost() {
  return !document.activeElement || document.activeElement === document.body;
}

/** Focuses el once it shows if a request is pending or the focus is lost, and clears the request. */
function useTakeFocus(el: RefObject<HTMLElement | null>, request: RefObject<boolean>) {
  useLayoutEffect(() => {
    if (!request.current && !focusLost()) return;
    request.current = false;
    el.current?.focus();
  }, [el, request]);
}

export function MessageView({ id, open, focusRequest }: Props) {
  const res = useAsync((signal) => getMessage(id, signal), [id]);
  // Also set when a message is picked in the conversation: its subject gets
  // the focus once it shows, so the new message is announced.
  const ownRequest = useRef(false);
  const focusSubject = focusRequest ?? ownRequest;
  if (res.status === "loading") return <MessageSkeleton />;
  if (res.status === "error") return <LoadError key={id} message={res.error.message} focusRequest={focusSubject} />;
  // key: reset view options (HTML/text, images) for each message. The
  // loaded message's id: right after id changes, res still holds the last one.
  return (
    <Message
      key={res.data.id}
      msg={res.data}
      focusSubject={focusSubject}
      open={(m) => {
        focusSubject.current = true;
        open(m);
      }}
    />
  );
}

/** The message could not load: the note takes the focus as the subject would. */
function LoadError({ message, focusRequest }: { message: string; focusRequest: RefObject<boolean> }) {
  const note = useRef<HTMLParagraphElement>(null);
  useTakeFocus(note, focusRequest);
  return (
    <p ref={note} tabIndex={-1} className="message__note app-text-error">
      {t.loadMessageFailed(message)}
    </p>
  );
}

/** The header and first lines of a message while it loads. Screen readers hear "Loading" instead. */
function MessageSkeleton() {
  return (
    <div role="status" className="message__skeleton">
      <span className="cds--visually-hidden">{t.loading}</span>
      <SkeletonText heading width="66%" />
      <SkeletonText paragraph lineCount={3} width="50%" />
      <SkeletonText paragraph lineCount={3} />
    </div>
  );
}

function Message({ msg, open, focusSubject }: { msg: MessageDetail; open: (id: string) => void; focusSubject: RefObject<boolean> }) {
  const [showHTML, setShowHTML] = useState(msg.hasHtml);
  const [images, setImages] = useState(false);
  // A compact header (#203): the subject, From and Date (on phones a byline),
  // one action row; the rest behind "Details". Follows rotation; each field
  // is rendered once, for either layout.
  const wide = useMediaQuery(twoPane);
  const [details, setDetails] = useState(false);
  const detailsId = useId();
  // The conversation loads on its own, so the message shows first and an
  // error there does not hide it; its toggle shows once it has loaded.
  const conversation = useAsync((signal) => getConversation(msg.id, signal), [msg.id]);
  const [conversationOpen, setConversationOpen] = useState(false);
  const conversationToggle = useRef<HTMLButtonElement>(null);
  // Back to the toggle once the panel is gone and the pane no longer inert.
  const refocusToggle = useRef(false);
  useEffect(() => {
    if (conversationOpen || !refocusToggle.current) return;
    refocusToggle.current = false;
    conversationToggle.current?.focus();
  }, [conversationOpen]);
  const subject = useRef<HTMLHeadingElement>(null);
  useTakeFocus(subject, focusSubject);
  // "Load remote images" removes itself: the focus goes to Download, the
  // action before it in the same row.
  const download = useRef<HTMLAnchorElement>(null);
  const attachments = msg.parts.filter((p) => p.attachment);
  // Journal mail has its envelope, filled and imported mail its origin.
  const delivery = msg.locations.filter((l) => l.envelope || l.filled);
  const html = msg.hasHtml && showHTML;
  const body = useRef<HTMLDivElement>(null);
  // The frame grows with the mail (frameFit.ts). A replaced frame (images
  // toggle, text and back) starts at the last fitted height. A layout
  // effect: attachFrameFit makes the frame inert before its first paint. It
  // is inert whenever the frame is not on a guarded mail document: until the
  // link blocker is on the mail's document (#209), and again from the moment
  // the frame leaves it until the blocker is on the next one (#265).
  const fitted = useRef("");
  useLayoutEffect(() => {
    const box = body.current;
    const frame = box?.querySelector("iframe");
    if (!box || !frame) return;
    const fit = attachFrameFit(frame, box, fitted.current);
    return () => {
      fitted.current = fit.height();
      fit.detach();
    };
  }, [html, images]);

  const recipients = (
    <>
      {msg.to && (
        <>
          <dt>{t.to}</dt>
          <dd>{msg.to}</dd>
        </>
      )}
      {msg.cc && (
        <>
          <dt>{t.cc}</dt>
          <dd>{msg.cc}</dd>
        </>
      )}
    </>
  );
  const date = (
    <>
      <dt>{t.date}</dt>
      <dd>{longDate(msg.sentAt) || msg.dateHeader}</dd>
    </>
  );
  const place = (
    <>
      <dt>{t.foundIn}</dt>
      <dd className="message__tags">
        {msg.locations.map((l) => (
          <LocationTag key={`${l.owner ?? ""}/${l.account}/${l.folder}/${l.uid}/${l.superseded}`} l={l} />
        ))}
      </dd>
      {msg.locations.length > 0 && (
        <>
          <dt>{t.retentionEnds}</dt>
          <dd className="message__tags">
            <span>{msg.retainUntil ? t.retentionEndsOn(retentionDay(msg.retainUntil)) : t.retentionKeptForever}</span>
            {msg.onHold && (
              <Tag size="sm" type="purple">
                {t.onLegalHold}
              </Tag>
            )}
          </dd>
        </>
      )}
    </>
  );
  const deliveryInfo = delivery.length > 0 && (
    <Accordion size="sm" className="message__delivery">
      {delivery.map((l) =>
        l.envelope ? (
          <AccordionItem key={`envelope/${l.account}/${l.folder}/${l.uid}`} title={t.envelope(l.account)}>
            <p className="app-break-words">
              {t.envelopeSender(l.envelope.mailFrom)} · {t.envelopeRecipients(l.envelope.rcptTo.join(", "))}
            </p>
            <p className="app-text-secondary app-text-small">{t.envelopeReceived(longDate(l.envelope.receivedAt), l.envelope.clientIp, l.envelope.helo)}</p>
            <p className="app-text-secondary app-text-small">{t.envelopeHint}</p>
          </AccordionItem>
        ) : (
          <AccordionItem key={`origin/${l.account}/${l.folder}/${l.uid}`} title={t.origin(l.account)}>
            <p>{originText(l)}</p>
          </AccordionItem>
        ),
      )}
    </Accordion>
  );
  // A text button on phones too: its privacy hint is its tooltip, which a tap may not show.
  const remoteImages = html && !images && (
    <Tooltip description={t.remoteImagesHint} align="bottom">
      <Button
        kind="tertiary"
        size="sm"
        onClick={() => {
          download.current?.focus();
          setImages(true);
        }}
      >
        {t.loadRemoteImages}
      </Button>
    </Tooltip>
  );

  const conv = conversation.status === "ok" && conversation.data.messages.length >= 2 ? conversation.data : null;
  const from = (
    <>
      <dt>{t.from}</dt>
      <dd>{msg.from}</dd>
    </>
  );

  return (
    <article className="message">
      <header className="message__header">
        <h1 ref={subject} tabIndex={-1} className={`message__subject${!wide && !details ? " message__subject--clamped" : ""}`}>
          {msg.subject || t.noSubject}
        </h1>
        {wide ? (
          <dl className="message__fields">
            {from}
            {date}
          </dl>
        ) : (
          <p className="message__byline">
            <span className="message__sender">{senderName(msg.from)}</span>
            {(msg.sentAt || msg.dateHeader) && (
              <>
                {" · "}
                {msg.sentAt ? (
                  <time dateTime={msg.sentAt} title={longDate(msg.sentAt)}>
                    {shortDate(msg.sentAt)}
                  </time>
                ) : (
                  <span>{msg.dateHeader}</span>
                )}
              </>
            )}
          </p>
        )}
        {/* One row, the same order everywhere; Conversation and Details at
            the end, so they can appear late without moving the others.
            Details comes last, so its content follows it. */}
        <div className="message__actions">
          {msg.hasHtml && (
            <IconButton kind="ghost" size={wide ? "sm" : "md"} align="bottom-start" label={showHTML ? t.showText : t.showHTML} onClick={() => setShowHTML(!showHTML)}>
              {showHTML ? <TextAlignLeft /> : <Html />}
            </IconButton>
          )}
          {/* Button, not IconButton: its types have no download. */}
          <Button
            ref={download}
            as="a"
            kind="ghost"
            size={wide ? "sm" : "md"}
            hasIconOnly
            iconDescription={t.downloadEml}
            tooltipPosition="bottom"
            // Opens to the right like the button before it; centred, it
            // starts left of the pane and is cut off (#379).
            tooltipAlignment="start"
            renderIcon={Download}
            href={messageURL.raw(msg.id)}
            download
          />
          {remoteImages}
          <span className="message__actions-end">
            {conv &&
              (wide ? (
                <Button
                  ref={conversationToggle}
                  kind="ghost"
                  size="sm"
                  aria-haspopup="dialog"
                  aria-expanded={conversationOpen}
                  onClick={() => setConversationOpen(true)}
                >
                  {t.conversationCount(conv.total)}
                </Button>
              ) : (
                <IconButton
                  ref={conversationToggle}
                  kind="ghost"
                  size="md"
                  align="bottom-end"
                  label={t.conversationCount(conv.total)}
                  className="message__count-toggle"
                  aria-haspopup="dialog"
                  aria-expanded={conversationOpen}
                  onClick={() => setConversationOpen(true)}
                >
                  <Forum />
                  <span aria-hidden>{conv.total}</span>
                </IconButton>
              ))}
            {wide ? (
              <Button
                kind="ghost"
                size="sm"
                renderIcon={details ? ChevronUp : ChevronDown}
                aria-expanded={details}
                aria-controls={detailsId}
                onClick={() => setDetails(!details)}
              >
                {t.details}
              </Button>
            ) : (
              <IconButton
                kind="ghost"
                size="md"
                align="bottom-end"
                label={t.details}
                aria-expanded={details}
                aria-controls={detailsId}
                onClick={() => setDetails(!details)}
              >
                {details ? <ChevronUp /> : <ChevronDown />}
              </IconButton>
            )}
          </span>
        </div>
        <div id={detailsId} className="message__details" hidden={!details}>
          <dl className="message__fields">
            {!wide && from}
            {recipients}
            {!wide && date}
            {place}
          </dl>
          {deliveryInfo}
        </div>
        {conversation.status === "error" && <p className="conversation__note app-text-error">{t.loadConversationFailed(conversation.error.message)}</p>}
        {attachments.length > 0 && (
          <ul aria-label={t.attachments} className="message__attachments">
            {attachments.map((p) => (
              <li key={p.index}>
                <AttachmentTile id={msg.id} part={p} />
              </li>
            ))}
          </ul>
        )}
        {msg.truncated && <p className="app-text-warning app-text-small">{t.truncated}</p>}
      </header>
      {conv && conversationOpen && (
        <ConversationPanel
          conversation={conv}
          wide={wide}
          toggle={conversationToggle}
          open={open}
          close={(refocus) => {
            refocusToggle.current = refocus;
            setConversationOpen(false);
          }}
        />
      )}
      {html ? (
        // In dark mode the mail's own (mostly white) page sits as a sheet on
        // the dark background instead of filling the pane edge to edge.
        <div ref={body} className="message__html">
          {/* The same origin, so the app can measure the document and size the
              frame to it (#203); never allow-scripts, which together with the
              same origin would undo the sandbox. No script runs in the mail:
              the sandbox, and the server's CSP (script-src 'none', no forms,
              no remote content) as a second layer. Its requests to the API
              are refused by the server (Fetch Metadata) and its links into
              the app by frameFit.ts. The same sandbox as the server's CSP. */}
          <iframe
            // A new element per mode: changing src would add a browser history
            // entry, so Back would toggle images instead of leaving the message.
            key={images ? "with-images" : "no-images"}
            title={t.messageContent}
            className="message__frame"
            sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox"
            referrerPolicy="no-referrer"
            src={messageURL.html(msg.id, images)}
          />
        </div>
      ) : (
        <div className="message__text">
          <MailText blocks={parseMailText(msg.text)} />
        </div>
      )}
    </article>
  );
}

/**
 * Where the message was found. A renumbered or gone location explains
 * itself in a toggletip: a tap, click, Enter or Space opens it, Escape or a
 * click elsewhere closes it (#310). The button is named by the location (Carbon's
 * default label is an English "Show information"), and holds a plain tag
 * as a span: no button inside the button, no div inside it.
 */
function LocationTag({ l }: { l: Location }) {
  const hint = l.superseded ? t.renumberedHint : l.goneAt ? t.goneFromServerHint : "";
  if (!hint) {
    return (
      <Tag size="sm" type="gray">
        {locationLabel(l)}
      </Tag>
    );
  }
  return (
    // autoAlign: no fixed side fits every place the tag wraps to; it flips
    // inside the reading pane instead of widening it (#379).
    <Toggletip align="bottom" autoAlign>
      <ToggletipButton label={locationLabel(l)}>
        <Tag as="span" size="sm" type={l.superseded ? "cool-gray" : "warm-gray"}>
          {locationLabel(l)}
        </Tag>
      </ToggletipButton>
      <ToggletipContent>{hint}</ToggletipContent>
    </Toggletip>
  );
}

/** A downloadable attachment: type, name and size, large enough to tap. */
function AttachmentTile({ id, part: p }: { id: string; part: Part }) {
  const kind = attachmentKind(p.filename, p.contentType);
  const name = p.filename || t.part(p.index);
  return (
    <a href={messageURL.part(id, p.index)} download={p.filename || undefined} title={name} className="attachment">
      <span aria-hidden className={`attachment__icon${kind.image ? " attachment__icon--image" : ""}`}>
        {kind.image ? <Image size={20} /> : kind.label || <Document size={20} />}
      </span>
      <span className="attachment__name">{name}</span>
      <span className="attachment__meta">{[kind.label, fileSize(p.size)].filter(Boolean).join(" · ")}</span>
    </a>
  );
}

/**
 * A plain-text body set for reading: the line breaks of the mail are kept,
 * quotes are indented and muted (older ones folded), the signature is quiet.
 */
function MailText({ blocks, depth = 0 }: { blocks: TextBlock[]; depth?: number }) {
  return (
    <div className={`mail-text${depth ? "" : " mail-text--top"}`}>
      {blocks.map((b, i) =>
        b.kind === "text" ? (
          <p key={i} className="mail-text__para">
            <Linked text={b.text} />
          </p>
        ) : b.kind === "signature" ? (
          <p key={i} className="mail-text__para mail-text__signature">
            <Linked text={b.text} />
          </p>
        ) : depth === 0 ? (
          <blockquote key={i} className="mail-text__quote">
            <MailText blocks={b.blocks} depth={depth + 1} />
          </blockquote>
        ) : (
          <details key={i} className="mail-text__fold">
            <summary>{t.showQuote}</summary>
            <div className="mail-text__folded">
              <MailText blocks={b.blocks} depth={depth + 1} />
            </div>
          </details>
        ),
      )}
    </div>
  );
}

function Linked({ text }: { text: string }) {
  return splitLinks(text).map((s, i) =>
    s.href ? (
      <a key={i} href={s.href} target="_blank" rel="noopener noreferrer" className="cds--link">
        {s.text}
      </a>
    ) : (
      s.text
    ),
  );
}

function relationLabel(r: ConversationEntry["relation"]): string | undefined {
  return ({ self: t.thisMessage, parent: t.inReplyTo, reply: t.replyLabel } as Partial<Record<ConversationEntry["relation"], string>>)[r];
}

/**
 * The messages of the conversation, oldest first, in a modal panel over the
 * message (#203): under the toggle on desktop, a sheet below the reader bar
 * on phones. It pushes nothing down, so it shows all the server returned.
 * The rest of the reading pane is inert and a scrim over it closes the panel
 * (a click into the mail frame never reaches the app's window otherwise).
 * Tab and the arrow keys stay in it; Escape, the scrim and focus or a click
 * elsewhere close it. The shortcuts are off meanwhile (dialogs.ts).
 */
function ConversationPanel({
  conversation: c,
  wide,
  toggle,
  open,
  close,
}: {
  conversation: Conversation;
  wide: boolean;
  toggle: RefObject<HTMLButtonElement | null>;
  open: (id: string) => void;
  close: (refocus: boolean) => void;
}) {
  const overlay = useRef<HTMLDivElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  // In the reading pane, next to its bar and scroller, which go inert.
  const [host] = useState(() => toggle.current?.closest<HTMLElement>(".mail-pane--reader") ?? document.body);
  // Desktop: under the toggle, end-aligned to it, in the pane's box.
  const [at, setAt] = useState<{ top: number; end: number } | null>(null);
  const closeRef = useRef(close);
  closeRef.current = close;

  useLayoutEffect(() => {
    const place = () => {
      const b = toggle.current?.getBoundingClientRect();
      const h = host.getBoundingClientRect();
      // The panel's width is min(32rem, pane − 2rem) (layout.css): it stays in the pane.
      const width = Math.min(32 * parseFloat(getComputedStyle(document.documentElement).fontSize || "16"), h.width - 32);
      if (b) setAt({ top: b.bottom - h.top + 4, end: Math.max(16, Math.min(h.right - b.right, h.width - width - 16)) });
    };
    place();
    window.addEventListener("resize", place);
    return () => window.removeEventListener("resize", place);
  }, [host, toggle]);

  useEffect(() => {
    // Everything else in the pane is inert while the panel is open.
    const others = [...host.children].filter((el) => el !== overlay.current && !el.hasAttribute("inert"));
    for (const el of others) el.setAttribute("inert", "");
    const self = panel.current?.querySelector<HTMLElement>('[aria-current="true"]') ?? entries(panel.current)[0];
    self?.focus();
    self?.scrollIntoView?.({ block: "nearest" });
    // A click or the focus outside the panel and its scrim closes it.
    const outside = (e: Event) => {
      if (e.target instanceof Node && !overlay.current?.contains(e.target)) closeRef.current(false);
    };
    document.addEventListener("pointerdown", outside, true);
    document.addEventListener("focusin", outside);
    return () => {
      document.removeEventListener("pointerdown", outside, true);
      document.removeEventListener("focusin", outside);
      for (const el of others) el.removeAttribute("inert");
    };
  }, [host]);

  const onKey = (e: ReactKeyboardEvent) => {
    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      close(true);
      return;
    }
    const list = entries(panel.current);
    // -1 with the focus on the dialog itself: forward goes to the first entry, back to the last.
    const i = list.indexOf(document.activeElement as HTMLElement);
    const to =
      e.key === "ArrowDown" || (e.key === "Tab" && !e.shiftKey)
        ? (i + 1) % list.length
        : e.key === "ArrowUp" || (e.key === "Tab" && e.shiftKey)
          ? (i < 0 ? list.length - 1 : i - 1 + list.length) % list.length
          : e.key === "Home"
            ? 0
            : e.key === "End"
              ? list.length - 1
              : -1;
    if (to < 0) return;
    e.preventDefault();
    list[to]?.focus();
  };

  const hidden = c.total - c.messages.length;
  return createPortal(
    <div ref={overlay} className={`conversation-overlay${wide ? "" : " conversation-overlay--sheet"}`}>
      <div aria-hidden className="conversation-overlay__scrim" onClick={() => close(true)} />
      <div
        ref={panel}
        role="dialog"
        aria-modal="true"
        aria-label={t.conversation}
        // As Carbon's Modal: a click on a part that cannot take focus focuses the dialog, not body, so its keys still work.
        tabIndex={-1}
        className="conversation"
        style={wide && at ? { insetBlockStart: at.top, insetInlineEnd: at.end, maxBlockSize: `min(28rem, ${Math.max(0, host.clientHeight - at.top - 16)}px)` } : undefined}
        onKeyDown={onKey}
      >
        <h2 className="conversation__title">{t.conversation}</h2>
        {c.truncated && <p className="app-text-secondary app-text-small">{t.earlierNotShown(hidden)}</p>}
        <ol className="conversation__list">
          {c.messages.map((m) => {
            const self = m.relation === "self";
            const label = relationLabel(m.relation);
            const line = (
              <>
                <time className="conversation__date" dateTime={m.sortAt}>
                  {shortDate(m.sentAt ?? m.sortAt)}
                </time>
                <span className="conversation__from">{senderName(m.from)}</span>
                <span className="conversation__subject">{m.subject || t.noSubject}</span>
                {label && (
                  <Tag as="span" size="sm" type="gray" className="conversation__tag">
                    {label}
                  </Tag>
                )}
              </>
            );
            return (
              <li key={m.id}>
                {self ? (
                  <div aria-current="true" tabIndex={-1} className="conversation__entry conversation__entry--self">
                    {line}
                  </div>
                ) : (
                  <button type="button" className="conversation__entry" data-id={m.id} onClick={() => open(m.id)}>
                    {line}
                  </button>
                )}
              </li>
            );
          })}
        </ol>
      </div>
    </div>,
    host,
  );
}

/** The entries of the panel, in order: the open message's too, so the keys reach it. */
function entries(panel: HTMLElement | null): HTMLElement[] {
  return panel ? [...panel.querySelectorAll<HTMLElement>(".conversation__entry")] : [];
}
