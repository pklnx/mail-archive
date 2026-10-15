import { Fragment, useState } from "react";
import { getConversation, getMessage, messageURL, type ConversationEntry, type MessageDetail } from "./api";
import { fileSize, locationLabel, longDate, retentionDay, senderName, shortDate } from "./format";
import { useAsync } from "./useAsync";
import { t } from "./i18n";

interface Props {
  id: string;
  /** Opens another message, from the conversation. */
  open: (id: string) => void;
}

export function MessageView({ id, open }: Props) {
  const res = useAsync((signal) => getMessage(id, signal), [id]);
  if (res.status === "loading") return <p className="p-6 text-sm text-ink-muted">{t.loading}</p>;
  if (res.status === "error") return <p className="p-6 text-sm text-danger">{t.loadMessageFailed(res.error.message)}</p>;
  // key: reset view options (HTML/text, images) for each message.
  return <Message key={id} msg={res.data} open={open} />;
}

function Message({ msg, open }: { msg: MessageDetail; open: (id: string) => void }) {
  const [showHTML, setShowHTML] = useState(msg.hasHtml);
  const [images, setImages] = useState(false);
  const attachments = msg.parts.filter((p) => p.attachment);

  return (
    <article className="flex h-full flex-col">
      <header className="border-b border-line p-4">
        <h1 className="text-lg font-semibold break-words">{msg.subject || t.noSubject}</h1>
        <dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-sm">
          <dt className="text-ink-muted">{t.from}</dt>
          <dd className="break-words">{msg.from}</dd>
          {msg.to && (
            <>
              <dt className="text-ink-muted">{t.to}</dt>
              <dd className="break-words">{msg.to}</dd>
            </>
          )}
          {msg.cc && (
            <>
              <dt className="text-ink-muted">{t.cc}</dt>
              <dd className="break-words">{msg.cc}</dd>
            </>
          )}
          <dt className="text-ink-muted">{t.date}</dt>
          <dd>{longDate(msg.sentAt) || msg.dateHeader}</dd>
          <dt className="text-ink-muted">{t.foundIn}</dt>
          <dd className="flex flex-wrap gap-1">
            {msg.locations.map((l) => (
              <span
                key={`${l.account}/${l.folder}/${l.uid}/${l.superseded}`}
                className={`rounded px-1.5 py-0.5 text-xs ${
                  l.superseded
                    ? "text-ink-muted italic"
                    : l.goneAt
                      ? "bg-tag-warning text-tag-warning-ink"
                      : "bg-surface-hover"
                }`}
                title={l.superseded ? t.renumberedHint : l.goneAt ? t.goneFromServerHint : undefined}
              >
                {locationLabel(l)}
              </span>
            ))}
          </dd>
          {msg.locations.length > 0 && (
            <>
              <dt className="text-ink-muted">{t.retentionEnds}</dt>
              <dd>
                {msg.retainUntil ? t.retentionEndsOn(retentionDay(msg.retainUntil)) : t.retentionKeptForever}
                {msg.onHold && (
                  <span className="ml-2 rounded bg-tag-warning px-1.5 py-0.5 text-xs text-tag-warning-ink">
                    {t.onLegalHold}
                  </span>
                )}
              </dd>
            </>
          )}
          {msg.locations.map(
            (l) =>
              l.envelope && (
                <Fragment key={`envelope/${l.account}/${l.folder}/${l.uid}`}>
                  <dt className="text-ink-muted" title={t.envelopeHint}>
                    {t.envelope(l.account)}
                  </dt>
                  <dd className="break-words">
                    {t.envelopeSender(l.envelope.mailFrom)} · {t.envelopeRecipients(l.envelope.rcptTo.join(", "))}
                    <span className="block text-xs text-ink-muted">
                      {t.envelopeReceived(longDate(l.envelope.receivedAt), l.envelope.clientIp, l.envelope.helo)}
                    </span>
                  </dd>
                </Fragment>
              ),
          )}
          {msg.locations.map(
            (l) =>
              l.filled && (
                <Fragment key={`origin/${l.account}/${l.folder}/${l.uid}`}>
                  <dt className="text-ink-muted">{t.origin(l.account)}</dt>
                  <dd>{t.originFilled}</dd>
                </Fragment>
              ),
          )}
        </dl>
        <div className="mt-3 flex flex-wrap items-center gap-2 text-sm">
          {msg.hasHtml && (
            <button type="button" className="rounded border border-line-control px-2 py-0.5 pointer-coarse:min-h-11" onClick={() => setShowHTML(!showHTML)}>
              {showHTML ? t.showText : t.showHTML}
            </button>
          )}
          {msg.hasHtml && showHTML && !images && (
            <button
              type="button"
              className="rounded border border-line-control px-2 py-0.5 pointer-coarse:min-h-11"
              title={t.remoteImagesHint}
              onClick={() => setImages(true)}
            >
              {t.loadRemoteImages}
            </button>
          )}
          <a className="text-accent-ink hover:underline pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center" href={messageURL.raw(msg.id)} download>
            {t.downloadEml}
          </a>
        </div>
        {attachments.length > 0 && (
          <ul aria-label={t.attachments} className="mt-3 flex flex-wrap gap-2">
            {attachments.map((p) => (
              <li key={p.index}>
                <a
                  href={messageURL.part(msg.id, p.index)}
                  download={p.filename || undefined}
                  className="inline-flex items-center gap-1 rounded border border-line-control px-2 py-1 text-xs hover:bg-surface-sunken pointer-coarse:min-h-11"
                >
                  📎 <span className="max-w-60 truncate">{p.filename || t.part(p.index)}</span>
                  <span className="text-ink-muted">{fileSize(p.size)}</span>
                </a>
              </li>
            ))}
          </ul>
        )}
        {msg.truncated && <p className="mt-2 text-xs text-warning">{t.truncated}</p>}
      </header>
      <ConversationSection id={msg.id} open={open} />
      {msg.hasHtml && showHTML ? (
        // No allow-scripts and no allow-same-origin: the mail's HTML runs with
        // an opaque origin and cannot reach the API. The server's CSP adds
        // another layer (no scripts, no forms, no remote content).
        <iframe
          // A new element per mode: changing src would add a browser history
          // entry, so Back would toggle images instead of leaving the message.
          key={images ? "with-images" : "no-images"}
          title={t.messageContent}
          className="min-h-0 w-full flex-1 bg-white"
          sandbox="allow-popups allow-popups-to-escape-sandbox"
          referrerPolicy="no-referrer"
          src={messageURL.html(msg.id, images)}
        />
      ) : (
        <pre className="min-h-0 flex-1 overflow-auto p-4 font-sans text-sm whitespace-pre-wrap break-words">{msg.text}</pre>
      )}
    </article>
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
    return <p className="border-b border-line px-4 py-2 text-xs text-danger">{t.loadConversationFailed(res.error.message)}</p>;
  }
  const c = res.data;
  if (c.messages.length < 2) return null;
  const hidden = c.total - c.messages.length;
  return (
    <section aria-label={t.conversation} className="max-h-48 shrink-0 overflow-y-auto border-b border-line px-4 py-2">
      <h2 className="text-xs font-semibold tracking-wide text-ink-muted uppercase">{t.conversation}</h2>
      {c.truncated && <p className="mt-1 text-xs text-ink-muted">{t.earlierNotShown(hidden)}</p>}
      <ol className="mt-1 text-sm">
        {c.messages.map((m) => {
          const self = m.relation === "self";
          const label = relationLabel(m.relation);
          const line = (
            <>
              <time className="w-20 shrink-0 text-xs text-ink-muted" dateTime={m.sortAt}>
                {shortDate(m.sentAt ?? m.sortAt)}
              </time>
              <span className="max-w-40 shrink-0 truncate">{senderName(m.from)}</span>
              <span className="min-w-0 flex-1 truncate text-ink-muted">{m.subject || t.noSubject}</span>
              {label && <span className="shrink-0 rounded bg-surface-hover px-1.5 text-xs text-ink-muted">{label}</span>}
            </>
          );
          return (
            <li key={m.id}>
              {self ? (
                <div aria-current="true" className="flex items-baseline gap-2 rounded px-1 py-0.5 font-semibold pointer-coarse:min-h-11 pointer-coarse:items-center">
                  {line}
                </div>
              ) : (
                <button type="button" className="flex w-full items-baseline gap-2 rounded px-1 py-0.5 text-left hover:bg-surface-hover pointer-coarse:min-h-11 pointer-coarse:items-center" onClick={() => open(m.id)}>
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
