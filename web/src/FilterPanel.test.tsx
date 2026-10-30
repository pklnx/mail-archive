// The filter form in the filter popover (#286): its fields edit the prefix
// tokens of the search text as the inline panel did; the date range is a
// Carbon DatePicker with an inclusive end and presets. flatpickr runs in
// jsdom; the listener order of a real browser is covered by e2e/filters.spec.ts.
import { useRef, useState } from "react";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FilterPanel } from "./FilterPanel";
import type { FocusRequest } from "./SearchFilters";
import { filterTextFor } from "./filterText";
import { applySettings, messagesFor } from "./i18n";
import { shiftDay } from "./format";
import { parseQuery } from "./searchSyntax";
import { cases, fieldsOf, focusOf, initialRange, run, watch, type Range, type Step } from "./test/dateRangeCases";

const ft = filterTextFor("en");

beforeEach(() => {
  vi.stubEnv("TZ", "Europe/Berlin");
  applySettings({ language: "en", locale: "en-US" });
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllEnvs();
  applySettings({ language: "en", locale: "en" });
  document.querySelectorAll(".flatpickr-calendar").forEach((c) => c.remove());
});

interface Seen {
  queries: string[];
  groups: boolean[];
  calendarOpen?: { current: () => boolean };
}

/** The form with the search text and grouping as state, as App holds them. */
function Form({ start, group: startGroup = false, seen, focus, onFocused }: { start: string; group?: boolean; seen: Seen; focus?: FocusRequest; onFocused?: (n: number) => void }) {
  const [query, setQuery] = useState(start);
  const [group, setGroup] = useState(startGroup);
  const calendarOpen = useRef<() => boolean>(() => false);
  seen.calendarOpen = calendarOpen;
  return (
    <FilterPanel
      id="f"
      query={query}
      setQuery={(q) => {
        seen.queries.push(q);
        setQuery(q);
      }}
      group={group}
      setGroup={(on) => {
        seen.groups.push(on);
        setGroup(on);
      }}
      focus={focus}
      onFocused={onFocused}
      calendarOpen={calendarOpen}
    />
  );
}

function setup(start: string, group = false) {
  const seen: Seen = { queries: [], groups: [] };
  const view = render(<Form start={start} group={group} seen={seen} />);
  return { seen, view, last: () => seen.queries.at(-1) };
}

const since = () => screen.getByLabelText(ft.since) as HTMLInputElement;
const until = () => screen.getByLabelText(ft.until) as HTMLInputElement;
const radio = (name: string) => screen.getByRole("radio", { name }) as HTMLInputElement;
/** The flatpickr instance on the Since field (Carbon's range picker has one for both fields). */
const fp = () => (document.getElementById("f-after") as unknown as { _flatpickr: { setDate: (d: Date[], change: boolean) => void; clear: () => void; isOpen: boolean; open: () => void; calendarContainer: HTMLElement } })._flatpickr;

describe("FilterPanel fields", () => {
  it("switches yes/no filters in place: the attachment in the text, the grouping through setGroup", () => {
    const { seen, last } = setup("invoice");
    fireEvent.click(screen.getByRole("checkbox", { name: "Has attachment" }));
    expect(last()).toBe("invoice has:attachment");
    fireEvent.click(screen.getByRole("checkbox", { name: "Has attachment" }));
    expect(last()).toBe("invoice");
    fireEvent.click(screen.getByRole("checkbox", { name: "Group by conversation" }));
    expect(seen.groups).toEqual([true]);
  });

  it("shows the prefixes of the text in its fields and sets, changes and clears them", () => {
    const { last } = setup("from:sam invoice has:attachment", true);
    expect((screen.getByRole("textbox", { name: "From" }) as HTMLInputElement).value).toBe("sam");
    expect((screen.getByRole("checkbox", { name: "Has attachment" }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByRole("checkbox", { name: "Group by conversation" }) as HTMLInputElement).checked).toBe(true);
    fireEvent.change(screen.getByRole("textbox", { name: "To or Cc" }), { target: { value: "office" } });
    expect(last()).toBe("from:sam invoice has:attachment to:office");
    fireEvent.change(screen.getByRole("textbox", { name: "From" }), { target: { value: "anna" } });
    expect(last()).toBe("from:anna invoice has:attachment to:office");
    fireEvent.change(screen.getByRole("textbox", { name: "From" }), { target: { value: "" } });
    expect(last()).toBe("invoice has:attachment to:office");
  });
});

describe("FilterPanel date range", () => {
  it("shows after: and the day before before: as local days, in the user's format", () => {
    // West of UTC a Date of "YYYY-MM-DD" would be the day before.
    vi.stubEnv("TZ", "America/Los_Angeles");
    setup("after:2026-10-01 before:2026-10-11 tax");
    expect(since().value).toBe("10/01/2026");
    expect(until().value).toBe("10/10/2026");
    cleanup();
    applySettings({ language: "de", locale: "de" });
    render(<Form start="after:2026-10-01 before:2026-10-11" seen={{ queries: [], groups: [] }} />);
    expect((screen.getByLabelText("Ab") as HTMLInputElement).value).toBe("01.10.2026");
    expect((screen.getByLabelText("Bis") as HTMLInputElement).value).toBe("10.10.2026");
  });

  it("writes the picked start as after: and the inclusive end + 1 day as before:, also across the DST end", () => {
    const { last } = setup("tax");
    act(() => fp().setDate([new Date(2026, 9, 20), new Date(2026, 9, 25)], true));
    expect(last()).toBe("tax after:2026-10-20 before:2026-10-26");
    act(() => fp().setDate([new Date(2026, 9, 3), new Date(2026, 9, 3)], true));
    expect(last()).toBe("tax after:2026-10-03 before:2026-10-04");
    // Cleared: both go.
    act(() => fp().clear());
    expect(last()).toBe("tax");
    cleanup();
    // Only a start picked so far: an open end.
    const open = setup("tax");
    act(() => fp().setDate([new Date(2026, 9, 3)], true));
    expect(open.last()).toBe("tax after:2026-10-03");
  });

  it("shows the format in the placeholder and the helper text, per locale", () => {
    for (const [lang, locale, placeholder, helper] of [
      ["en", "en-US", "mm/dd/yyyy", "Format: mm/dd/yyyy"],
      ["en", "en-GB", "dd/mm/yyyy", "Format: dd/mm/yyyy"],
      ["de", "de", "TT.MM.JJJJ", "Format: TT.MM.JJJJ"],
    ] as const) {
      applySettings({ language: lang, locale });
      const t = filterTextFor(lang);
      render(<Form start="" seen={{ queries: [], groups: [] }} />);
      for (const label of [t.since, t.until]) {
        const input = screen.getByLabelText(label) as HTMLInputElement;
        expect(input.placeholder, locale).toBe(placeholder);
        const described = document.getElementById(input.getAttribute("aria-describedby") ?? "");
        expect(described?.textContent, locale).toBe(helper);
      }
      cleanup();
    }
  });

  it("reports the calendar's live state, and none once the form is gone", () => {
    const { seen, view } = setup("");
    expect(seen.calendarOpen!.current()).toBe(false);
    act(() => fp().open());
    expect(seen.calendarOpen!.current()).toBe(true);
    // Unmounted with the calendar open: flatpickr's destroy reports no close.
    view.unmount();
    expect(seen.calendarOpen!.current()).toBe(false);
  });

  it("closes an open calendar on the first Escape in its field and keeps that Escape from the popover", () => {
    setup("");
    act(() => fp().open());
    const outer = vi.fn((e: KeyboardEvent) => e.defaultPrevented);
    window.addEventListener("keydown", outer);
    try {
      fireEvent.keyDown(since(), { key: "Escape" });
      expect(fp().isOpen).toBe(false);
      expect(outer.mock.results[0]?.value).toBe(true);
      // The second one is not taken: the popover closes on it.
      fireEvent.keyDown(since(), { key: "Escape" });
      expect(outer.mock.results[1]?.value).toBe(false);
    } finally {
      window.removeEventListener("keydown", outer);
    }
  });
});

// The shared case table of DateRange (#385), in German, with each field
// left as a browser leaves it (test/dateRangeCases.ts): the fields show
// exactly the after: and before: of the search, before: the day after the
// end.
describe("FilterPanel date range: fields and search agree", () => {
  let watching: ReturnType<typeof watch>;
  beforeEach(() => {
    applySettings({ language: "de", locale: "de" });
    watching = watch();
  });
  afterEach(() => watching.stop());

  const dt = filterTextFor("de");
  const ab = () => screen.getByLabelText(dt.since) as HTMLInputElement;
  const bis = () => screen.getByLabelText(dt.until) as HTMLInputElement;
  const other = () => screen.getByRole("textbox", { name: messagesFor("de").from });
  const picker = { from: ab, to: bis, other };
  /** The search text of a range, before: the day after its end. */
  const queryOf = (r: Range) => `tax after:${r.start}${r.end ? ` before:${shiftDay(r.end, 1)}` : ""}`;
  const rangeOf = (q: string): Range => {
    const p = parseQuery(q);
    return { start: p.after, end: p.before ? shiftDay(p.before, -1) : "" };
  };

  it.each(cases)("$name", (c) => {
    const start = queryOf(initialRange[c.initial]);
    const { seen } = setup(start);
    run(picker, c.steps);
    expect(seen.queries, "setQuery calls").toHaveLength(c.edits);
    const applied = rangeOf(seen.queries.at(-1) ?? start);
    if (c.range) expect(applied).toEqual(c.range);
    expect([ab().value, bis().value]).toEqual(fieldsOf(applied));
    expect(document.activeElement).toBe(focusOf(picker, c.focus));
    expect(watching.errors).toEqual([]);
  });

  it("P a range, then a preset with a start only: Bis is emptied", () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date(2026, 9, 28, 12));
    const { last } = setup(queryOf(initialRange.both));
    fireEvent.click(radio(dt.presets.last7));
    expect(last()).toBe("tax after:2026-10-22");
    expect([ab().value, bis().value]).toEqual(["22.10.2026", ""]);
  });

  // An end alone (typed in the search) has no start to show: the fields stay
  // empty and keep it, whatever is pressed (maintainer decision 1a).
  it.each([
    ["Ab focused, Enter", [["focus", "from"], ["enter", "from"]]],
    ["Ab focused, press outside", [["focus", "from"], ["leave", "outside"]]],
    ["Bis focused, press outside", [["focus", "to"], ["leave", "outside"]]],
    ["Bis focused, Enter", [["focus", "to"], ["enter", "to"]]],
  ] as [string, Step[]][])("Q before: alone, %s: kept, nothing sent", (_, steps) => {
    const { seen } = setup("tax before:2026-10-11");
    run(picker, steps);
    expect(seen.queries).toEqual([]);
    expect([ab().value, bis().value]).toEqual(["", ""]);
    expect(watching.errors).toEqual([]);
  });

  it("R the search text changes from a range to before: alone while the form is open: kept, nothing sent", () => {
    const calls: string[] = [];
    let edit!: (q: string) => void;
    function Search() {
      const [query, setQuery] = useState(queryOf(initialRange.both));
      edit = setQuery;
      const calendarOpen = useRef<() => boolean>(() => false);
      return <FilterPanel id="f" query={query} setQuery={(q) => { calls.push(q); setQuery(q); }} group={false} setGroup={() => {}} calendarOpen={calendarOpen} />;
    }
    render(<Search />);
    act(() => edit("tax before:2026-10-11"));
    expect(calls).toEqual([]);
    expect([ab().value, bis().value]).toEqual(["", ""]);
  });
});

describe("FilterPanel presets", () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date(2026, 9, 28, 12));
  });

  it("fills after: only, and Any time removes both dates", () => {
    const { last } = setup("tax before:2026-01-01");
    fireEvent.click(radio(ft.presets.last7));
    expect(last()).toBe("tax after:2026-10-22");
    expect(radio(ft.presets.last7).checked).toBe(true);
    fireEvent.click(radio(ft.presets.last30));
    expect(last()).toBe("tax after:2026-09-29");
    fireEvent.click(radio(ft.presets.year));
    expect(last()).toBe("tax after:2026-01-01");
    expect(since().value).toBe("01/01/2026");
    fireEvent.click(radio(ft.presets.any));
    expect(last()).toBe("tax");
    expect(radio(ft.presets.any).checked).toBe(true);
  });

  it("selects the preset of the search, Any time without dates and Custom for other dates", () => {
    setup("after:2026-10-22");
    expect(radio(ft.presets.last7).checked).toBe(true);
    cleanup();
    setup("invoice");
    expect(radio(ft.presets.any).checked).toBe(true);
    for (const q of ["before:2026-10-01", "after:2026-10-21", "after:2025-01-01", "after:2026-10-22 before:2026-10-29"]) {
      cleanup();
      setup(q);
      expect(radio(ft.presets.custom).checked, q).toBe(true);
    }
  });

  it("Custom keeps the dates and focuses Since", () => {
    const { seen } = setup("after:2026-10-22");
    fireEvent.click(radio(ft.presets.custom));
    expect(seen.queries).toEqual([]);
    expect(radio(ft.presets.custom).checked).toBe(true);
    expect(document.activeElement).toBe(since());
  });

  it("selects Custom after a hand edit of the range, also one that a preset would give", () => {
    const { last } = setup("");
    expect(radio(ft.presets.any).checked).toBe(true);
    // Exactly Last 7 days, but picked by hand.
    act(() => fp().setDate([new Date(2026, 9, 22)], true));
    expect(last()).toBe("after:2026-10-22");
    expect(radio(ft.presets.custom).checked).toBe(true);
    // A preset afterwards is selected again.
    fireEvent.click(radio(ft.presets.year));
    expect(radio(ft.presets.year).checked).toBe(true);
  });
});

describe("FilterPanel with a focus request", () => {
  const panel = (focus: FocusRequest | undefined, onFocused: (n: number) => void) => (
    <Form start="from:sam" seen={{ queries: [], groups: [] }} focus={focus} onFocused={onFocused} />
  );

  it.each([
    ["from", "From"],
    ["to", "To or Cc"],
    ["after", ft.since],
    ["before", ft.until],
    ["has", "Has attachment"],
    ["group", "Group by conversation"],
  ] as const)("focuses %s when it mounts and acknowledges once", (field, label) => {
    const onFocused = vi.fn();
    render(panel({ field, n: 1 }, onFocused));
    expect(document.activeElement).toBe(screen.getByLabelText(label));
    expect(onFocused.mock.calls).toEqual([[1]]);
  });

  it("takes no focus without a request", () => {
    const onFocused = vi.fn();
    render(panel(undefined, onFocused));
    expect(document.activeElement).toBe(document.body);
    expect(onFocused).not.toHaveBeenCalled();
  });

  it("focuses again on a new request while mounted, also for the same field", () => {
    const onFocused = vi.fn();
    const { rerender } = render(panel({ field: "from", n: 1 }, onFocused));
    const from = screen.getByRole("textbox", { name: "From" });
    from.blur();
    rerender(panel({ field: "from", n: 2 }, onFocused));
    expect(document.activeElement).toBe(from);
    rerender(panel({ field: "to", n: 3 }, onFocused));
    expect(document.activeElement).toBe(screen.getByRole("textbox", { name: "To or Cc" }));
    expect(onFocused.mock.calls).toEqual([[1], [2], [3]]);
  });
});

describe("FilterPanel in German", () => {
  it("speaks German, the presets and the calendar's labels included", () => {
    applySettings({ language: "de", locale: "de" });
    const de = messagesFor("de");
    const dt = filterTextFor("de");
    setup("");
    expect(screen.getByRole("group", { name: de.filters })).toBeTruthy();
    for (const name of Object.values(dt.presets)) expect(radio(name)).toBeTruthy();
    expect(screen.getByRole("group", { name: de.dateRange })).toBeTruthy();
    act(() => fp().open());
    const calendar = fp().calendarContainer;
    expect(calendar.getAttribute("aria-label")).toBe(dt.calendar);
    expect(calendar.querySelector(`[aria-label="${dt.nextMonth}"]`)).not.toBeNull();
    expect(calendar.querySelector(`[aria-label="${dt.previousMonth}"]`)).not.toBeNull();
    // flatpickr's German month names.
    expect(calendar.textContent).toContain("Oktober");
    const html = document.body.innerHTML;
    for (const s of ["Any time", "Last 7 days", "Custom", "Since", "Until", "Format: mm", "Next month", "Previous month", "calendar-container"]) expect(html, s).not.toContain(s);
  });
});
