import { useState } from "react";
import { getMessage, messageURL, type MessageDetail } from "./api";
import { fileSize, longDate } from "./format";
import { useAsync } from "./useAsync";

export function MessageView({ id }: { id: string }) {
  const res = useAsync((signal) => getMessage(id, signal), [id]);
  if (res.status === "loading") return <p className="p-6 text-sm text-zinc-500">Loading…</p>;
  if (res.status === "error") return <p className="p-6 text-sm text-red-600">Could not load message: {res.error.message}</p>;
  // key: reset view options (HTML/text, images) for each message.
  return <Message key={id} msg={res.data} />;
}

function Message({ msg }: { msg: MessageDetail }) {
  const [showHTML, setShowHTML] = useState(msg.hasHtml);
  const [images, setImages] = useState(false);
  const attachments = msg.parts.filter((p) => p.attachment);

  return (
    <article className="flex h-full flex-col">
      <header className="border-b border-zinc-200 p-4 dark:border-zinc-800">
        <h1 className="text-lg font-semibold break-words">{msg.subject || "(no subject)"}</h1>
        <dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-sm">
          <dt className="text-zinc-500">From</dt>
          <dd className="break-words">{msg.from}</dd>
          {msg.to && (
            <>
              <dt className="text-zinc-500">To</dt>
              <dd className="break-words">{msg.to}</dd>
            </>
          )}
          {msg.cc && (
            <>
              <dt className="text-zinc-500">Cc</dt>
              <dd className="break-words">{msg.cc}</dd>
            </>
          )}
          <dt className="text-zinc-500">Date</dt>
          <dd>{longDate(msg.sentAt) || msg.dateHeader}</dd>
          <dt className="text-zinc-500">Found in</dt>
          <dd className="flex flex-wrap gap-1">
            {msg.locations.map((l) => (
              <span
                key={`${l.account}/${l.folder}/${l.uid}`}
                className="rounded bg-zinc-100 px-1.5 py-0.5 text-xs dark:bg-zinc-800"
              >
                {l.account} / {l.folder}
              </span>
            ))}
          </dd>
        </dl>
        <div className="mt-3 flex flex-wrap items-center gap-2 text-sm">
          {msg.hasHtml && (
            <button type="button" className="rounded border border-zinc-300 px-2 py-0.5 dark:border-zinc-700" onClick={() => setShowHTML(!showHTML)}>
              {showHTML ? "Show text" : "Show HTML"}
            </button>
          )}
          {msg.hasHtml && showHTML && !images && (
            <button
              type="button"
              className="rounded border border-zinc-300 px-2 py-0.5 dark:border-zinc-700"
              title="Remote images can tell the sender that you opened this message."
              onClick={() => setImages(true)}
            >
              Load remote images
            </button>
          )}
          <a className="text-blue-600 hover:underline dark:text-blue-400" href={messageURL.raw(msg.id)} download>
            Download .eml
          </a>
        </div>
        {attachments.length > 0 && (
          <ul aria-label="Attachments" className="mt-3 flex flex-wrap gap-2">
            {attachments.map((p) => (
              <li key={p.index}>
                <a
                  href={messageURL.part(msg.id, p.index)}
                  download={p.filename || undefined}
                  className="inline-flex items-center gap-1 rounded border border-zinc-300 px-2 py-1 text-xs hover:bg-zinc-50 dark:border-zinc-700 dark:hover:bg-zinc-900"
                >
                  📎 <span className="max-w-60 truncate">{p.filename || `part ${p.index}`}</span>
                  <span className="text-zinc-500">{fileSize(p.size)}</span>
                </a>
              </li>
            ))}
          </ul>
        )}
        {msg.truncated && <p className="mt-2 text-xs text-amber-700">This message is very large; the preview is shortened.</p>}
      </header>
      {msg.hasHtml && showHTML ? (
        // No allow-scripts and no allow-same-origin: the mail's HTML runs with
        // an opaque origin and cannot reach the API. The server's CSP adds
        // another layer (no scripts, no forms, no remote content).
        <iframe
          // A new element per mode: changing src would add a browser history
          // entry, so Back would toggle images instead of leaving the message.
          key={images ? "with-images" : "no-images"}
          title="Message content"
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
