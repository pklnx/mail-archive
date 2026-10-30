import { useEffect, useRef, useState, type KeyboardEvent, type MutableRefObject } from "react";
import { Checkbox, Layer, RadioButton, RadioButtonGroup, TextInput } from "@carbon/react";
import { DateRange, type PickerHandle } from "./DateRange";
import { shiftDay } from "./format";
import { presetOf, presetRange, presets, type Preset } from "./datePresets";
import { filterText } from "./filterText";
import { t } from "./i18n";
import { parseQuery, setToken, type TokenKey } from "./searchSyntax";
import { md, useMediaQuery } from "./breakpoints";
import type { FilterField, FocusRequest } from "./SearchFilters";

interface Props {
  /** The base of the field ids. */
  id: string;
  query: string;
  setQuery: (q: string) => void;
  group: boolean;
  setGroup: (on: boolean) => void;
  focus?: FocusRequest;
  onFocused?: (n: number) => void;
  /**
   * Set while the form is mounted: whether its calendar is open right now.
   * The popover ignores its close requests while it is (App.tsx).
   */
  calendarOpen: MutableRefObject<() => boolean>;
}

/**
 * The filter form in the filter button's popover (#286), a lazy chunk of its
 * own: it is mounted only while the popover is open. The fields edit the
 * prefix tokens in the search text, so both stay in sync; attachment names
 * (attachment:) are typed only.
 *
 * The date range is the shared DateRange with an inclusive end (Since /
 * Until); the search keeps the exclusive before: = end + 1 day. The presets
 * only fill after: and before:. The selected one follows the search, except
 * that Custom stays selected once it was chosen or the range was edited by
 * hand, until the form closes.
 *
 * A focus request moves focus to one field once the form is there, or
 * right away when it already is; onFocused then acknowledges it.
 */
export function FilterPanel({ id, query, setQuery, group, setGroup, focus, onFocused, calendarOpen }: Props) {
  const ft = filterText();
  const p = parseQuery(query);
  const set = (key: TokenKey, value: string) => setQuery(setToken(query, key, value));
  const setRange = (after: string, before: string) => setQuery(setToken(setToken(query, "after", after), "before", before));
  const [custom, setCustom] = useState(false);
  const selected: Preset = custom ? "custom" : presetOf(p.after, p.before, new Date());
  const wide = useMediaQuery(md);
  const picker = useRef<PickerHandle | null>(null);
  const fieldId = (field: FilterField) => `${id}-${field}`;

  // The live state, read when the popover is asked to close: a calendar
  // destroyed with the form never reports its close.
  useEffect(() => {
    calendarOpen.current = () => !!picker.current?.calendar?.isOpen;
    return () => {
      calendarOpen.current = () => false;
    };
  }, [calendarOpen]);

  const request = focus?.n ?? 0;
  const field = focus?.field;
  const acknowledge = useRef(onFocused);
  acknowledge.current = onFocused;
  // After the commit, so the fields are mounted (no timers).
  useEffect(() => {
    if (!request || !field) return;
    document.getElementById(`${id}-${field}`)?.focus();
    acknowledge.current?.(request);
  }, [request, field, id]);

  const choose = (preset: Preset) => {
    if (preset === "custom") {
      setCustom(true);
      document.getElementById(fieldId("after"))?.focus();
      return;
    }
    setCustom(false);
    const r = presetRange(preset, new Date());
    setRange(r.after, r.before);
  };

  // One Escape closes an open calendar, the next one the popover: Carbon
  // only hides the calendar on Escape in its field and lets the key on to
  // the popover. (On a calendar day flatpickr handles Escape itself.)
  const onKeyDownCapture = (e: KeyboardEvent) => {
    const calendar = picker.current?.calendar;
    if (e.key === "Escape" && calendar?.isOpen) {
      e.preventDefault();
      calendar.close();
    }
  };

  const text = (key: "from" | "to", label: string) => (
    <TextInput id={fieldId(key)} size="sm" labelText={label} value={p[key]} onChange={(e) => set(key, e.target.value)} />
  );
  return (
    // A layer up from the list, so the fields contrast with the popover.
    <Layer role="group" aria-label={t.filters} className="search-filters" onKeyDownCapture={onKeyDownCapture}>
      {text("from", t.from)}
      {text("to", t.toOrCc)}
      <RadioButtonGroup
        legendText={t.dateRange}
        name={`${id}-preset`}
        orientation={wide ? "horizontal" : "vertical"}
        valueSelected={selected}
        onChange={(v) => choose(v as Preset)}
      >
        {presets.map((preset) => (
          <RadioButton key={preset} id={`${id}-preset-${preset}`} labelText={ft.presets[preset]} value={preset} />
        ))}
      </RadioButtonGroup>
      <DateRange
        ids={{ start: fieldId("after"), end: fieldId("before") }}
        labels={{ start: ft.since, end: ft.until }}
        size="sm"
        start={p.after}
        end={p.before ? shiftDay(p.before, -1) : ""}
        pickerRef={picker}
        onChange={(after, last) => {
          setCustom(true);
          setRange(after, last ? shiftDay(last, 1) : "");
        }}
      />
      <Checkbox id={fieldId("has")} labelText={t.hasAttachment} checked={p.hasAttachment} onChange={(_, { checked }) => set("has", checked ? "attachment" : "")} />
      <Checkbox id={fieldId("group")} labelText={t.groupByConversation} checked={group} onChange={(_, { checked }) => setGroup(checked)} />
    </Layer>
  );
}
