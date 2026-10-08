import { useCallback, useEffect, useRef, useState } from "react";
import { listMessages, type Filter, type MessageSummary } from "./api";
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
      {list.items.map((m) => {
        const on = m.id === selected;
        return (
          <li key={m.id}>
            <button
              type="button"
              onClick={() => open(m.id)}
              aria-current={on ? "true" : undefined}
              className={`block w-full border-b border-zinc-200 px-4 py-3 text-left dark:border-zinc-800 ${
                on ? "bg-blue-50 dark:bg-blue-950/50" : "hover:bg-zinc-50 dark:hover:bg-zinc-900"
              }`}
            >
              <div className="flex items-baseline justify-between gap-2">
                <span className="truncate text-sm font-semibold">{senderName(m.from)}</span>
                <span className="flex shrink-0 items-center gap-1 text-xs text-zinc-500">
                  {m.hasAttachment && (
                    <svg role="img" aria-label={t.hasAttachment} width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                      <title>{t.hasAttachment}</title>
                      <path d="M21.4 11.1l-9.2 9.2a6 6 0 0 1-8.5-8.5l9.2-9.2a4 4 0 0 1 5.7 5.7l-9.2 9.2a2 2 0 0 1-2.8-2.8l8.5-8.5" />
                    </svg>
                  )}
                  <time dateTime={m.sortAt}>{shortDate(m.sentAt ?? m.sortAt)}</time>
                </span>
              </div>
              <div className="truncate text-sm">{m.subject || t.noSubject}</div>
              {m.snippet && (
                <p className="mt-0.5 line-clamp-2 text-xs text-zinc-500 dark:text-zinc-400">
                  <Highlight text={m.snippet} />
                </p>
              )}
            </button>
          </li>
        );
      })}
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
