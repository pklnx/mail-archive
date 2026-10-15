import type { ReactNode } from "react";
import { day } from "./format";
import { parseQuery, setToken, type TokenKey } from "./searchSyntax";
import { t } from "./i18n";

interface Props {
  query: string;
  setQuery: (q: string) => void;
  group: boolean;
  setGroup: (on: boolean) => void;
  /** Opens the sheet with all filter fields. */
  openFilters: () => void;
}

const chip = "flex h-8 shrink-0 items-center gap-1 rounded-full border text-[13px] whitespace-nowrap transition-colors pointer-coarse:h-9";
const off = `${chip} border-line-control px-3 hover:bg-surface-hover`;
const on = `${chip} border-accent-soft bg-accent-soft font-medium text-accent-soft-ink`;

/**
 * The filters on phones: one row of chips under the search. Yes/no filters
 * switch directly, the others open the filter sheet; a set filter shows its
 * value and can be removed. Like SearchFilters, they edit the search text.
 */
export function FilterChips({ query, setQuery, group, setGroup, openFilters }: Props) {
  const p = parseQuery(query);
  const clear = (...keys: TokenKey[]) => setQuery(keys.reduce((q, k) => setToken(q, k, ""), query));
  const valueChip = (label: string, value: string, keys: TokenKey[]) =>
    value ? (
      <span key={label} className={`${on} pl-3`}>
        <button type="button" onClick={openFilters}>
          {label}: {value}
        </button>
        <button type="button" aria-label={t.removeFilter(label)} className="grid size-7 place-items-center rounded-full" onClick={() => clear(...keys)}>
          <span aria-hidden>×</span>
        </button>
      </span>
    ) : (
      <button key={label} type="button" className={off} onClick={openFilters}>
        {label}
      </button>
    );
  const toggle = (label: string, value: boolean, set: (v: boolean) => void): ReactNode => (
    <button key={label} type="button" aria-pressed={value} className={value ? `${on} px-3` : off} onClick={() => set(!value)}>
      {label}
    </button>
  );
  const range = p.after || p.before ? `${p.after ? day(p.after) : "…"} – ${p.before ? day(p.before) : "…"}` : "";
  return (
    <div role="group" aria-label={t.filters} className="flex gap-2 overflow-x-auto border-b border-line px-2 py-2 [scrollbar-width:none] md:hidden">
      {toggle(t.hasAttachment, p.hasAttachment, (v) => setQuery(setToken(query, "has", v ? "attachment" : "")))}
      {valueChip(t.dateRange, range, ["after", "before"])}
      {valueChip(t.from, p.from, ["from"])}
      {valueChip(t.toOrCc, p.to, ["to"])}
      {toggle(t.conversation, group, setGroup)}
    </div>
  );
}
