import { Checkbox, DismissibleTag, Layer, TextInput } from "@carbon/react";
import { day } from "./format";
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

/**
 * The filter panel under the search field, for people who do not type
 * prefixes. The filter button toggles it, on phones and desktops alike.
 * The fields edit the prefix tokens in the search text, so both stay in
 * sync. Attachment names (attachment:) are typed only.
 */
export function SearchFilters({ id, query, setQuery, group, setGroup }: Props & { id?: string }) {
  const p = parseQuery(query);
  const set = (key: TokenKey, value: string) => setQuery(setToken(query, key, value));
  const input = (key: "from" | "to" | "after" | "before", label: string, type: "text" | "date") => (
    <TextInput id={`${id ?? "filters"}-${key}`} size="sm" type={type} labelText={label} value={p[key]} onChange={(e) => set(key, e.target.value)} />
  );
  return (
    // A layer up from the list, so the fields contrast with the panel.
    <Layer id={id} role="group" aria-label={t.filters} className="search-filters">
      {input("from", t.from, "text")}
      {input("to", t.toOrCc, "text")}
      {input("after", t.since, "date")}
      {input("before", t.before, "date")}
      <Checkbox
        id={`${id ?? "filters"}-has`}
        labelText={t.hasAttachment}
        checked={p.hasAttachment}
        onChange={(_, { checked }) => set("has", checked ? "attachment" : "")}
      />
      <Checkbox id={`${id ?? "filters"}-group`} labelText={t.groupByConversation} checked={group} onChange={(_, { checked }) => setGroup(checked)} />
    </Layer>
  );
}

/**
 * The filters that are set, as tags under the search field; each one's
 * dismiss button removes it from the search text (or the grouping). A date
 * range is one tag for both ends.
 */
export function ActiveFilters({ query, setQuery, group, setGroup }: Props) {
  const p = parseQuery(query);
  const clear = (...keys: TokenKey[]) => setQuery(keys.reduce((q, k) => setToken(q, k, ""), query));
  const range = p.after || p.before ? `${p.after ? day(p.after) : "…"} – ${p.before ? day(p.before) : "…"}` : "";
  const tags: { label: string; text: string; remove: () => void }[] = [];
  if (p.hasAttachment) tags.push({ label: t.hasAttachment, text: t.hasAttachment, remove: () => clear("has") });
  if (range) tags.push({ label: t.dateRange, text: `${t.dateRange}: ${range}`, remove: () => clear("after", "before") });
  if (p.from) tags.push({ label: t.from, text: `${t.from}: ${p.from}`, remove: () => clear("from") });
  if (p.to) tags.push({ label: t.toOrCc, text: `${t.toOrCc}: ${p.to}`, remove: () => clear("to") });
  if (group) tags.push({ label: t.conversation, text: t.groupByConversation, remove: () => setGroup(false) });
  if (!tags.length) return null;
  return (
    <ul aria-label={t.activeFilters} className="active-filters">
      {tags.map((tag) => (
        <li key={tag.label}>
          <DismissibleTag
            size="sm"
            type="gray"
            text={tag.text}
            title={t.removeFilter(tag.label)}
            dismissTooltipLabel={t.removeFilter(tag.label)}
            onClose={tag.remove}
          />
        </li>
      ))}
    </ul>
  );
}
