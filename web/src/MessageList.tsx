import { useCallback, useEffect, useRef, useState } from "react";
import { listMessages, type Filter, type MessageSummary } from "./api";
import { useAsync } from "./useAsync";
import { senderName, shortDate } from "./format";
import { Highlight } from "./Highlight";
import { t } from "./i18n";

interface Props {
  filter: Filter;
  selected: string;
  open: (id: string) => void;
}

interface ListState {
  items: MessageSummary[];
  cursor: string | null;
  loading: boolean;
  error: string;
  done: boolean;
}

const initial: ListState = { items: [], cursor: null, loading: true, error: "", done: false };

export function MessageList({ filter, selected, open }: Props) {
  const [list, setList] = useState<ListState>(initial);
  const key = JSON.stringify(filter);
  // Grouping is a view, not a search.
  const searching = Boolean(
    filter.q || filter.from || filter.to || filter.attachment || filter.hasAttachment || filter.after || filter.before,
  );
  const generation = useRef(0);

  const load = useCallback(
    (cursor: string | null, gen: number, signal?: AbortSignal) => {
      setList((s) => ({ ...s, loading: true, error: "" }));
      listMessages(filter, cursor, signal).then(
        (page) => {
          if (gen !== generation.current) return;
          setList((s) => ({
            items: cursor ? [...s.items, ...page.messages] : page.messages,
            cursor: page.nextCursor,
            loading: false,
            error: "",
            done: page.nextCursor === null,
          }));
        },
        (err: unknown) => {
          if (gen !== generation.current || signal?.aborted) return;
          setList((s) => ({ ...s, loading: false, error: err instanceof Error ? err.message : String(err) }));
        },
      );
    },
    // The filter is captured through `key`.
    [key],
  );

  // New filter: start from the first page.
  useEffect(() => {
    const gen = ++generation.current;
    const ctrl = new AbortController();
    setList(initial);
    load(null, gen, ctrl.signal);
    return () => ctrl.abort();
  }, [load]);

  // Load the next page when the sentinel at the end of the list scrolls into view.
  const sentinel = useRef<HTMLLIElement>(null);
  useEffect(() => {
    const el = sentinel.current;
    if (!el || list.done || list.loading || list.error) return;
    const obs = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting)) load(list.cursor, generation.current);
    });
    obs.observe(el);
    return () => obs.disconnect();
  }, [list.cursor, list.done, list.loading, list.error, load]);

  return (
    <ul aria-label={t.messages} className="h-full overflow-y-auto">
      {list.items.map((m) => (
        <li key={m.id}>
          {filter.group && m.thread && (m.count ?? 1) > 1 ? (
            <ThreadRow m={m} filter={filter} selected={selected} open={open} />
          ) : (
            <Row m={m} selected={selected} open={open} />
          )}
        </li>
      ))}
      {list.error && (
        <li className="p-4 text-sm text-red-600">
          {t.loadMessagesFailed(list.error)}{" "}
          <button type="button" className="underline" onClick={() => load(list.cursor, generation.current)}>
            {t.retry}
          </button>
        </li>
      )}
      {!list.loading && !list.error && list.items.length === 0 && (
        <li className="p-4 text-sm text-zinc-500">{searching ? t.noMatches : t.noMessages}</li>
      )}
      {list.loading && <li className="p-4 text-sm text-zinc-500">{t.loading}</li>}
      {!list.done && <li ref={sentinel} aria-hidden className="h-px" />}
    </ul>
  );
}

const paperclip = (
  <svg role="img" aria-label={t.hasAttachment} width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <title>{t.hasAttachment}</title>
    <path d="M21.4 11.1l-9.2 9.2a6 6 0 0 1-8.5-8.5l9.2-9.2a4 4 0 0 1 5.7 5.7l-9.2 9.2a2 2 0 0 1-2.8-2.8l8.5-8.5" />
  </svg>
);

interface RowProps {
  m: MessageSummary;
  selected: string;
  open: (id: string) => void;
  /** Rows of an expanded conversation are indented and more compact. */
  nested?: boolean;
  /** Messages of the conversation, shown as a badge. */
  count?: number;
}

function Row({ m, selected, open, nested, count }: RowProps) {
  const on = m.id === selected;
  return (
    <button
      type="button"
      onClick={() => open(m.id)}
      aria-current={on ? "true" : undefined}
      className={`block w-full min-w-0 border-b border-zinc-200 py-3 pr-4 text-left dark:border-zinc-800 ${nested ? "pl-8" : "pl-4"} ${
        on ? "bg-blue-50 dark:bg-blue-950/50" : "hover:bg-zinc-50 dark:hover:bg-zinc-900"
      }`}
    >
      <div className="flex items-baseline justify-between gap-2">
        <span className="flex min-w-0 items-baseline gap-1.5">
          <span className="truncate text-sm font-semibold">{senderName(m.from)}</span>
          {count !== undefined && (
            <span
              className="shrink-0 rounded-full bg-zinc-200 px-1.5 text-xs font-medium text-zinc-700 dark:bg-zinc-700 dark:text-zinc-200"
              title={t.inConversation(count)}
              aria-label={t.inConversation(count)}
            >
              {count}
            </span>
          )}
        </span>
        <span className="flex shrink-0 items-center gap-1 text-xs text-zinc-500">
          {m.hasAttachment && paperclip}
          <time dateTime={m.sortAt}>{shortDate(m.sentAt ?? m.sortAt)}</time>
        </span>
      </div>
      <div className="truncate text-sm">{m.subject || t.noSubject}</div>
      {m.snippet && !nested && (
        <p className="mt-0.5 line-clamp-2 text-xs text-zinc-500 dark:text-zinc-400">
          <Highlight text={m.snippet} />
        </p>
      )}
    </button>
  );
}

/** The newest message of a conversation; expands to all its matching messages. */
function ThreadRow({ m, filter, selected, open }: { m: MessageSummary; filter: Filter; selected: string; open: (id: string) => void }) {
  const [expanded, setExpanded] = useState(false);
  const count = m.count ?? 1;
  const id = `thread-${m.id}`;
  return (
    <>
      <div className="flex items-stretch">
        <div className="min-w-0 flex-1">
          <Row m={m} selected={selected} open={open} count={count} />
        </div>
        <button
          type="button"
          aria-expanded={expanded}
          aria-controls={id}
          aria-label={expanded ? t.hideConversation : t.showConversation(count)}
          title={expanded ? t.hideConversation : t.showConversation(count)}
          className="shrink-0 border-b border-zinc-200 px-2 text-zinc-500 hover:bg-zinc-100 hover:text-zinc-800 dark:border-zinc-800 dark:hover:bg-zinc-800 dark:hover:text-zinc-100"
          onClick={() => setExpanded(!expanded)}
        >
          <svg aria-hidden width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className={expanded ? "rotate-180" : ""}>
            <path d="M6 9l6 6 6-6" />
          </svg>
        </button>
      </div>
      {expanded && <ThreadMembers id={id} filter={{ ...filter, group: false, thread: m.thread }} count={count} selected={selected} open={open} />}
    </>
  );
}

/** At most this many messages of an expanded conversation (the API's page limit). */
const maxMembers = 200;

function ThreadMembers({ id, filter, count, selected, open }: { id: string; filter: Filter; count: number; selected: string; open: (id: string) => void }) {
  const key = JSON.stringify(filter);
  const res = useAsync((signal) => listMessages(filter, null, signal, maxMembers), [key]);
  return (
    <ul id={id} aria-label={t.conversation} className="bg-zinc-50/60 dark:bg-zinc-900/40">
      {res.status === "loading" && <li className="py-2 pl-8 text-xs text-zinc-500">{t.loading}</li>}
      {res.status === "error" && <li className="py-2 pl-8 text-xs text-red-600">{t.loadMessagesFailed(res.error.message)}</li>}
      {res.status === "ok" &&
        res.data.messages.map((x) => (
          <li key={x.id}>
            <Row m={x} selected={selected} open={open} nested />
          </li>
        ))}
      {res.status === "ok" && count > res.data.messages.length && (
        <li className="py-2 pl-8 text-xs text-zinc-500">{t.moreInConversation(count - res.data.messages.length)}</li>
      )}
    </ul>
  );
}