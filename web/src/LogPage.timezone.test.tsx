// The Log's From/To count local days in every hour (#402). In its own file,
// like SearchFilters.timezone.test.tsx: the zone is set before this file's
// first date formatter is made. The clock is 2026-10-30T23:03Z, 00:03 on
// 31 October in Berlin, the hour of the red CI run: the server read a plain
// since=2026-10-31 as UTC midnight, after its default until, and answered 400.
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterAll, beforeAll, expect, it, vi } from "vitest";
import { applySettings, messagesFor } from "./i18n";
import { LogPage } from "./LogPage";

let urls: string[] = [];

beforeAll(() => {
  // The page keeps its filters in the address, under view=log (#376).
  window.history.replaceState(null, "", "/?view=log");
  vi.stubEnv("TZ", "Europe/Berlin");
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(new Date("2026-10-30T23:03:00Z"));
  applySettings({ language: "de", locale: "de" });
  vi.spyOn(globalThis, "fetch").mockImplementation(async (url) => {
    urls.push(String(url));
    return new Response(JSON.stringify({ entries: [] }), { status: 200, headers: { "Content-Type": "application/json" } });
  });
});
afterAll(() => {
  cleanup();
  window.history.replaceState(null, "", "/");
  vi.restoreAllMocks();
  vi.useRealTimers();
  vi.unstubAllEnvs();
  applySettings({ language: "en", locale: "en" });
  document.querySelectorAll(".flatpickr-calendar").forEach((c) => c.remove());
});

type Flatpickr = { setDate: (d: Date[], change: boolean) => void };

it("sends Berlin's days as instants of local midnight just after local midnight", async () => {
  const de = messagesFor("de");
  render(<LogPage admin={false} open={() => {}} />);
  const from = () => screen.getByLabelText(de.logFrom) as HTMLInputElement;
  const audit = () => urls.filter((u) => u.startsWith("/api/audit"));
  // The default From: 30 local days before 31 October.
  await waitFor(() => expect(audit()).toEqual(["/api/audit?since=2026-09-30T22%3A00%3A00.000Z"]));
  expect(from().value).toBe("01.10.2026");

  const fp = (from() as unknown as { _flatpickr: Flatpickr })._flatpickr;
  // Today picked alone: since is today's local midnight, before the server's default until.
  act(() => fp.setDate([new Date(2026, 9, 31)], true));
  await waitFor(() => expect(audit().at(-1)).toBe("/api/audit?since=2026-10-30T23%3A00%3A00.000Z"));
  // Today picked twice: until is the start of the next local day.
  act(() => fp.setDate([new Date(2026, 9, 31), new Date(2026, 9, 31)], true));
  await waitFor(() => expect(audit().at(-1)).toBe("/api/audit?since=2026-10-30T23%3A00%3A00.000Z&until=2026-10-31T23%3A00%3A00.000Z"));
});
