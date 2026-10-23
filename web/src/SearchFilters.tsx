import { useEffect, useRef } from "react";
import { Button, Checkbox, DismissibleTag, Layer, SelectableTag, TextInput } from "@carbon/react";
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

/** A field of the filter panel. */
export type FilterField = "from" | "to" | "after" | "before" | "has" | "group";

/** A request to focus a panel field. Each "Edit filters" press counts n up, so a second press focuses again. */
export interface FocusRequest {
  field: FilterField;
  n: number;
}

/**
 * The field "Edit filters" moves focus to: the first visible tag's field,
 * in the panel's order (From, To or Cc, Since, Before, Has attachment,
 * Group by conversation). A date range is Since if set, otherwise Before.
 * With quick (phones) has and group have no tag, so they are skipped.
 * Empty when no tag is visible.
 */
export function editTarget(query: string, group: boolean, quick = false): FilterField | "" {
  const p = parseQuery(query);
  if (p.from) return "from";
  if (p.to) return "to";
  if (p.after) return "after";
  if (p.before) return "before";
  if (quick) return "";
  if (p.hasAttachment) return "has";
  if (group) return "group";
  return "";
}

/**
 * The filter panel under the search field, for people who do not type
 * prefixes. The filter button toggles it, on phones and desktops alike.
 * The fields edit the prefix tokens in the search text, so both stay in
 * sync. Attachment names (attachment:) are typed only.
 *
 * A focus request moves focus to one field once the panel is there, or
 * right away when it already is. onFocused then acknowledges it, so the
 * panel does not take the focus again when it is reopened later.
 */
export function SearchFilters({
  id,
  query,
  setQuery,
  group,
  setGroup,
  focus,
  onFocused,
}: Props & { id?: string; focus?: FocusRequest; onFocused?: (n: number) => void }) {
  const p = parseQuery(query);
  const set = (key: TokenKey, value: string) => setQuery(setToken(query, key, value));
  const fields = useRef<Partial<Record<FilterField, HTMLInputElement | null>>>({});
  const ref = (key: FilterField) => (el: HTMLInputElement | null) => {
    fields.current[key] = el;
  };
  const request = focus?.n ?? 0;
  const field = focus?.field;
  const acknowledge = useRef(onFocused);
  acknowledge.current = onFocused;
  // After the commit, so the fields are mounted (no timers).
  useEffect(() => {
    if (!request || !field) return;
    fields.current[field]?.focus();
    acknowledge.current?.(request);
  }, [request, field]);
  const input = (key: "from" | "to" | "after" | "before", label: string, type: "text" | "date") => (
    <TextInput
      ref={ref(key)}
      id={`${id ?? "filters"}-${key}`}
      size="sm"
      type={type}
      labelText={label}
      value={p[key]}
      onChange={(e) => set(key, e.target.value)}
    />
  );
  return (
    // A layer up from the list, so the fields contrast with the panel.
    <Layer id={id} role="group" aria-label={t.filters} className="search-filters">
      {input("from", t.from, "text")}
      {input("to", t.toOrCc, "text")}
      {input("after", t.since, "date")}
      {input("before", t.before, "date")}
      <Checkbox
        ref={ref("has")}
        id={`${id ?? "filters"}-has`}
        labelText={t.hasAttachment}
        checked={p.hasAttachment}
        onChange={(_, { checked }) => set("has", checked ? "attachment" : "")}
      />
      <Checkbox
        ref={ref("group")}
        id={`${id ?? "filters"}-group`}
        labelText={t.groupByConversation}
        checked={group}
        onChange={(_, { checked }) => setGroup(checked)}
      />
    </Layer>
  );
}

/**
 * The two yes/no filters as one-tap Carbon SelectableTags, for phones
 * (below 48rem), where the panel starts closed. They edit the same search
 * text and grouping as the panel's checkboxes. Carbon calls onChange with
 * the new state.
 */
export function QuickFilters({ query, setQuery, group, setGroup }: Props) {
  const p = parseQuery(query);
  return (
    <div role="group" aria-label={t.quickFilters} className="quick-filters">
      <SelectableTag
        size="md"
        text={t.hasAttachment}
        selected={p.hasAttachment}
        onChange={(on: boolean) => setQuery(setToken(query, "has", on ? "attachment" : ""))}
      />
      <SelectableTag size="md" text={t.groupByConversation} selected={group} onChange={(on: boolean) => setGroup(on)} />
    </div>
  );
}

/**
 * The filters that are set, as tags under the search field; each one's
 * dismiss button removes it from the search text (or the grouping). A date
 * range is one tag for both ends. With quick (phones) has and group are
 * left out, because the quick filters show them. "Edit filters" at the
 * end calls onEdit; it is there only with at least one tag.
 */
export function ActiveFilters({ query, setQuery, group, setGroup, quick = false, onEdit }: Props & { quick?: boolean; onEdit?: () => void }) {
  const p = parseQuery(query);
  const clear = (...keys: TokenKey[]) => setQuery(keys.reduce((q, k) => setToken(q, k, ""), query));
  const range = p.after || p.before ? `${p.after ? day(p.after) : "…"} – ${p.before ? day(p.before) : "…"}` : "";
  const tags: { label: string; text: string; remove: () => void }[] = [];
  if (p.hasAttachment && !quick) tags.push({ label: t.hasAttachment, text: t.hasAttachment, remove: () => clear("has") });
  if (range) tags.push({ label: t.dateRange, text: `${t.dateRange}: ${range}`, remove: () => clear("after", "before") });
  if (p.from) tags.push({ label: t.from, text: `${t.from}: ${p.from}`, remove: () => clear("from") });
  if (p.to) tags.push({ label: t.toOrCc, text: `${t.toOrCc}: ${p.to}`, remove: () => clear("to") });
  if (group && !quick) tags.push({ label: t.conversation, text: t.groupByConversation, remove: () => setGroup(false) });
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
      {onEdit && (
        <li>
          <Button kind="ghost" size="sm" onClick={onEdit}>
            {t.editFilters}
          </Button>
        </li>
      )}
    </ul>
  );
}
