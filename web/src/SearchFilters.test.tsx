// The filter panel and the set filters as Carbon DismissibleTags (they
// replace the hand-written FilterChips of the phones). Both edit the
// search text; the grouping lives in the URL.
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { ActiveFilters, SearchFilters } from "./SearchFilters";
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
