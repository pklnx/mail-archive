// The filter panel and the set filters as Carbon DismissibleTags (they
// replace the hand-written FilterChips of the phones). Both edit the
// search text; the grouping lives in the URL. On phones the yes/no
// filters are quick filters (Carbon SelectableTags) instead of tags.
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { ActiveFilters, QuickFilters, SearchFilters, editTarget } from "./SearchFilters";
import { applySettings, messagesFor } from "./i18n";

afterEach(() => {
  cleanup();
  applySettings({ language: "en", locale: "en" });
});

function setup(query: string, group = false, Component = ActiveFilters) {
  applySettings({ language: "en", locale: "en-US" });
  const setQuery = vi.fn();
  const setGroup = vi.fn();
  render(<Component query={query} setQuery={setQuery} group={group} setGroup={setGroup} />);
  return { setQuery, setGroup };
}

// The visible text of each tag (its dismiss tooltip aside).
const tags = () =>
  within(screen.getByRole("list", { name: "Active filters" }))
    .getAllByRole("listitem")
    .map((li) => li.querySelector(".cds--tag__label")?.textContent);

describe("ActiveFilters", () => {
  it("shows nothing while no filter is set", () => {
    setup("invoice");
    expect(screen.queryByRole("list", { name: "Active filters" })).toBeNull();
  });

  it("shows each set filter with its value and removes one with its dismiss button", () => {
    const { setQuery, setGroup } = setup("from:sam invoice has:attachment", true);
    expect(tags()).toEqual(["Has attachment", "From: sam", "Group by conversation"]);
    fireEvent.click(screen.getByRole("button", { name: "Remove filter: From" }));
    expect(setQuery).toHaveBeenCalledWith("invoice has:attachment");
    fireEvent.click(screen.getByRole("button", { name: "Remove filter: Has attachment" }));
    expect(setQuery).toHaveBeenLastCalledWith("from:sam invoice");
    fireEvent.click(screen.getByRole("button", { name: "Remove filter: Conversation" }));
    expect(setGroup).toHaveBeenCalledWith(false);
  });

  it("removes both ends of a date range at once", () => {
    const { setQuery } = setup("after:2026-01-01 before:2026-02-01 tax to:office");
    expect(tags()[0]).toMatch(/^Date: .+ – .+$/);
    expect(tags()[1]).toBe("To or Cc: office");
    fireEvent.click(screen.getByRole("button", { name: "Remove filter: Date" }));
    expect(setQuery).toHaveBeenCalledWith("tax to:office");
  });

  it("speaks German, Carbon's dismiss texts included", () => {
    applySettings({ language: "de", locale: "de" });
    const de = messagesFor("de");
    render(<ActiveFilters query="from:sam has:attachment" setQuery={() => {}} group setGroup={() => {}} />);
    expect(screen.getByRole("list", { name: de.activeFilters })).toBeTruthy();
    expect(screen.getByRole("button", { name: de.removeFilter(de.from) })).toBeTruthy();
    const html = document.body.innerHTML;
    for (const s of ["Dismiss", "Remove filter", "Has attachment", "Active filters"]) expect(html, s).not.toContain(s);
  });
});

describe("SearchFilters", () => {
  it("switches yes/no filters in place: the attachment in the text, the grouping through setGroup", () => {
    const { setQuery, setGroup } = setup("invoice", false, SearchFilters);
    fireEvent.click(screen.getByRole("checkbox", { name: "Has attachment" }));
    expect(setQuery).toHaveBeenCalledWith("invoice has:attachment");
    fireEvent.click(screen.getByRole("checkbox", { name: "Group by conversation" }));
    expect(setGroup).toHaveBeenCalledWith(true);
  });

  it("shows the prefixes of the text in its fields and edits them", () => {
    const { setQuery } = setup("from:sam after:2026-01-01 invoice has:attachment", true, SearchFilters);
    expect((screen.getByRole("textbox", { name: "From" }) as HTMLInputElement).value).toBe("sam");
    expect((screen.getByLabelText("Since") as HTMLInputElement).value).toBe("2026-01-01");
    expect((screen.getByRole("checkbox", { name: "Has attachment" }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByRole("checkbox", { name: "Group by conversation" }) as HTMLInputElement).checked).toBe(true);
    fireEvent.change(screen.getByRole("textbox", { name: "To or Cc" }), { target: { value: "office" } });
    expect(setQuery).toHaveBeenCalledWith("from:sam after:2026-01-01 invoice has:attachment to:office");
  });
});

describe("QuickFilters", () => {
  const quick = (name: string) => within(screen.getByRole("group", { name: "Quick filters" })).getByRole("button", { name });

  it("shows the yes/no filters as pressed or not, from the text and the grouping", () => {
    setup("from:sam has:attachment", false, QuickFilters);
    expect(quick("Has attachment").getAttribute("aria-pressed")).toBe("true");
    expect(quick("Group by conversation").getAttribute("aria-pressed")).toBe("false");
    cleanup();
    setup("invoice", true, QuickFilters);
    expect(quick("Has attachment").getAttribute("aria-pressed")).toBe("false");
    expect(quick("Group by conversation").getAttribute("aria-pressed")).toBe("true");
  });

  it("switches the attachment in the text and keeps the other tokens", () => {
    const on = setup("from:sam invoice", false, QuickFilters);
    fireEvent.click(quick("Has attachment"));
    expect(on.setQuery).toHaveBeenCalledWith("from:sam invoice has:attachment");
    cleanup();
    const off = setup("from:sam has:attachment invoice", false, QuickFilters);
    fireEvent.click(quick("Has attachment"));
    expect(off.setQuery).toHaveBeenCalledWith("from:sam invoice");
  });

  it("switches the grouping through setGroup (Carbon's onChange passes the new state)", () => {
    const on = setup("", false, QuickFilters);
    fireEvent.click(quick("Group by conversation"));
    expect(on.setGroup).toHaveBeenCalledWith(true);
    cleanup();
    const off = setup("", true, QuickFilters);
    fireEvent.click(quick("Group by conversation"));
    expect(off.setGroup).toHaveBeenCalledWith(false);
  });

  it("speaks German", () => {
    applySettings({ language: "de", locale: "de" });
    const de = messagesFor("de");
    render(<QuickFilters query="" setQuery={() => {}} group={false} setGroup={() => {}} />);
    const group = screen.getByRole("group", { name: de.quickFilters });
    expect(within(group).getByRole("button", { name: de.hasAttachment })).toBeTruthy();
    expect(within(group).getByRole("button", { name: de.groupByConversation })).toBeTruthy();
    const html = document.body.innerHTML;
    for (const s of ["Quick filters", "Has attachment", "Group by conversation"]) expect(html, s).not.toContain(s);
  });
});

describe("ActiveFilters on phones and the Edit filters button", () => {
  const edit = () => screen.queryByRole("button", { name: "Edit filters" });
  const shown = () => tags().filter(Boolean);

  it("leaves out the attachment and the grouping with quick, and shows no row when only they are set", () => {
    applySettings({ language: "en", locale: "en-US" });
    render(<ActiveFilters query="from:sam has:attachment" setQuery={() => {}} group setGroup={() => {}} quick onEdit={() => {}} />);
    expect(shown()).toEqual(["From: sam"]);
    expect(edit()).not.toBeNull();
    cleanup();
    render(<ActiveFilters query="has:attachment invoice" setQuery={() => {}} group setGroup={() => {}} quick onEdit={() => {}} />);
    expect(screen.queryByRole("list", { name: "Active filters" })).toBeNull();
    expect(edit()).toBeNull();
  });

  it("shows Edit filters after the tags, only with a tag, and calls onEdit", () => {
    applySettings({ language: "en", locale: "en-US" });
    const onEdit = vi.fn();
    render(<ActiveFilters query="invoice" setQuery={() => {}} group={false} setGroup={() => {}} onEdit={onEdit} />);
    expect(edit()).toBeNull();
    cleanup();
    render(<ActiveFilters query="to:office" setQuery={() => {}} group={false} setGroup={() => {}} onEdit={onEdit} />);
    const items = within(screen.getByRole("list", { name: "Active filters" })).getAllByRole("listitem");
    expect(items).toHaveLength(2);
    expect(items[1]!.contains(edit())).toBe(true);
    fireEvent.click(edit()!);
    expect(onEdit).toHaveBeenCalledTimes(1);
  });

  it("speaks German, the button and the dismiss names included", () => {
    applySettings({ language: "de", locale: "de" });
    const de = messagesFor("de");
    render(<ActiveFilters query="from:sam" setQuery={() => {}} group={false} setGroup={() => {}} onEdit={() => {}} />);
    expect(screen.getByRole("button", { name: de.editFilters })).toBeTruthy();
    expect(screen.getByRole("button", { name: de.removeFilter(de.from) })).toBeTruthy();
    const html = document.body.innerHTML;
    for (const s of ["Edit filters", "Remove filter"]) expect(html, s).not.toContain(s);
  });
});

describe("editTarget: the first visible tag's field, in the panel's order", () => {
  it.each([
    ["from:sam before:2026-02-01", false, false, "from"],
    ["after:2026-01-01 to:office", false, false, "to"],
    ["after:2026-01-01 before:2026-02-01 has:attachment", true, false, "after"],
    ["before:2026-02-01", false, false, "before"],
    ["has:attachment", true, false, "has"],
    ["invoice", true, false, "group"],
    ["invoice", false, false, ""],
    // Phones: the attachment and the grouping have no tag.
    ["has:attachment before:2026-02-01", true, true, "before"],
    ["has:attachment", true, true, ""],
  ] as const)("%s, group %s, quick %s: %s", (query, group, quick, field) => {
    expect(editTarget(query, group, quick)).toBe(field);
  });
});

describe("SearchFilters with a focus request", () => {
  type Field = "from" | "to" | "after" | "before" | "has" | "group";
  const panel = (focus: { field: Field; n: number } | undefined, onFocused: (n: number) => void) => (
    <SearchFilters query="from:sam" setQuery={() => {}} group={false} setGroup={() => {}} focus={focus} onFocused={onFocused} />
  );

  it.each([
    ["from", "From"],
    ["to", "To or Cc"],
    ["after", "Since"],
    ["before", "Before"],
    ["has", "Has attachment"],
    ["group", "Group by conversation"],
  ] as const)("focuses %s when it mounts and acknowledges once", (field, label) => {
    applySettings({ language: "en", locale: "en-US" });
    const onFocused = vi.fn();
    render(panel({ field, n: 1 }, onFocused));
    expect(document.activeElement).toBe(screen.getByLabelText(label));
    expect(onFocused.mock.calls).toEqual([[1]]);
  });

  it("takes no focus without a request", () => {
    applySettings({ language: "en", locale: "en-US" });
    const onFocused = vi.fn();
    render(panel(undefined, onFocused));
    expect(document.activeElement).toBe(document.body);
    expect(onFocused).not.toHaveBeenCalled();
  });

  it("focuses again on a new request while mounted, also for the same field", () => {
    applySettings({ language: "en", locale: "en-US" });
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
