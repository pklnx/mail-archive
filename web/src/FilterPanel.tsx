import { useEffect, useMemo, useRef, useState, type KeyboardEvent, type MutableRefObject } from "react";
import { Checkbox, DatePicker, DatePickerInput, Layer, RadioButton, RadioButtonGroup, TextInput } from "@carbon/react";
import { dateOfDay, dayOfDate, shiftDay } from "./format";
import { inputFormat, presetOf, presetRange, presets, type Preset } from "./datePresets";
import { filterText } from "./filterText";
import { lang, locale, t } from "./i18n";
import { parseQuery, setToken, type TokenKey } from "./searchSyntax";
import { useMediaQuery } from "./breakpoints";
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

/** Carbon's DatePicker ref: the flatpickr instance (DatePicker.js useImperativeHandle). */
interface PickerHandle {
  calendar?: { isOpen: boolean; close: () => void; calendarContainer?: HTMLElement } | null;
}

/** A native pattern for the input format: Carbon's default only fits m/d/Y. */
const patternOf = (format: string) =>
  format.replace(/[^dmY]/g, (c) => `\\${c}`).replace(/[dm]/g, "\\d{1,2}").replace("Y", "\\d{4}");

/**
 * The filter form in the filter button's popover (#286), a lazy chunk of its
 * own: it is mounted only while the popover is open. The fields edit the
 * prefix tokens in the search text, so both stay in sync; attachment names
 * (attachment:) are typed only.
 *
 * The date range is one Carbon DatePicker with an inclusive end (Since /
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
  const wide = useMediaQuery("(min-width: 42rem)");
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

  const { format, placeholder } = inputFormat(locale, ft.letters);
  const hint = ft.format(placeholder);
  // Local days, never strings (flatpickr would parse them with the input format).
  const value = useMemo(
    () => (p.after ? [dateOfDay(p.after), ...(p.before ? [dateOfDay(shiftDay(p.before, -1))] : [])] : []),
    [p.after, p.before],
  );

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
      <DatePicker
        ref={picker as never}
        datePickerType="range"
        dateFormat={format}
        locale={lang}
        value={value}
        nextMonthAriaLabel={ft.nextMonth}
        prevMonthAriaLabel={ft.previousMonth}
        onOpen={(_dates: Date[], _str: string, fp: { calendarContainer?: HTMLElement }) => fp.calendarContainer?.setAttribute("aria-label", ft.calendar)}
        onChange={(dates: Date[]) => {
          const [start, end] = dates;
          const after = start ? dayOfDate(start) : "";
          const before = end ? shiftDay(dayOfDate(end), 1) : "";
          // Carbon clears the calendar itself when the search lost its dates (Any time): no edit.
          if (after === p.after && before === p.before) return;
          setCustom(true);
          setRange(after, before);
        }}
      >
        <DatePickerInput id={fieldId("after")} size="sm" labelText={ft.since} placeholder={placeholder} pattern={patternOf(format)} helperText={hint} />
        <DatePickerInput id={fieldId("before")} size="sm" labelText={ft.until} placeholder={placeholder} pattern={patternOf(format)} helperText={hint} />
      </DatePicker>
      <Checkbox id={fieldId("has")} labelText={t.hasAttachment} checked={p.hasAttachment} onChange={(_, { checked }) => set("has", checked ? "attachment" : "")} />
      <Checkbox id={fieldId("group")} labelText={t.groupByConversation} checked={group} onChange={(_, { checked }) => setGroup(checked)} />
    </Layer>
  );
}
