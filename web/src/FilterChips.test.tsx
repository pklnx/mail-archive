import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { FilterChips } from "./FilterChips";
import { applySettings } from "./i18n";

afterEach(() => {
  cleanup();
  applySettings({ language: "auto", locale: "auto" });
});

function setup(query: string, group = false) {
  applySettings({ language: "en", locale: "en-US" });
  const setQuery = vi.fn();
  const setGroup = vi.fn();
  const openFilters = vi.fn();
  render(<FilterChips query={query} setQuery={setQuery} group={group} setGroup={setGroup} openFilters={openFilters} />);
  return { setQuery, setGroup, openFilters };
}

describe("FilterChips", () => {
  it("switches yes/no filters in place", () => {
    const { setQuery, setGroup } = setup("invoice");
    fireEvent.click(screen.getByRole("button", { name: "Has attachment" }));
    expect(setQuery).toHaveBeenCalledWith("invoice has:attachment");
    fireEvent.click(screen.getByRole("button", { name: "Conversation" }));
    expect(setGroup).toHaveBeenCalledWith(true);
  });

  it("shows set values, opens the sheet and removes a filter", () => {
    const { setQuery, openFilters } = setup("from:sam invoice has:attachment", true);
    expect(screen.getByRole("button", { name: "Has attachment" }).getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByRole("button", { name: "Conversation" }).getAttribute("aria-pressed")).toBe("true");
    fireEvent.click(screen.getByRole("button", { name: "From: sam" }));
    expect(openFilters).toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Remove filter: From" }));
    expect(setQuery).toHaveBeenCalledWith("invoice has:attachment");
  });

  it("removes both ends of a date range at once", () => {
    const { setQuery } = setup("after:2026-01-01 before:2026-02-01 tax");
    expect(screen.getByRole("button", { name: /^Date: / })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Remove filter: Date" }));
    expect(setQuery).toHaveBeenCalledWith("tax");
  });
});
