// The Log page on Carbon (#168 part 5): the filters build the same query
// as before, the table, folded rows, the message link and Load more. From/To
// is the search filters' Carbon date range (#377); flatpickr runs in jsdom,
// a real browser is covered by e2e/log.spec.ts.
import { useRef, useState } from "react";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { auditQuery, type AuditEntry } from "./api";
import { FilterPanel } from "./FilterPanel";
import { filterTextFor } from "./filterText";
import { applySettings, messagesFor, t } from "./i18n";
import { defaultFilter, LogPage } from "./LogPage";
import { expectLoaded, expectLoading } from "./test/loading";
import { calendarOpen, cases, fieldsOf, focusOf, run, watch, type Range } from "./test/dateRangeCases";

const sha = "a".repeat(64);
let n = 0;
function entry(action: string, at: string, more: Partial<AuditEntry> = {}): AuditEntry {
  return { id: ++n, at: `2026-10-11T10:${at}Z`, actor: "session", user: "alice", action, target: null, details: {}, ...more };
}

// Newest first, as the API sends them.
const page1 = [
  entry("message.open", "00:09", { target: { type: "message", id: sha, subject: "Invoice 42" }, clientIp: "192.0.2.1", userAgent: "Mozilla/5.0 (X11; Linux x86_64) Chrome/140.0" }),
  entry("message.search", "00:03", { details: { q: "invoice" }, apiKey: "backup", actor: "api_key" }),
  entry("message.search", "00:02", { details: { q: "invoi" }, apiKey: "backup", actor: "api_key" }),
  entry("message.search", "00:00", { details: { q: "inv" }, apiKey: "backup", actor: "api_key" }),
  entry("account.add", "00:00", { actor: "cli", user: undefined, target: { type: "account", id: "3", name: "work" } }),
  entry("auth.login_failed", "00:00", { details: { method: "password", reason: "wrong_password" } }),
];
const page2 = [entry("message.download", "00:00", { user: "bob", target: { type: "message", id: "b".repeat(64) } })];

let urls: string[];

beforeEach(() => {
  urls = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (url) => {
    const u = String(url);
    urls.push(u);
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { "Content-Type": "application/json" } });
    if (u === "/api/users") return json({ users: [{ name: "alice" }, { name: "bob" }] });
    if (u.includes("cursor=next")) return json({ entries: page2 });
    return json({ entries: page1, nextCursor: "next" });
  });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.useRealTimers();
  vi.unstubAllEnvs();
  applySettings({ language: "en", locale: "en" });
  document.querySelectorAll(".flatpickr-calendar").forEach((c) => c.remove());
});

const audit = () => urls.filter((u) => u.startsWith("/api/audit"));
/** The request URL of a filter. */
const query = (f: Partial<ReturnType<typeof defaultFilter>>) => `/api/audit?${auditQuery({ since: "", until: "", group: "", who: "", ...f }, "")}`;

type Flatpickr = { setDate: (d: Date[], change: boolean) => void; clear: () => void; open: () => void; calendarContainer: HTMLElement };
const field = (label: string) => screen.getByLabelText(label) as HTMLInputElement;
/** The flatpickr instance on the From field (Carbon's range picker has one for both fields). */
const fpOf = (label: string) => (field(label) as unknown as { _flatpickr: Flatpickr })._flatpickr;

// flatpickr's range plugin empties To 10 ms after a change that leaves no
// dates (rangePlugin.js onChange), so field checks wait for the fields.

/** Types into a field and commits it the way a user does: Enter (with keyCode, as browsers send it), or leaving the field. */
function type(input: HTMLInputElement, value: string, commit: "enter" | "blur") {
  act(() => input.focus());
  fireEvent.change(input, { target: { value } });
  if (commit === "enter") fireEvent.keyDown(input, { key: "Enter", keyCode: 13 });
  else act(() => input.blur());
}

describe("LogPage", () => {
  it("shows a table skeleton with the log's columns while the first page loads (#287)", async () => {
    let release!: () => void;
    const released = new Promise<void>((r) => {
      release = r;
    });
    const answer = vi.mocked(globalThis.fetch).getMockImplementation()!;
    vi.mocked(globalThis.fetch).mockImplementation(async (url, init) => {
      if (String(url).startsWith("/api/audit")) await released;
      return answer(url, init);
    });
    render(<LogPage admin open={() => {}} />);
    const table = expectLoading()!.querySelector(".cds--data-table.cds--skeleton")!;
    expect([...table.querySelectorAll("th")].map((th) => th.textContent)).toEqual([t.logTime, t.logAction, t.logWho, t.logTarget, t.logClient]);
    expect(table.className).toContain("cds--data-table--md");
    expect(screen.queryByText(t.logEmpty)).toBeNull();
    await act(async () => release());
    await screen.findByText("Invoice 42");
    expectLoaded();
  });

  it("builds the same query from the filters as before", async () => {
    render(<LogPage admin open={() => {}} />);
    await screen.findByText("Invoice 42");
    const base = defaultFilter();
    expect(audit()).toEqual([`/api/audit?${auditQuery(base, "")}`]);

    // The dates through the picker, as a user picks them (#377).
    act(() => fpOf(t.logFrom).setDate([new Date(2026, 9, 1)], true));
    await waitFor(() => expect(audit().at(-1)).toBe("/api/audit?since=2026-10-01"));
    act(() => fpOf(t.logFrom).setDate([new Date(2026, 9, 1), new Date(2026, 9, 31)], true));
    await waitFor(() => expect(audit().at(-1)).toBe("/api/audit?since=2026-10-01&until=2026-10-31"));
    fireEvent.change(screen.getByLabelText(t.logAction), { target: { value: "read" } });
    await waitFor(() => expect(audit().at(-1)).toBe("/api/audit?since=2026-10-01&until=2026-10-31&action=read"));

    const who = screen.getByLabelText(t.logWho) as HTMLSelectElement;
    await waitFor(() => expect([...who.options].map((o) => o.value)).toEqual(["", "cli", "api_key", "alice", "bob"]));
    fireEvent.change(who, { target: { value: "bob" } });
    await waitFor(() => expect(audit().at(-1)).toBe("/api/audit?since=2026-10-01&until=2026-10-31&action=read&user=bob"));
    fireEvent.change(who, { target: { value: "cli" } });
    await waitFor(() => expect(audit().at(-1)).toBe("/api/audit?since=2026-10-01&until=2026-10-31&action=read&actor=cli"));
    fireEvent.change(who, { target: { value: "api_key" } });
    await waitFor(() => expect(audit().at(-1)).toBe("/api/audit?since=2026-10-01&until=2026-10-31&action=read&actor=api_key"));
    fireEvent.change(screen.getByLabelText(t.logAction), { target: { value: "" } });
    await waitFor(() => expect(audit().at(-1)).toBe("/api/audit?since=2026-10-01&until=2026-10-31&actor=api_key"));
  });

  it("offers every action group, and the Who filter only to admins", async () => {
    render(<LogPage admin={false} open={() => {}} />);
    await screen.findByText("Invoice 42");
    const groups = [...(screen.getByLabelText(t.logAction) as HTMLSelectElement).options].map((o) => o.value);
    expect(groups).toEqual(["", "login", "search", "read", "export", "accounts", "users", "profile", "api_keys", "sync", "retention"]);
    expect(screen.queryByLabelText(t.logWho)).toBeNull();
    expect(urls).not.toContain("/api/users");
  });

  it("shows time, action, who, target, detail and client in a table", async () => {
    render(<LogPage admin open={() => {}} />);
    await screen.findByText("Invoice 42");
    const rows = screen.getAllByRole("row");
    expect(within(rows[0]!).getAllByRole("columnheader").map((h) => h.textContent)).toEqual([t.logTime, t.logAction, t.logWho, t.logTarget, t.logClient]);
    // The typed search is one row with its prefixes folded in.
    expect(rows).toHaveLength(1 + 4);
    const open = within(rows[1]!);
    expect(open.getByText(t.auditAction("message.open"))).toBeTruthy();
    expect(open.getByText("alice")).toBeTruthy();
    expect(open.getByText("192.0.2.1 · Chrome 140 · Linux")).toBeTruthy();
    const search = within(rows[2]!);
    expect(search.getByText("“invoice”")).toBeTruthy();
    expect(search.getByText(t.logViaKey("backup"))).toBeTruthy();
    // For an admin the command line is in the Who column, not in the detail.
    const cli = within(rows[3]!).getByText(t.logCLI);
    expect(cli.tagName).toBe("TD");
    expect(within(rows[3]!).getAllByRole("cell")[2]).toBe(cli);
    expect(within(rows[3]!).getByText("work")).toBeTruthy();
    const failed = within(rows[4]!).getByText(t.auditAction("auth.login_failed"));
    expect(failed.className).toContain("app-text-error");
    expect(within(rows[4]!).getByText("password, wrong_password")).toBeTruthy();
  });

  it("shows the Who column only to admins, and the command line in the detail for the others (#310)", async () => {
    render(<LogPage admin={false} open={() => {}} />);
    await screen.findByText("Invoice 42");
    const rows = screen.getAllByRole("row");
    expect(within(rows[0]!).getAllByRole("columnheader").map((h) => h.textContent)).toEqual([t.logTime, t.logAction, t.logTarget, t.logClient]);
    for (const r of rows.slice(1)) expect(within(r).getAllByRole("cell")).toHaveLength(4);
    // The CLI entry: "command line" in its target cell, as a detail line.
    const cli = within(rows[3]!).getByText(t.logCLI);
    expect(cli.className).toContain("log-table__detail");
    expect(cli.closest("td")).toBe(within(rows[3]!).getByText("work").closest("td"));
    // A session entry gets no such line.
    expect(within(rows[1]!).queryByText(t.logCLI)).toBeNull();
  });

  it("shows the table skeleton without the Who column to a non-admin", () => {
    vi.mocked(globalThis.fetch).mockImplementation(() => new Promise<Response>(() => {}));
    render(<LogPage admin={false} open={() => {}} />);
    const table = expectLoading()!.querySelector(".cds--data-table.cds--skeleton")!;
    expect([...table.querySelectorAll("th")].map((th) => th.textContent)).toEqual([t.logTime, t.logAction, t.logTarget, t.logClient]);
  });

  it("unfolds and folds the repeats", async () => {
    render(<LogPage admin open={() => {}} />);
    await screen.findByText("Invoice 42");
    const more = screen.getByRole("button", { name: t.logFolded(2) });
    expect(more.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(more);
    expect(screen.getByText("“invoi”")).toBeTruthy();
    expect(screen.getByText("“inv”")).toBeTruthy();
    expect(screen.getAllByRole("row")).toHaveLength(1 + 4 + 2);
    const hide = screen.getByRole("button", { name: t.logHideFolded });
    expect(hide.getAttribute("aria-expanded")).toBe("true");
    for (const id of hide.getAttribute("aria-controls")!.split(" ")) expect(document.getElementById(id)?.tagName).toBe("TR");
    fireEvent.click(hide);
    expect(screen.queryByText("“inv”")).toBeNull();
  });

  it("opens the message of a target, and leaves a modified click to the browser", async () => {
    const open = vi.fn();
    render(<LogPage admin open={open} />);
    const link = (await screen.findByText("Invoice 42")).closest("a")!;
    expect(link.getAttribute("href")).toBe(`/?m=${sha}`);
    fireEvent.click(link, { ctrlKey: true });
    expect(open).not.toHaveBeenCalled();
    fireEvent.click(link);
    expect(open).toHaveBeenCalledWith(sha);
  });

  it("loads more with the cursor and the same filter", async () => {
    render(<LogPage admin open={() => {}} />);
    await screen.findByText("Invoice 42");
    fireEvent.click(screen.getByRole("button", { name: t.logMore }));
    await screen.findByText(t.auditAction("message.download"));
    expect(audit().at(-1)).toBe(`/api/audit?${auditQuery(defaultFilter(), "next")}`);
    expect(screen.getByText("bbbbbbbbbbbb…")).toBeTruthy();
    expect(screen.queryByRole("button", { name: t.logMore })).toBeNull();
  });
});

describe("German", () => {
  it("shows the filters and the table in German", async () => {
    applySettings({ language: "de", locale: "de" });
    try {
      const de = messagesFor("de");
      render(<LogPage admin open={() => {}} />);
      await screen.findByText("Invoice 42");
      expect(screen.getByRole("heading", { level: 1, name: de.logTitle })).toBeTruthy();
      expect(screen.getByLabelText(de.logFrom)).toBeTruthy();
      for (const label of [de.logFrom, de.logTo]) {
        const input = screen.getByLabelText(label) as HTMLInputElement;
        expect(input.placeholder, label).toBe("TT.MM.JJJJ");
        expect(document.getElementById(input.getAttribute("aria-describedby") ?? "")?.textContent, label).toBe("Format: TT.MM.JJJJ");
      }
      expect(screen.getByRole("columnheader", { name: de.logClient })).toBeTruthy();
      expect(screen.getByRole("button", { name: de.logFolded(2) })).toBeTruthy();
      expect(screen.getByRole("button", { name: de.logMore })).toBeTruthy();
      const text = document.body.textContent ?? "";
      for (const e of [/\bLoading\b/, /Load more/, /\bFrom\b/, /\bAction\b/, /\bClient\b/, /\bmore\b/]) expect(text, String(e)).not.toMatch(e);
    } finally {
      applySettings({ language: "en", locale: "en" });
    }
  });
});

// From/To is the search filters' date range (#377): a text field in the
// user's format with Carbon's calendar, an inclusive To sent unchanged
// (the server takes a plain until as inclusive). The clock is frozen at noon
// UTC, so the default since (a UTC day, defaultFilter) is the same day in
// every time zone used here.
describe("LogPage date range", () => {
  const now = new Date(Date.UTC(2026, 9, 28, 12));
  beforeEach(() => {
    vi.stubEnv("TZ", "Europe/Berlin");
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(now);
  });

  /** The page in German, loaded. */
  async function german() {
    applySettings({ language: "de", locale: "de" });
    const de = messagesFor("de");
    render(<LogPage admin open={() => {}} />);
    await screen.findByText("Invoice 42");
    return { from: () => field(de.logFrom), to: () => field(de.logTo), fp: () => fpOf(de.logFrom), de };
  }

  it("shows From and To as text fields in the user's format, with the hint and the default since", async () => {
    // The day the default since is, computed as defaultFilter does (UTC).
    const since = defaultFilter(now).since;
    expect(since).toBe("2026-09-28");
    for (const [lang, locale, placeholder, hint, shown] of [
      ["en", "en-US", "mm/dd/yyyy", "Format: mm/dd/yyyy", "09/28/2026"],
      ["de", "de", "TT.MM.JJJJ", "Format: TT.MM.JJJJ", "28.09.2026"],
    ] as const) {
      applySettings({ language: lang, locale });
      const m = messagesFor(lang);
      render(<LogPage admin={false} open={() => {}} />);
      await screen.findByText("Invoice 42");
      for (const label of [m.logFrom, m.logTo]) {
        const input = field(label);
        expect(input.type, `${locale} ${label}`).toBe("text");
        expect(input.placeholder, `${locale} ${label}`).toBe(placeholder);
        expect(document.getElementById(input.getAttribute("aria-describedby") ?? "")?.textContent, `${locale} ${label}`).toBe(hint);
      }
      expect(field(m.logFrom).value, locale).toBe(shown);
      expect(field(m.logTo).value, locale).toBe("");
      cleanup();
    }
  });

  it("sends typed German dates as ISO days, To unchanged (inclusive)", async () => {
    const { from, to } = await german();
    type(from(), "01.10.2026", "enter");
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: "2026-10-01" })));
    type(to(), "31.10.2026", "enter");
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: "2026-10-01", until: "2026-10-31" })));
    await waitFor(() => expect([from().value, to().value]).toEqual(["01.10.2026", "31.10.2026"]));
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("sends a picked range, a single day and an open end as local days, also west of UTC", async () => {
    for (const tz of ["Europe/Berlin", "America/Los_Angeles"]) {
      vi.stubEnv("TZ", tz);
      const { from, to, fp } = await german();
      act(() => fp().setDate([new Date(2026, 9, 20), new Date(2026, 9, 25)], true));
      await waitFor(() => expect(audit().at(-1), tz).toBe(query({ since: "2026-10-20", until: "2026-10-25" })));
      await waitFor(() => expect([from().value, to().value], tz).toEqual(["20.10.2026", "25.10.2026"]));
      // One day picked twice: since = until.
      act(() => fp().setDate([new Date(2026, 9, 3), new Date(2026, 9, 3)], true));
      await waitFor(() => expect(audit().at(-1), tz).toBe(query({ since: "2026-10-03", until: "2026-10-03" })));
      cleanup();
      urls = [];
      // Only a start picked so far: since only.
      const open = await german();
      act(() => open.fp().setDate([new Date(2026, 9, 7)], true));
      await waitFor(() => expect(audit().at(-1), tz).toBe(query({ since: "2026-10-07" })));
      await waitFor(() => expect([open.from().value, open.to().value], tz).toEqual(["07.10.2026", ""]));
      cleanup();
      urls = [];
    }
  });

  it("clears both dates: the server's default period, no error", async () => {
    const { from, to, fp } = await german();
    act(() => fp().setDate([new Date(2026, 9, 1), new Date(2026, 9, 20)], true));
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: "2026-10-01", until: "2026-10-20" })));
    act(() => fp().clear());
    await waitFor(() => expect(audit().at(-1)).toBe(query({})));
    await waitFor(() => expect([from().value, to().value]).toEqual(["", ""]));
    expect(screen.queryByRole("alert")).toBeNull();
    // Emptying the fields by hand does the same.
    act(() => fp().setDate([new Date(2026, 9, 1), new Date(2026, 9, 20)], true));
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: "2026-10-01", until: "2026-10-20" })));
    type(to(), "", "blur");
    type(from(), "", "blur");
    await waitFor(() => expect(audit().at(-1)).toBe(query({})));
    await waitFor(() => expect([from().value, to().value]).toEqual(["", ""]));
    expect(screen.queryByRole("alert")).toBeNull();
  });

  // An emptied From or a lone To (plan review of #377, note 1; #385): the
  // fields show exactly the dates that are sent.
  describe("fields and query agree", () => {
    it("From emptied and left, from the default period: no dates", async () => {
      const { from, to } = await german();
      type(from(), "", "blur");
      await waitFor(() => expect(audit().at(-1)).toBe(query({})));
      await waitFor(() => expect([from().value, to().value]).toEqual(["", ""]));
    });

    it("From emptied with both set: left or Enter, no dates (#385)", async () => {
      const { from, to, fp } = await german();
      for (const commit of ["blur", "enter"] as const) {
        act(() => fp().setDate([new Date(2026, 9, 1), new Date(2026, 9, 20)], true));
        await waitFor(() => expect(audit().at(-1), commit).toBe(query({ since: "2026-10-01", until: "2026-10-20" })));
        type(from(), "", commit);
        await waitFor(() => expect(audit().at(-1), commit).toBe(query({})));
        await waitFor(() => expect([from().value, to().value], commit).toEqual(["", ""]));
      }
    });

    it("only To typed, with the default From: Enter and leaving the field", async () => {
      for (const commit of ["enter", "blur"] as const) {
        const { from, to } = await german();
        type(to(), "05.10.2026", commit);
        await waitFor(() => expect(audit().at(-1), commit).toBe(query({ since: "2026-09-28", until: "2026-10-05" })));
        await waitFor(() => expect([from().value, to().value], commit).toEqual(["28.09.2026", "05.10.2026"]));
        cleanup();
        urls = [];
      }
    });

    it("only To typed after From was emptied, with Enter: no dates, To emptied (#385)", async () => {
      const { from, to, de } = await german();
      type(from(), "", "blur");
      await waitFor(() => expect(audit().at(-1)).toBe(query({})));
      const count = audit().length;
      type(to(), "05.10.2026", "enter");
      await waitFor(() => expect([from().value, to().value]).toEqual(["", ""]));
      // The next change is the only new request.
      fireEvent.change(screen.getByLabelText(de.logAction), { target: { value: "read" } });
      await waitFor(() => expect(audit().at(-1)).toBe(query({ group: "read" })));
      expect(audit()).toHaveLength(count + 1);
    });
  });

  // The shared case table of DateRange (#385), with each field left as a
  // browser leaves it (test/dateRangeCases.ts).
  describe("the case table", () => {
    let watching: ReturnType<typeof watch>;
    beforeEach(() => {
      watching = watch();
    });
    afterEach(() => watching.stop());

    const rangeOf = (url: string): Range => {
      const p = new URL(url, "http://x").searchParams;
      return { start: p.get("since") ?? "", end: p.get("until") ?? "" };
    };

    it.each(cases)("$name", async (c) => {
      const { from, to, fp, de } = await german();
      if (c.initial === "both") {
        act(() => fp().setDate([new Date(2026, 9, 1), new Date(2026, 9, 20)], true));
        await waitFor(() => expect(audit().at(-1)).toBe(query({ since: "2026-10-01", until: "2026-10-20" })));
      }
      const action = () => screen.getByLabelText(de.logAction);
      const picker = { from, to, other: action };
      const count = audit().length;
      run(picker, c.steps);
      // The next change is sent after every request of the steps.
      fireEvent.change(action(), { target: { value: "read" } });
      await waitFor(() => expect(audit().at(-1)).toContain("action=read"));
      expect(audit().slice(count, -1), "requests").toHaveLength(c.edits);
      const applied = rangeOf(audit().at(-1)!);
      if (c.range) expect(applied).toEqual(c.range);
      await waitFor(() => expect([from().value, to().value]).toEqual(fieldsOf(applied)));
      expect(document.activeElement).toBe(focusOf(picker, c.focus));
      expect(calendarOpen(picker), "calendar open").toBe(false);
      expect(watching.errors).toEqual([]);
    });
  });

  it("never sends what is not an ISO day for invalid typing, and does what the search filters do", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const iso = /^\d{4}-\d{2}-\d{2}$/;
    // The search filters' outcome for the same typing.
    const filters: string[] = [];
    function Filters() {
      const [q, setQ] = useState("");
      const open = useRef<() => boolean>(() => false);
      return <FilterPanel id="f" query={q} setQuery={(v) => { filters.push(v); setQ(v); }} group={false} setGroup={() => {}} calendarOpen={open} />;
    }
    applySettings({ language: "de", locale: "de" });
    const dt = filterTextFor("de");
    render(<Filters />);
    type(field(dt.since), "32.13.2026", "enter");
    type(field(dt.until), "abc", "enter");
    const filtersShow = [field(dt.since).value, field(dt.until).value];
    const filtersAfter = /after:(\S+)/.exec(filters.at(-1) ?? "")?.[1] ?? "";
    const filtersBefore = /before:(\S+)/.exec(filters.at(-1) ?? "")?.[1] ?? "";
    cleanup();

    const { from, to } = await german();
    const before = audit().length;
    type(from(), "32.13.2026", "enter");
    type(to(), "abc", "enter");
    // A later change shows the dates the typing left behind.
    fireEvent.change(screen.getByLabelText(messagesFor("de").logAction), { target: { value: "read" } });
    await waitFor(() => expect(audit().at(-1)).toContain("action=read"));
    for (const u of audit().slice(before)) {
      const p = new URL(u, "http://x").searchParams;
      for (const k of ["since", "until"]) if (p.has(k)) expect(p.get(k), u).toMatch(iso);
    }
    const last = new URL(audit().at(-1)!, "http://x").searchParams;
    await waitFor(() => expect([from().value, to().value]).toEqual(filtersShow));
    expect(last.get("since") ?? "").toBe(filtersAfter);
    // The filters keep an exclusive before:, the Log an inclusive until.
    expect(last.get("until") ?? "").toBe(filtersBefore ? new Date(Date.parse(filtersBefore) - 86_400_000).toISOString().slice(0, 10) : "");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("shows a set range as local days, and a re-pick of the same days sends no request", async () => {
    vi.stubEnv("TZ", "America/Los_Angeles");
    const { from, to, fp, de } = await german();
    act(() => fp().setDate([new Date(2026, 9, 1), new Date(2026, 9, 10)], true));
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: "2026-10-01", until: "2026-10-10" })));
    await waitFor(() => expect([from().value, to().value]).toEqual(["01.10.2026", "10.10.2026"]));
    const count = audit().length;
    act(() => fp().setDate([new Date(2026, 9, 1), new Date(2026, 9, 10)], true));
    // The next change is the only new request.
    fireEvent.change(screen.getByLabelText(de.logAction), { target: { value: "read" } });
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: "2026-10-01", until: "2026-10-10", group: "read" })));
    expect(audit()).toHaveLength(count + 1);
  });

  it("labels the calendar in German", async () => {
    const { fp } = await german();
    const dt = filterTextFor("de");
    act(() => fp().setDate([new Date(2026, 9, 1)], true));
    act(() => fp().open());
    const calendar = fp().calendarContainer;
    expect(calendar.getAttribute("aria-label")).toBe(dt.calendar);
    expect(calendar.querySelector(`[aria-label="${dt.nextMonth}"]`)).not.toBeNull();
    expect(calendar.textContent).toContain("Oktober");
  });
});
