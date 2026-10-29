import { Fragment, useCallback, useEffect, useRef, useState } from "react";
import { Button, SkeletonText, Tag } from "@carbon/react";
import { Attachment, ChevronDown, Email, Search } from "@carbon/icons-react";
import { listMessages, type Filter, type MessageSummary } from "./api";
import { useAsync } from "./useAsync";
import { dateGroup, senderName, shortDate } from "./format";
import { Highlight } from "./Highlight";
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
  // Every message of the "only in archive" view is gone from the servers.
  const gone = filter.gone === true;
  let group = "";
  return (
    <div className="mail-list">
      {items.length > 0 && (
        <p className="mail-list__count">
          <span>{t.resultCount(items.length, !done, kind)}</span>
          <span>{t.newestFirst}</span>
        </p>
      )}
      <ul aria-label={t.messages} aria-busy={loading} className="mail-list__items">
        {items.map((m) => {
          // A heading where the date group changes (the list is newest first).
          const g = dateGroup(m.sortAt);
          const heading = g !== group;
          group = g;
          return (
            <Fragment key={m.id}>
              {heading && (
                <li role="heading" aria-level={2} className="mail-list__group">
                  {g}
                </li>
              )}
              <li>
                {filter.group && m.thread && (m.count ?? 1) > 1 ? (
                  <ThreadRow m={m} filter={filter} selected={selected} open={open} gone={gone} />
                ) : (
                  <Row m={m} selected={selected} open={open} gone={gone} />
                )}
              </li>
            </Fragment>
          );
        })}
        {error && (
          <li className="mail-list__note app-text-error">
            {t.loadMessagesFailed(error)}{" "}
            <Button kind="ghost" size="sm" onClick={() => load(cursor, generation.current)}>
              {t.retry}
            </Button>
          </li>
        )}
        {!loading && !error && items.length === 0 && (
          <li>
            <Empty searching={searching} clearSearch={clearSearch} searchEverywhere={searchEverywhere} />
          </li>
        )}
        {loading && (
          <>
            <li className="cds--visually-hidden" role="status">
              {t.loading}
            </li>
            <RowsSkeleton rows={items.length ? 1 : 6} />
          </>
        )}
        {!done && <li ref={sentinel} aria-hidden className="mail-list__sentinel" />}
      </ul>
    </div>
  );
}

/** Rows of the list while it loads, in the shape of the content. Screen readers hear "Loading" instead. */
function RowsSkeleton({ rows, nested = false }: { rows: number; nested?: boolean }) {
  return Array.from({ length: rows }, (_, i) => (
    <li key={i} aria-hidden className={nested ? "mail-row mail-row--nested mail-row--skeleton" : "mail-row mail-row--skeleton"}>
      <SkeletonText width={["40%", "30%", "50%"][i % 3]} />
      <SkeletonText width={["75%", "66%", "50%"][i % 3]} />
      {/* A message of a conversation has no snippet line. */}
      {!nested && <SkeletonText width="85%" />}
    </li>
  ));
}

/** Says why the list is empty and offers the next step. */
function Empty({ searching, clearSearch, searchEverywhere }: { searching: boolean; clearSearch?: () => void; searchEverywhere?: () => void }) {
  const Icon = searching ? Search : Email;
  return (
    <div className="empty-state">
      <Icon size={32} aria-hidden className="empty-state__icon" />
      <p className="empty-state__title">{searching ? t.noMatches : t.noMessages}</p>
      {searching && (clearSearch || searchEverywhere) && (
        <div className="empty-state__actions">
          {clearSearch && (
            <Button kind="tertiary" size="sm" onClick={clearSearch}>
              {t.clearSearch}
            </Button>
          )}
          {searchEverywhere && (
            <Button kind="tertiary" size="sm" onClick={searchEverywhere}>
              {t.searchEverywhere}
            </Button>
          )}
        </div>
      )}
    </div>
  );
}

interface RowProps {
  m: MessageSummary;
  selected: string;
  open: (id: string) => void;
  /** Rows of an expanded conversation are indented and more compact. */
  nested?: boolean;
  /** Messages of the conversation, shown as a tag. */
  count?: number;
  /** The message is no longer on any server ("only in archive"). */
  gone?: boolean;
}

function Row({ m, selected, open, nested, count, gone }: RowProps) {
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
      className={`mail-row${nested ? " mail-row--nested" : ""}${on ? " mail-row--selected" : ""}`}
    >
      <span className="mail-row__top">
        <span className="mail-row__from">{senderName(m.from)}</span>
        {count !== undefined && (
          <Tag as="span" size="sm" type="outline" className="mail-row__tag">
            <span aria-hidden>{count}</span>
            <span className="cds--visually-hidden">{t.inConversation(count)}</span>
          </Tag>
        )}
        <span className="mail-row__meta">
          {m.hasAttachment && <Attachment size={16} role="img" aria-label={t.hasAttachment} />}
          <time dateTime={m.sortAt}>{shortDate(m.sentAt ?? m.sortAt)}</time>
        </span>
      </span>
      <span className="mail-row__subject">{m.subject || t.noSubject}</span>
      {m.snippet && !nested && (
        <span className="mail-row__snippet">
          <Highlight text={m.snippet} />
        </span>
      )}
      {gone && !nested && (
        <span className="mail-row__tags">
          <Tag as="span" size="sm" type="warm-gray">
            {t.onlyInArchive}
          </Tag>
        </span>
      )}
    </button>
  );
}

/** The newest message of a conversation; expands to all its matching messages. */
function ThreadRow({ m, filter, selected, open, gone }: { m: MessageSummary; filter: Filter; selected: string; open: (id: string) => void; gone: boolean }) {
  const [expanded, setExpanded] = useState(false);
  const count = m.count ?? 1;
  const id = `thread-${m.id}`;
  const label = expanded ? t.hideConversation : t.showConversation(count - 1);
  return (
    <>
      <div className="mail-thread">
        <div className="mail-thread__row">
          <Row m={m} selected={selected} open={open} count={count} gone={gone} />
        </div>
        <button
          type="button"
          aria-expanded={expanded}
          aria-controls={id}
          aria-label={label}
          title={label}
          className={`mail-thread__toggle${expanded ? " mail-thread__toggle--open" : ""}`}
          onClick={() => setExpanded(!expanded)}
        >
          <ChevronDown size={16} aria-hidden />
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
    <ul id={id} aria-label={t.conversation} className="mail-thread__members">
      {res.status === "loading" && (
        <>
          <li className="cds--visually-hidden" role="status">
            {t.loading}
          </li>
          <RowsSkeleton rows={Math.min(count - 1, 3)} nested />
        </>
      )}
      {res.status === "error" && <li className="mail-thread__note app-text-error">{t.loadMessagesFailed(res.error.message)}</li>}
      {res.status === "ok" &&
        members.map((x) => (
          <li key={x.id}>
            <Row m={x} selected={selected} open={open} nested />
          </li>
        ))}
      {res.status === "ok" && hidden > 0 && <li className="mail-thread__note app-text-secondary">{t.moreInConversation(hidden)}</li>}
    </ul>
  );
}
