import { Fragment, useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { listMessages, type Filter, type MessageSummary } from "./api";
import { useAsync } from "./useAsync";
import { dateGroup, senderName, shortDate } from "./format";
import { Highlight } from "./Highlight";
import { EmptyState } from "./EmptyState";
import { SearchIcon } from "./icons";
import { MessageRowsSkeleton } from "./Skeleton";
import { button } from "./formStyles";
import type { ListNav } from "./pager";
import { t } from "./i18n";

interface Props {
  filter: Filter;
  selected: string;
  open: (id: string) => void;
  /** Gets the loaded messages in list order, for paging in the reading pane. */
  onNav?: (nav: ListNav) => void;
  /** Next steps when a search finds nothing. */
  clearSearch?: () => void;
  searchEverywhere?: () => void;
}

interface ListState {
  items: MessageSummary[];
  cursor: string | null;
  loading: boolean;
  error: string;
  done: boolean;
}

const initial: ListState = { items: [], cursor: null, loading: true, error: "", done: false };

export function MessageList({ filter, selected, open, onNav, clearSearch, searchEverywhere }: Props) {
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

  // The reading pane pages through what is loaded and asks for more at the end.
  const { items, cursor, done, loading, error } = list;
  useEffect(() => {
    const loadMore = () => {
      if (!loading && !error) load(cursor, generation.current);
    };
    onNav?.({ ids: items.map((m) => m.id), more: !done, loadMore });
  }, [items, cursor, done, loading, error, load, onNav]);

  const kind = searching ? "matches" : filter.group ? "conversations" : "messages";
  let group = "";
  return (
    <div className="flex h-full flex-col">
      {items.length > 0 && (
        <p className="flex shrink-0 items-center justify-between gap-2 border-b border-line px-4 py-1.5 text-xs text-ink-muted">
          <span>{t.resultCount(items.length, !done, kind)}</span>
          <span>{t.newestFirst}</span>
        </p>
      )}
      {/* scroll-pt-9: rows scrolled into view stay clear of the sticky date heading. */}
      <ul aria-label={t.messages} aria-busy={loading} className="min-h-0 flex-1 scroll-pt-9 overflow-y-auto">
        {items.map((m) => {
          // A heading where the date group changes (the list is newest first).
          const g = dateGroup(m.sortAt);
          const heading = g !== group;
          group = g;
          return (
            <Fragment key={m.id}>
              {heading && (
                <li role="heading" aria-level={2} className="sticky top-0 z-[1] border-b border-line bg-bg px-4 pt-2 pb-1 text-xs font-semibold text-ink-muted">
                  {g}
                </li>
              )}
              <li>
                {filter.group && m.thread && (m.count ?? 1) > 1 ? (
                  <ThreadRow m={m} filter={filter} selected={selected} open={open} />
                ) : (
                  <Row m={m} selected={selected} open={open} />
                )}
              </li>
            </Fragment>
          );
        })}
        {error && (
          <li className="p-4 text-sm text-danger">
            {t.loadMessagesFailed(error)}{" "}
            <button type="button" className="underline" onClick={() => load(cursor, generation.current)}>
              {t.retry}
            </button>
          </li>
        )}
        {!loading && !error && items.length === 0 && (
          <li>
            <Empty searching={searching} clearSearch={clearSearch} searchEverywhere={searchEverywhere} />
          </li>
        )}
        {loading && (
          <>
            <li className="sr-only" role="status">
              {t.loading}
            </li>
            <MessageRowsSkeleton rows={items.length ? 1 : 6} />
          </>
        )}
        {!done && <li ref={sentinel} aria-hidden className="h-px" />}
      </ul>
    </div>
  );
}

function Empty({ searching, clearSearch, searchEverywhere }: { searching: boolean; clearSearch?: () => void; searchEverywhere?: () => void }) {
  if (!searching) return <EmptyState icon={inboxIcon} title={t.noMessages} />;
  const actions: ReactNode[] = [];
  if (clearSearch)
    actions.push(
      <button key="clear" type="button" className={button} onClick={clearSearch}>
        {t.clearSearch}
      </button>,
    );
  if (searchEverywhere)
    actions.push(
      <button key="all" type="button" className={button} onClick={searchEverywhere}>
        {t.searchEverywhere}
      </button>,
    );
  return <EmptyState icon={<SearchIcon />} title={t.noMatches} actions={actions.length ? actions : undefined} />;
}

const inboxIcon = (
  <svg aria-hidden width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
    <path d="M22 12h-6l-2 3h-4l-2-3H2" />
    <path d="M5.5 5.1L2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.5-6.9A2 2 0 0 0 16.7 4H7.3a2 2 0 0 0-1.8 1.1z" />
  </svg>
);

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
  // Paging in the reading pane moves the selection; keep it in view.
  const ref = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (on) ref.current?.scrollIntoView?.({ block: "nearest" });
  }, [on]);
  return (
    <button
      ref={ref}
      type="button"
      onClick={() => open(m.id)}
      aria-current={on ? "true" : undefined}
      className={`block w-full min-w-0 border-b border-line py-3 pr-4 text-left ${nested ? "pl-8" : "pl-4"} ${
        on ? "bg-accent-soft" : "hover:bg-surface-sunken"
      }`}
    >
      <div className="flex items-baseline justify-between gap-2">
        <span className="flex min-w-0 items-baseline gap-1.5">
          <span className="truncate text-sm font-semibold">{senderName(m.from)}</span>
          {count !== undefined && (
            <span
              className="shrink-0 rounded-full bg-line px-1.5 text-xs font-medium text-ink"
              title={t.inConversation(count)}
              aria-label={t.inConversation(count)}
            >
              {count}
            </span>
          )}
        </span>
        <span className="flex shrink-0 items-center gap-1 text-xs text-ink-muted">
          {m.hasAttachment && paperclip}
          <time dateTime={m.sortAt}>{shortDate(m.sentAt ?? m.sortAt)}</time>
        </span>
      </div>
      <div className="truncate text-sm">{m.subject || t.noSubject}</div>
      {m.snippet && !nested && (
        <p className="mt-0.5 line-clamp-2 text-xs text-ink-muted">
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
          aria-label={expanded ? t.hideConversation : t.showConversation(count - 1)}
          title={expanded ? t.hideConversation : t.showConversation(count - 1)}
          className="shrink-0 border-b border-line px-2 pointer-coarse:px-3.5 text-ink-muted hover:bg-surface-hover hover:text-ink"
          onClick={() => setExpanded(!expanded)}
        >
          <svg aria-hidden width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className={expanded ? "rotate-180" : ""}>
            <path d="M6 9l6 6 6-6" />
          </svg>
        </button>
      </div>
      {expanded && (
        <ThreadMembers id={id} filter={{ ...filter, group: false, thread: m.thread }} newest={m.id} count={count} selected={selected} open={open} />
      )}
    </>
  );
}

/** At most this many messages of an expanded conversation (the API's page limit). */
const maxMembers = 200;

/** The messages of an expanded conversation without its newest one, which the row above shows. */
export function earlierMembers(members: MessageSummary[], newest: string): MessageSummary[] {
  return members.filter((m) => m.id !== newest);
}

interface ThreadMembersProps {
  id: string;
  filter: Filter;
  /** The message of the grouped row. */
  newest: string;
  count: number;
  selected: string;
  open: (id: string) => void;
}

function ThreadMembers({ id, filter, newest, count, selected, open }: ThreadMembersProps) {
  const key = JSON.stringify(filter);
  const res = useAsync((signal) => listMessages(filter, null, signal, maxMembers), [key]);
  const members = res.status === "ok" ? earlierMembers(res.data.messages, newest) : [];
  const hidden = count - 1 - members.length;
  return (
    <ul id={id} aria-label={t.conversation} className="bg-surface-sunken">
      {res.status === "loading" && <li className="py-2 pl-8 text-xs text-ink-muted">{t.loading}</li>}
      {res.status === "error" && <li className="py-2 pl-8 text-xs text-danger">{t.loadMessagesFailed(res.error.message)}</li>}
      {res.status === "ok" &&
        members.map((x) => (
          <li key={x.id}>
            <Row m={x} selected={selected} open={open} nested />
          </li>
        ))}
      {res.status === "ok" && hidden > 0 && <li className="py-2 pl-8 text-xs text-ink-muted">{t.moreInConversation(hidden)}</li>}
    </ul>
  );
}