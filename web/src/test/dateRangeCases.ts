// The cases of the shared DateRange (#385): typing in Von/Bis and leaving
// or committing them as a browser does. Both callers run this table
// (LogPage.test.tsx, FilterPanel.test.tsx), in German; each row checks the
// applied range, that the fields show exactly that range, the number of
// edits, where focus is, that the calendar is closed, and that nothing
// throws.
import { act, fireEvent } from "@testing-library/react";

/** Inclusive ISO days, "" for none. */
export interface Range {
  start: string;
  end: string;
}

export type Field = "from" | "to";

/** A user action. */
export type Step =
  | ["focus", Field]
  | ["type", Field, string]
  | ["enter", Field]
  | ["tab", Field]
  | ["shiftTab", Field]
  | ["escape"]
  /** A press on the page (outside the picker) or on another field. */
  | ["leave", "outside" | "other"]
  /** A touch outside that starts a scroll: no blur, no click. */
  | ["touch"]
  /** Focus moves to another field without a key or a press (a script). */
  | ["focusOther"];

export interface Case {
  name: string;
  /** "start": Von 28.09.2026 alone (the Log's default); "both": 01.10.2026 – 20.10.2026. */
  initial: "start" | "both";
  steps: Step[];
  /** The applied range afterwards; left out where only agreement is pinned. */
  range?: Range;
  /** New edits: requests on the Log, setQuery calls in the search. */
  edits: number;
  focus: Field | "other" | "body";
}

export interface Picker {
  from: () => HTMLInputElement;
  to: () => HTMLInputElement;
  /** A field outside the picker. */
  other: () => HTMLElement;
}

/** The picker's calendar is shown (Escape in a field only hides it: Carbon). */
export const calendarOpen = (p: Picker) =>
  !!(p.from() as unknown as { _flatpickr?: { calendarContainer: HTMLElement } })._flatpickr?.calendarContainer.classList.contains("open");

const start: Range = { start: "2026-09-28", end: "" };
const both: Range = { start: "2026-10-01", end: "2026-10-20" };
const none: Range = { start: "", end: "" };
export const initialRange = { start, both };

export const cases: Case[] = [
  { name: "N both set, Von focused, press outside: nothing changes", initial: "both", steps: [["focus", "from"], ["leave", "outside"]], range: both, edits: 0, focus: "body" },
  { name: "N2 both set, Bis focused, press outside: nothing changes", initial: "both", steps: [["focus", "to"], ["leave", "outside"]], range: both, edits: 0, focus: "body" },
  { name: "N3 Von only, Von focused, press outside: nothing changes", initial: "start", steps: [["focus", "from"], ["leave", "outside"]], range: start, edits: 0, focus: "body" },
  { name: "A Von only, Von emptied, Enter: no dates, focus stays", initial: "start", steps: [["type", "from", ""], ["enter", "from"]], range: none, edits: 1, focus: "from" },
  { name: "A2 Von only, Von emptied, press outside: no dates", initial: "start", steps: [["type", "from", ""], ["leave", "outside"]], range: none, edits: 1, focus: "body" },
  {
    name: "B both set, Von emptied and left, then only Bis typed and left: no dates",
    initial: "both",
    steps: [["type", "from", ""], ["leave", "outside"], ["type", "to", "05.10.2026"], ["leave", "outside"]],
    range: none,
    edits: 1,
    focus: "body",
  },
  {
    name: "B2 as B, Bis left with Tab, then a press outside",
    initial: "both",
    steps: [["type", "from", ""], ["leave", "outside"], ["type", "to", "05.10.2026"], ["tab", "to"], ["leave", "outside"]],
    range: none,
    edits: 1,
    focus: "body",
  },
  { name: "C both set, Bis emptied, press outside: open end", initial: "both", steps: [["type", "to", ""], ["leave", "outside"]], range: { start: "2026-10-01", end: "" }, edits: 1, focus: "body" },
  { name: "C1 both set, Bis emptied, press on another field: open end", initial: "both", steps: [["type", "to", ""], ["leave", "other"]], range: { start: "2026-10-01", end: "" }, edits: 1, focus: "other" },
  { name: "C2 both set, Bis emptied, Enter: open end", initial: "both", steps: [["type", "to", ""], ["enter", "to"]], range: { start: "2026-10-01", end: "" }, edits: 1, focus: "to" },
  { name: "C3 both set, Bis emptied, Tab, press outside: open end", initial: "both", steps: [["type", "to", ""], ["tab", "to"], ["leave", "outside"]], range: { start: "2026-10-01", end: "" }, edits: 1, focus: "body" },
  { name: "D both set, Von emptied, press outside: no dates", initial: "both", steps: [["type", "from", ""], ["leave", "outside"]], range: none, edits: 1, focus: "body" },
  { name: "D1 both set, Von emptied, press on another field: no dates", initial: "both", steps: [["type", "from", ""], ["leave", "other"]], range: none, edits: 1, focus: "other" },
  { name: "E both set, Von emptied, Enter: no dates", initial: "both", steps: [["type", "from", ""], ["enter", "from"]], range: none, edits: 1, focus: "from" },
  { name: "E2 both set, Von emptied, Tab, press outside: no dates", initial: "both", steps: [["type", "from", ""], ["tab", "from"], ["leave", "outside"]], range: none, edits: 1, focus: "body" },
  { name: "E3 both set, Von emptied, Tab, Escape: no dates", initial: "both", steps: [["type", "from", ""], ["tab", "from"], ["escape"]], range: none, edits: 1, focus: "from" },
  { name: "S both set, Von emptied, Shift+Tab: no dates", initial: "both", steps: [["type", "from", ""], ["shiftTab", "from"]], range: none, edits: 1, focus: "body" },
  { name: "F Von only, Bis typed, Enter: the range", initial: "start", steps: [["type", "to", "05.10.2026"], ["enter", "to"]], range: { start: "2026-09-28", end: "2026-10-05" }, edits: 1, focus: "to" },
  { name: "F2 Von only, Bis typed, press outside: the range", initial: "start", steps: [["type", "to", "05.10.2026"], ["leave", "outside"]], range: { start: "2026-09-28", end: "2026-10-05" }, edits: 1, focus: "body" },
  {
    name: "G Von emptied and left, then Bis typed, Enter: no dates",
    initial: "start",
    steps: [["type", "from", ""], ["leave", "outside"], ["type", "to", "05.10.2026"], ["enter", "to"]],
    range: none,
    edits: 1,
    focus: "to",
  },
  { name: "H both emptied (Bis first), Enter in Von: no dates, no error", initial: "both", steps: [["type", "to", ""], ["type", "from", ""], ["enter", "from"]], range: none, edits: 2, focus: "from" },
  { name: "H2 both emptied (Von first), Enter in Bis: no dates, no error", initial: "both", steps: [["type", "from", ""], ["type", "to", ""], ["enter", "to"]], range: none, edits: 1, focus: "to" },
  { name: "H3 both emptied, press outside: no dates", initial: "both", steps: [["type", "to", ""], ["type", "from", ""], ["leave", "outside"]], range: none, edits: 2, focus: "body" },
  // flatpickr rolls an impossible day over; strict parsing is out of scope.
  { name: "I Von 32.13.2026, Enter: flatpickr's day", initial: "start", steps: [["type", "from", "32.13.2026"], ["enter", "from"]], range: { start: "2027-01-01", end: "" }, edits: 1, focus: "from" },
  { name: "I2 both set, Bis unreadable, Enter: both kept, Bis put back", initial: "both", steps: [["type", "to", "abc"], ["enter", "to"]], range: both, edits: 0, focus: "to" },
  { name: "I3 both set, Von unreadable, press outside: both kept", initial: "both", steps: [["type", "from", "abc"], ["leave", "outside"]], range: both, edits: 0, focus: "body" },
  { name: "I4 both set, Bis unreadable, press outside: both kept", initial: "both", steps: [["type", "to", "abc"], ["leave", "outside"]], range: both, edits: 0, focus: "body" },
  { name: "J both set, Bis before Von, Enter: sorted", initial: "both", steps: [["type", "to", "01.09.2026"], ["enter", "to"]], range: { start: "2026-09-01", end: "2026-10-01" }, edits: 1, focus: "to" },
  { name: "K both set, Von changed, press outside: the new start", initial: "both", steps: [["type", "from", "05.10.2026"], ["leave", "outside"]], range: { start: "2026-10-05", end: "2026-10-20" }, edits: 1, focus: "body" },
  // Focus must not jump back to Bis (the range plugin's refocus).
  { name: "K2 both set, Bis changed, press outside: the new end", initial: "both", steps: [["type", "to", "25.10.2026"], ["leave", "outside"]], range: { start: "2026-10-01", end: "2026-10-25" }, edits: 1, focus: "body" },
  { name: "K3 Von only, Bis typed, Tab, press outside: the range", initial: "start", steps: [["type", "to", "05.10.2026"], ["tab", "to"], ["leave", "outside"]], range: { start: "2026-09-28", end: "2026-10-05" }, edits: 1, focus: "body" },
  { name: "K4 both set, Von changed, Enter: the new start", initial: "both", steps: [["type", "from", "05.10.2026"], ["enter", "from"]], range: { start: "2026-10-05", end: "2026-10-20" }, edits: 1, focus: "from" },
  { name: "Es both set, Von emptied, Escape: no dates", initial: "both", steps: [["type", "from", ""], ["escape"]], range: none, edits: 1, focus: "from" },
  // Carbon remembers the start of the last change event and puts it into an
  // emptied start when the calendar closes.
  {
    name: "Es2 both set, Von changed and left, then emptied, Escape: no dates",
    initial: "both",
    steps: [["type", "from", "03.10.2026"], ["leave", "outside"], ["type", "from", ""], ["escape"]],
    range: none,
    edits: 2,
    focus: "from",
  },
  { name: "Es3 both set, Bis emptied, Escape: open end", initial: "both", steps: [["type", "to", ""], ["escape"]], range: { start: "2026-10-01", end: "" }, edits: 1, focus: "to" },
  // The calendar closes too, before flatpickr's focus listener reads Von alone.
  { name: "K5 both set, Von changed, focus moved by a script: the new start", initial: "both", steps: [["type", "from", "05.10.2026"], ["focusOther"]], range: { start: "2026-10-05", end: "2026-10-20" }, edits: 1, focus: "other" },
  // A touch that starts a scroll settles like any press outside; flatpickr
  // reads the half-typed day leniently (out of scope), so only agreement and
  // one edit are pinned.
  { name: "T both set, Bis half typed, a touch outside starts a scroll: fields and range agree", initial: "both", steps: [["type", "to", "25.1"], ["touch"]], edits: 1, focus: "to" },
];

/** A day in the German format. */
export const german = (day: string) => (day ? day.split("-").reverse().join(".") : "");

/** What the fields show for a range: no start, no dates. */
export const fieldsOf = (r: Range) => (r.start ? [german(r.start), german(r.end)] : ["", ""]);

/**
 * Collects uncaught errors (flatpickr throws from its listeners) and each
 * field's value at focus, which decides whether leaving it sends change, as
 * a browser does. Call stop() after the test.
 */
export function watch() {
  const errors: unknown[] = [];
  const onError = (e: ErrorEvent) => {
    errors.push(e.error ?? e.message);
    e.preventDefault();
  };
  const onFocus = (e: FocusEvent) => {
    if (e.target instanceof HTMLInputElement) atFocus.set(e.target, e.target.value);
  };
  window.addEventListener("error", onError);
  document.addEventListener("focusin", onFocus, true);
  return {
    errors,
    stop: () => {
      window.removeEventListener("error", onError);
      document.removeEventListener("focusin", onFocus, true);
    },
  };
}

const atFocus = new WeakMap<HTMLInputElement, string>();

/** A browser sends change when a field loses focus with another value than at focus. */
function changeIfEdited(el: Element | null) {
  if (!(el instanceof HTMLInputElement) || !atFocus.has(el) || atFocus.get(el) === el.value) return;
  atFocus.set(el, el.value);
  fireEvent.change(el);
}

/**
 * A press on target as a browser delivers it: pointer down, change on the
 * edited field, focus moves (to target if it takes focus, else nowhere),
 * pointer up, click. Both touchstart and mousedown are sent: jsdom defines
 * window.ontouchstart, so flatpickr listens to touchstart there, while
 * desktop Chromium uses mousedown (e2e/log.spec.ts).
 */
export function leave(target: HTMLElement) {
  const from = document.activeElement;
  fireEvent.touchStart(target);
  fireEvent.mouseDown(target);
  if (from !== target) changeIfEdited(from);
  act(() => {
    if (target !== document.body && target.tabIndex >= 0) target.focus();
    else if (from instanceof HTMLElement) from.blur();
  });
  fireEvent.mouseUp(target);
  fireEvent.click(target);
}

/** Runs the steps on a picker. Typing is an input event; Enter carries keyCode 13, as in browsers. */
export function run(p: Picker, steps: Step[]) {
  const el = (f: Field) => (f === "from" ? p.from() : p.to());
  for (const step of steps) {
    switch (step[0]) {
      case "focus":
        leave(el(step[1]));
        break;
      case "type": {
        const input = el(step[1]);
        if (document.activeElement !== input) leave(input);
        fireEvent.input(input, { target: { value: step[2] } });
        break;
      }
      case "enter":
        fireEvent.keyDown(el(step[1]), { key: "Enter", code: "Enter", keyCode: 13 });
        break;
      case "tab": {
        // Carbon moves focus itself on Tab; the browser then sends change.
        const input = el(step[1]);
        fireEvent.keyDown(input, { key: "Tab", code: "Tab", keyCode: 9 });
        if (document.activeElement !== input) changeIfEdited(input);
        break;
      }
      case "shiftTab": {
        const input = el(step[1]);
        fireEvent.keyDown(input, { key: "Tab", code: "Tab", keyCode: 9, shiftKey: true });
        changeIfEdited(input);
        act(() => input.blur());
        break;
      }
      case "escape":
        fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape", code: "Escape", keyCode: 27 });
        break;
      case "leave":
        leave(step[1] === "outside" ? document.body : p.other());
        break;
      case "touch":
        fireEvent.touchStart(document.body);
        break;
      case "focusOther":
        act(() => p.other().focus());
        break;
    }
  }
}

/** The element a row's focus names. */
export const focusOf = (p: Picker, f: Case["focus"]) => (f === "from" ? p.from() : f === "to" ? p.to() : f === "other" ? p.other() : document.body);
