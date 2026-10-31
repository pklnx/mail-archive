import { useEffect, useEffectEvent, useLayoutEffect, useMemo, useRef, type MutableRefObject } from "react";
import { DatePicker, DatePickerInput } from "@carbon/react";
import { dateOfDay, dayOfDate } from "./format";
import { inputFormat } from "./datePresets";
import { filterText } from "./filterText";
import { lang, locale } from "./i18n";

/** Carbon's DatePicker ref: the flatpickr instance (DatePicker.js useImperativeHandle). */
export interface PickerHandle {
  calendar?: Calendar | null;
}

/** The parts of the flatpickr instance DateRange uses. */
interface Calendar {
  isOpen: boolean;
  close: () => void;
  calendarContainer?: HTMLElement;
  selectedDates: Date[];
  config: { errorHandler: (e: Error) => void };
  parseDate: (text: string, format: string) => Date | undefined;
  formatDate: (date: Date, format: string) => string;
  setDate: (dates: Date[], change: boolean) => void;
  clear: () => void;
  set: (options: object) => void;
}

/** A native pattern for the input format: Carbon's default only fits m/d/Y. */
const patternOf = (format: string) =>
  format.replace(/[^dmY]/g, (c) => `\\${c}`).replace(/[dm]/g, "\\d{1,2}").replace("Y", "\\d{4}");

/** Inclusive ISO days, "" for none. */
interface Range {
  start: string;
  end: string;
}
const none: Range = { start: "", end: "" };
/** The range as the fields show it: no start, no dates. */
const shown = (r: Range) => (r.start ? r : none);
const same = (a: Range, b: Range) => a.start === b.start && a.end === b.end;
// Local days, never strings (flatpickr would parse them with the input format).
const datesOf = (r: Range) => (r.start ? [dateOfDay(r.start), ...(r.end ? [dateOfDay(r.end)] : [])] : []);

interface Props {
  ids: { start: string; end: string };
  labels: { start: string; end: string };
  size?: "sm" | "md";
  /** The first day, "YYYY-MM-DD" or "". */
  start: string;
  /** The last day, inclusive, "YYYY-MM-DD" or "". */
  end: string;
  /** Called only on a real change, with inclusive ISO days. */
  onChange: (start: string, end: string) => void;
  pickerRef?: MutableRefObject<PickerHandle | null>;
}

/**
 * A date range in one Carbon DatePicker with an inclusive end, in the
 * user's date format (shown under the fields), shared by the search's
 * filter form and the Log page (#377), so the two cannot drift apart. Each
 * caller maps the inclusive ISO days to its own model. Both callers are
 * lazy, so this stays out of the initial bundle.
 *
 * Typed text is committed by one rule (#385), on Enter in a field, when
 * focus leaves a field for anything but the calendar, on a press outside
 * the picker and its calendar, and when the calendar closes: both fields
 * are read as one range. An empty start means no dates, a start alone an
 * open end, both a range (sorted); a field that holds no date puts the
 * committed range back. The fields then show exactly what is applied.
 * Carbon and flatpickr each commit one field on these events, so their
 * listeners are pre-empted in the capture phase.
 */
export function DateRange({ ids, labels, size = "md", start, end, onChange, pickerRef }: Props) {
  const ft = filterText();
  const { format, placeholder } = inputFormat(locale, ft.letters);
  const hint = ft.format(placeholder);
  const value = useMemo(() => datesOf({ start, end }), [start, end]);
  const own = useRef<PickerHandle | null>(null);
  const handle = pickerRef ?? own;
  // What was applied last. Updated at once on a change: the props stay stale
  // until the caller renders again.
  const committed = useRef<Range>({ start, end });
  const fields = () => [document.getElementById(ids.start), document.getElementById(ids.end)] as (HTMLInputElement | null)[];

  /** Fields and calendar show a range, without a change event. */
  const show = (r: Range) => {
    const fp = handle.current?.calendar;
    const [from, to] = fields();
    if (!fp || !from || !to) return;
    const dates = datesOf(r);
    if (fp.selectedDates.map(dayOfDate).join() !== dates.map(dayOfDate).join()) {
      // clear() also makes the range plugin forget the old end; its change
      // event is no edit (emit).
      if (!dates.length) fp.clear();
      else fp.setDate(dates, false);
      // A whole range: the value hooks without a change event, so the range
      // plugin remembers it (a later calendar click keeps its end).
      if (dates.length === 2) fp.set({});
    }
    // setDate writes only the start field for one date.
    from.value = r.start ? fp.formatDate(dateOfDay(r.start), format) : "";
    to.value = r.start && r.end ? fp.formatDate(dateOfDay(r.end), format) : "";
  };

  /** Applies a range unless the fields showed it already. */
  const emit = (r: Range) => {
    if (same(shown(r), shown(committed.current))) return;
    committed.current = r;
    onChange(r.start, r.end);
  };

  /** The range in the fields' text, or null when a field holds no date. */
  const read = (): Range | null => {
    const fp = handle.current?.calendar;
    const [from, to] = fields();
    if (!fp || !from || !to) return null;
    const day = (text: string) => {
      if (!text.trim()) return "";
      // Muted as Carbon's handleInput does: unreadable text is no error here.
      const errorHandler = fp.config.errorHandler;
      fp.config.errorHandler = () => {};
      try {
        const d = fp.parseDate(text.trim(), format);
        return d && !Number.isNaN(d.getTime()) ? dayOfDate(d) : null;
      } finally {
        fp.config.errorHandler = errorHandler;
      }
    };
    const s = day(from.value);
    const e = day(to.value);
    if (s === null || e === null) return null;
    if (!s) return none;
    if (!e || s <= e) return { start: s, end: e };
    return { start: e, end: s };
  };

  /** Applies the fields' range and shows what is applied. */
  const settle = () => {
    const r = read() ?? committed.current;
    emit(r);
    show(shown(r));
  };

  // Before Carbon's passive value effect, whose clear() fires a change event.
  useLayoutEffect(() => {
    committed.current = { start, end };
  }, [start, end]);
  // After Carbon's value effect (a child's effects run first), which leaves
  // the end field as it was for a start alone or no start.
  const showProps = useEffectEvent(() => show({ start, end }));
  useEffect(() => showProps(), [start, end]);

  const onKeyDown = useEffectEvent((e: KeyboardEvent) => {
    if (!isField(e.target)) return;
    // Shift+Tab out of the start field: before Carbon closes the calendar and
    // puts an emptied start back.
    if (e.key === "Tab" && e.shiftKey && e.target === fields()[0]) return settle();
    if (e.key !== "Enter") return;
    // Enter that confirms an IME composition is the IME's: no commit, also
    // not Carbon's or flatpickr's.
    if (e.isComposing || e.keyCode === 229) return e.stopPropagation();
    // Before Carbon's, flatpickr's and the range plugin's Enter, which commit
    // one field each (and throw on an emptied one); focus stays in the field.
    e.preventDefault();
    e.stopPropagation();
    settle();
    handle.current?.calendar?.close();
  });
  const onBlur = useEffectEvent((e: FocusEvent) => {
    if (!isField(e.target)) return;
    // Instead of flatpickr's and Carbon's blur, which read one field each.
    e.stopPropagation();
    // Into the calendar: still editing (a commit there would redraw it under
    // the focused day, and the range plugin would refocus the end field).
    if (inCalendar(e.relatedTarget)) return;
    settle();
    if (!inPicker(e.relatedTarget)) handle.current?.calendar?.close();
  });
  // Escape in a field closes the calendar (Carbon, flatpickr, FilterPanel's
  // capture), and Carbon then puts an emptied start back from an older
  // change: settle first, on the document, before React's capture listeners.
  const onEscape = useEffectEvent((e: KeyboardEvent) => {
    if (e.key === "Escape" && isField(e.target)) settle();
  });
  // flatpickr's documentClick (document mousedown or touchstart, bubble
  // phase) rewrites the start field before the blur: settle and close first.
  // A touch that starts a scroll settles too.
  const onPress = useEffectEvent((e: Event) => {
    const fp = handle.current?.calendar;
    if (!fp?.isOpen || inPicker(e.target) || inCalendar(e.target)) return;
    settle();
    fp.close();
  });
  const isField = (t: EventTarget | null) => !!t && fields().includes(t as HTMLInputElement);
  const inPicker = (t: EventTarget | null) => t instanceof Node && !!document.getElementById(ids.start)?.closest(".cds--date-picker")?.contains(t);
  const inCalendar = (t: EventTarget | null) => t instanceof Node && !!handle.current?.calendar?.calendarContainer?.contains(t);

  useEffect(() => {
    const picker = document.getElementById(ids.start)?.closest(".cds--date-picker");
    if (!picker) return;
    const key = (e: Event) => onKeyDown(e as KeyboardEvent);
    const blur = (e: Event) => onBlur(e as FocusEvent);
    const press = (e: Event) => onPress(e);
    const escape = (e: Event) => onEscape(e as KeyboardEvent);
    const passive = { capture: true, passive: true };
    picker.addEventListener("keydown", key, true);
    picker.addEventListener("blur", blur, true);
    document.addEventListener("mousedown", press, true);
    document.addEventListener("touchstart", press, passive);
    document.addEventListener("keydown", escape, true);
    return () => {
      document.removeEventListener("keydown", escape, true);
      picker.removeEventListener("keydown", key, true);
      picker.removeEventListener("blur", blur, true);
      document.removeEventListener("mousedown", press, true);
      document.removeEventListener("touchstart", press, passive);
    };
  }, [ids.start]);

  return (
    <DatePicker
      ref={handle as never}
      datePickerType="range"
      dateFormat={format}
      locale={lang}
      value={value}
      nextMonthAriaLabel={ft.nextMonth}
      prevMonthAriaLabel={ft.previousMonth}
      onOpen={(_dates: Date[], _str: string, fp: { calendarContainer?: HTMLElement }) => fp.calendarContainer?.setAttribute("aria-label", ft.calendar)}
      // Tab into the calendar, then Escape or out of it: settled here.
      onClose={() => settle()}
      onChange={(dates: Date[]) => {
        // Calendar clicks. Carbon clears the calendar itself when the caller
        // lost its dates, and show() clears it: no edit then (emit).
        const [first, last] = dates;
        emit({ start: first ? dayOfDate(first) : "", end: last ? dayOfDate(last) : "" });
      }}
    >
      <DatePickerInput id={ids.start} size={size} labelText={labels.start} placeholder={placeholder} pattern={patternOf(format)} helperText={hint} />
      <DatePickerInput id={ids.end} size={size} labelText={labels.end} placeholder={placeholder} pattern={patternOf(format)} helperText={hint} />
    </DatePicker>
  );
}
