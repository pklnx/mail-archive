// The date tag shows the search's days as local days (#286). In its own
// file: a date formatter keeps the zone it was made in, so the zone is set
// before this file's first formatter is made.
import { cleanup, render, screen } from "@testing-library/react";
import { afterAll, beforeAll, expect, it, vi } from "vitest";
import { ActiveFilters } from "./SearchFilters";
import { applySettings } from "./i18n";

beforeAll(() => {
  vi.stubEnv("TZ", "America/Los_Angeles");
  applySettings({ language: "en", locale: "en-US" });
});
afterAll(() => {
  cleanup();
  vi.unstubAllEnvs();
  applySettings({ language: "en", locale: "en" });
});

it("shows after:2025-01-01 before:2025-02-01 as Jan 1 – Jan 31 west of UTC, not from Dec 31", () => {
  render(<ActiveFilters query="after:2025-01-01 before:2025-02-01" setQuery={() => {}} group={false} setGroup={() => {}} />);
  const tag = screen.getByRole("list", { name: "Active filters" }).querySelector(".cds--tag__label");
  expect(tag?.textContent).toBe("Date: Jan 1, 2025 – Jan 31, 2025");
});
