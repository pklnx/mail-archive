import { useEffect, useRef, type RefObject } from "react";
import { Button, DismissibleTag, SelectableTag } from "@carbon/react";
import { localDay, shiftDay } from "./format";
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

/** A field of the filter form (FilterPanel.tsx, in the filter button's popover). */
export type FilterField = "from" | "to" | "after" | "before" | "has" | "group";

/** A request to focus a panel field. Each "Edit filters" press counts n up, so a second press focuses again. */
export interface FocusRequest {
  field: FilterField;
  n: number;
}

/**
 * The field "Edit filters" moves focus to: the first visible tag's field,
 * in the form's order (From, To or Cc, Since, Until, Has attachment,
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
 * The two yes/no filters as one-tap Carbon SelectableTags, for phones
 * (below 48rem). They edit the same search
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
 * range is one tag for both ends, its end shown inclusive (before: − 1
 * day). With quick (phones) has and group are left out, because the quick
 * filters show them. "Edit filters" at the end calls onEdit; it is there
 * only with at least one tag.
 *
 * After a dismiss the focus goes to the next tag's dismiss button, else the
 * previous one's, else to fallbackFocus (the filter button).
 */
export function ActiveFilters({
  query,
  setQuery,
  group,
  setGroup,
  quick = false,
  onEdit,
  fallbackFocus,
}: Props & { quick?: boolean; onEdit?: () => void; fallbackFocus?: RefObject<HTMLElement | null> }) {
  const p = parseQuery(query);
  const clear = (...keys: TokenKey[]) => setQuery(keys.reduce((q, k) => setToken(q, k, ""), query));
  const range = p.after || p.before ? `${p.after ? localDay(p.after) : "…"} – ${p.before ? localDay(shiftDay(p.before, -1)) : "…"}` : "";
  const tags: { label: string; text: string; remove: () => void }[] = [];
  if (p.hasAttachment && !quick) tags.push({ label: t.hasAttachment, text: t.hasAttachment, remove: () => clear("has") });
  if (range) tags.push({ label: t.dateRange, text: `${t.dateRange}: ${range}`, remove: () => clear("after", "before") });
  if (p.from) tags.push({ label: t.from, text: `${t.from}: ${p.from}`, remove: () => clear("from") });
  if (p.to) tags.push({ label: t.toOrCc, text: `${t.toOrCc}: ${p.to}`, remove: () => clear("to") });
  if (group && !quick) tags.push({ label: t.conversation, text: t.groupByConversation, remove: () => setGroup(false) });
  // The dismissed tag's place and the tag count then; the focus moves once the count changed.
  const dismissed = useRef<{ at: number; count: number } | null>(null);
  const list = useRef<HTMLUListElement>(null);
  const count = tags.length;
  useEffect(() => {
    const d = dismissed.current;
    if (!d || d.count === count) return;
    dismissed.current = null;
    const buttons = list.current?.querySelectorAll<HTMLElement>(".cds--tag__close-icon");
    (buttons?.[d.at] ?? buttons?.[d.at - 1] ?? fallbackFocus?.current)?.focus();
  });
  if (!tags.length) return null;
  return (
    <ul ref={list} aria-label={t.activeFilters} className="active-filters">
      {tags.map((tag, i) => (
        <li key={tag.label}>
          <DismissibleTag
            size="sm"
            type="gray"
            text={tag.text}
            title={t.removeFilter(tag.label)}
            dismissTooltipLabel={t.removeFilter(tag.label)}
            onClose={() => {
              dismissed.current = { at: i, count };
              tag.remove();
            }}
          />
        </li>
      ))}
      {onEdit && (
        <li>
          <Button
            kind="ghost"
            size="sm"
            onClick={(e) => {
              // The filter popover's outside-click listener on window would
              // take this click, which opens it, as one that closes it.
              e.stopPropagation();
              onEdit();
            }}
          >
            {t.editFilters}
          </Button>
        </li>
      )}
    </ul>
  );
}
