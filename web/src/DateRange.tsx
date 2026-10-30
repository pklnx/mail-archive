import { useMemo, type MutableRefObject } from "react";
import { DatePicker, DatePickerInput } from "@carbon/react";
import { dateOfDay, dayOfDate } from "./format";
import { inputFormat } from "./datePresets";
import { filterText } from "./filterText";
import { lang, locale } from "./i18n";

/** Carbon's DatePicker ref: the flatpickr instance (DatePicker.js useImperativeHandle). */
export interface PickerHandle {
  calendar?: { isOpen: boolean; close: () => void; calendarContainer?: HTMLElement } | null;
}

/** A native pattern for the input format: Carbon's default only fits m/d/Y. */
const patternOf = (format: string) =>
  format.replace(/[^dmY]/g, (c) => `\\${c}`).replace(/[dm]/g, "\\d{1,2}").replace("Y", "\\d{4}");

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
 */
export function DateRange({ ids, labels, size = "md", start, end, onChange, pickerRef }: Props) {
  const ft = filterText();
  const { format, placeholder } = inputFormat(locale, ft.letters);
  const hint = ft.format(placeholder);
  // Local days, never strings (flatpickr would parse them with the input format).
  const value = useMemo(() => (start ? [dateOfDay(start), ...(end ? [dateOfDay(end)] : [])] : []), [start, end]);
  return (
    <DatePicker
      ref={pickerRef as never}
      datePickerType="range"
      dateFormat={format}
      locale={lang}
      value={value}
      nextMonthAriaLabel={ft.nextMonth}
      prevMonthAriaLabel={ft.previousMonth}
      onOpen={(_dates: Date[], _str: string, fp: { calendarContainer?: HTMLElement }) => fp.calendarContainer?.setAttribute("aria-label", ft.calendar)}
      onChange={(dates: Date[]) => {
        const [first, last] = dates;
        const s = first ? dayOfDate(first) : "";
        const e = last ? dayOfDate(last) : "";
        // Carbon clears the calendar itself when the caller lost its dates: no edit.
        if (s === start && e === end) return;
        onChange(s, e);
      }}
    >
      <DatePickerInput id={ids.start} size={size} labelText={labels.start} placeholder={placeholder} pattern={patternOf(format)} helperText={hint} />
      <DatePickerInput id={ids.end} size={size} labelText={labels.end} placeholder={placeholder} pattern={patternOf(format)} helperText={hint} />
    </DatePicker>
  );
}
