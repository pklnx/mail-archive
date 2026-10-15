import { parseQuery, setToken, type TokenKey } from "./searchSyntax";
import { t } from "./i18n";

interface Props {
  /** The text in the search field. */
  query: string;
  setQuery: (q: string) => void;
  /** One row per conversation; kept in the URL, not in the search text. */
  group: boolean;
  setGroup: (on: boolean) => void;
}

const field =
  "w-full min-w-0 rounded border border-line-control bg-field px-2 py-1 text-xs pointer-coarse:min-h-11";

/**
 * Filters under the search field, for people who do not type prefixes.
 * They edit the prefix tokens in the search text, so both stay in sync.
 * Attachment names (attachment:) are typed only.
 */
export function SearchFilters({ query, setQuery, group, setGroup }: Props) {
  const p = parseQuery(query);
  const set = (key: TokenKey, value: string) => setQuery(setToken(query, key, value));
  const input = (key: "from" | "to" | "after" | "before", label: string, type: "text" | "date") => (
    <label className="flex min-w-0 flex-col gap-0.5">
      <span className="text-ink-muted">{label}</span>
      <input type={type} className={field} value={p[key]} onChange={(e) => set(key, e.target.value)} />
    </label>
  );
  return (
    <div role="group" aria-label={t.filters} className="grid grid-cols-2 gap-x-2 gap-y-1.5 border-b border-line p-2 text-xs">
      {input("from", t.from, "text")}
      {input("to", t.toOrCc, "text")}
      {input("after", t.since, "date")}
      {input("before", t.before, "date")}
      <label className="flex items-center gap-2 pointer-coarse:min-h-11">
        <input type="checkbox" checked={p.hasAttachment} onChange={(e) => set("has", e.target.checked ? "attachment" : "")} />
        {t.hasAttachment}
      </label>
      <label className="flex items-center gap-2 pointer-coarse:min-h-11">
        <input type="checkbox" checked={group} onChange={(e) => setGroup(e.target.checked)} />
        {t.groupByConversation}
      </label>
    </div>
  );
}
