import { useState } from "react";
import { Accordion, AccordionItem, Button, OperationalTag, SkeletonText, Tag, Tooltip } from "@carbon/react";
import { Document, Download, Image } from "@carbon/icons-react";
import { getConversation, getMessage, messageURL, type ConversationEntry, type Location, type MessageDetail, type Part } from "./api";
import { fileSize, locationLabel, longDate, originText, retentionDay, senderName, shortDate } from "./format";
import { attachmentKind, parseMailText, splitLinks, type TextBlock } from "./mailText";
import { useAsync } from "./useAsync";
import { t } from "./i18n";

interface Props {
  id: string;
  /** Opens another message, from the conversation. */
  open: (id: string) => void;
}

export function MessageView({ id, open }: Props) {
  const res = useAsync((signal) => getMessage(id, signal), [id]);
  if (res.status === "loading") return <MessageSkeleton />;
  if (res.status === "error") return <p className="message__note app-text-error">{t.loadMessageFailed(res.error.message)}</p>;
  // key: reset view options (HTML/text, images) for each message.
  return <Message key={id} msg={res.data} open={open} />;
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

function Message({ msg, open }: { msg: MessageDetail; open: (id: string) => void }) {
  const [showHTML, setShowHTML] = useState(msg.hasHtml);
  const [images, setImages] = useState(false);
  const attachments = msg.parts.filter((p) => p.attachment);
  // Journal mail has its envelope, filled and imported mail its origin.
  const delivery = msg.locations.filter((l) => l.envelope || l.filled);

  return (
    <article className="message">
      <header className="message__header">
        <h1 className="message__subject">{msg.subject || t.noSubject}</h1>
        <dl className="message__fields">
          <dt>{t.from}</dt>
          <dd>{msg.from}</dd>
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
          <dt>{t.date}</dt>
          <dd>{longDate(msg.sentAt) || msg.dateHeader}</dd>
          <dt>{t.foundIn}</dt>
          <dd className="message__tags">
            {msg.locations.map((l) => (
              <LocationTag key={`${l.account}/${l.folder}/${l.uid}/${l.superseded}`} l={l} />
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
        </dl>
        {delivery.length > 0 && (
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
        )}
        <div className="message__actions">
          {msg.hasHtml && (
            <Button kind="tertiary" size="sm" onClick={() => setShowHTML(!showHTML)}>
              {showHTML ? t.showText : t.showHTML}
            </Button>
          )}
          {msg.hasHtml && showHTML && !images && (
            <Tooltip description={t.remoteImagesHint} align="bottom">
              <Button kind="tertiary" size="sm" onClick={() => setImages(true)}>
                {t.loadRemoteImages}
              </Button>
            </Tooltip>
          )}
          <Button as="a" kind="ghost" size="sm" renderIcon={Download} href={messageURL.raw(msg.id)} download>
            {t.downloadEml}
          </Button>
        </div>
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
      <ConversationSection id={msg.id} open={open} />
      {msg.hasHtml && showHTML ? (
        // In dark mode the mail's own (mostly white) page sits as a sheet on
        // the dark background instead of filling the pane edge to edge.
        <div className="message__html">
          {/* No allow-scripts and no allow-same-origin: the mail's HTML runs with
              an opaque origin and cannot reach the API. The server's CSP adds
              another layer (no scripts, no forms, no remote content). */}
          <iframe
            // A new element per mode: changing src would add a browser history
            // entry, so Back would toggle images instead of leaving the message.
            key={images ? "with-images" : "no-images"}
            title={t.messageContent}
            className="message__frame"
            sandbox="allow-popups allow-popups-to-escape-sandbox"
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
 * itself in a tooltip; its tag is a button, so the keyboard reaches it.
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
    <Tooltip description={hint} align="bottom">
      <OperationalTag size="sm" type={l.superseded ? "cool-gray" : "warm-gray"} text={locationLabel(l)} />
    </Tooltip>
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
 * The other messages of the conversation, oldest first. It loads on its own,
 * so the message shows first and an error here does not hide it.
 */
function ConversationSection({ id, open }: { id: string; open: (id: string) => void }) {
  const res = useAsync((signal) => getConversation(id, signal), [id]);
  if (res.status === "loading") return null;
  if (res.status === "error") {
    return <p className="conversation__note app-text-error">{t.loadConversationFailed(res.error.message)}</p>;
  }
  const c = res.data;
  if (c.messages.length < 2) return null;
  const hidden = c.total - c.messages.length;
  return (
    <section aria-label={t.conversation} className="conversation">
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
                <div aria-current="true" className="conversation__entry conversation__entry--self">
                  {line}
                </div>
              ) : (
                <button type="button" className="conversation__entry" onClick={() => open(m.id)}>
                  {line}
                </button>
              )}
            </li>
          );
        })}
      </ol>
    </section>
  );
}
