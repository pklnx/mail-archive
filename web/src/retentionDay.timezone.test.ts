// The last day of a retention period east of the rule's time zone (#406).
// In its own file: a date formatter keeps the zone it was made in, so the
// zone is set before this file's first formatter is made.
import { afterAll, beforeAll, expect, it, vi } from "vitest";
import { retentionDay } from "./format";
import { applySettings } from "./i18n";
import { periodEnds } from "./test/retentionDayCases";

beforeAll(() => {
  vi.stubEnv("TZ", "Asia/Tokyo");
  applySettings({ language: "en", locale: "en" });
});
afterAll(() => {
  vi.unstubAllEnvs();
  applySettings({ language: "en", locale: "en" });
});

it.each(periodEnds)("shows Dec 31, 2025 in Tokyo for a period ending 1 January 2026 in %s", (_zone, end) => {
  expect(retentionDay(end)).toBe("Dec 31, 2025");
});

it("shows nothing for an unreadable end", () => {
  expect(retentionDay("nonsense")).toBe("");
});
