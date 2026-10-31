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
import { dayOfDate, shiftDay } from "./format";
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
  // The page keeps its filters in the address, under view=log (#376).
  window.history.replaceState(null, "", "/?view=log");
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
  window.history.replaceState(null, "", "/");
  vi.restoreAllMocks();
  vi.useRealTimers();
  vi.unstubAllEnvs();
  applySettings({ language: "en", locale: "en" });
  document.querySelectorAll(".flatpickr-calendar").forEach((c) => c.remove());
});

/** Unmounts the page and starts again from a fresh address, as a new page load does. */
function restart() {
  cleanup();
  urls = [];
  window.history.replaceState(null, "", "/?view=log");
}

const audit = () => urls.filter((u) => u.startsWith("/api/audit"));
/** The request URL of a filter. */
const query = (f: Partial<ReturnType<typeof defaultFilter>>, cursor = "") =>
  `/api/audit?${auditQuery({ since: "", until: "", actions: [], users: [], actors: [], ...f }, cursor)}`;

/** A filter's field (Carbon's MultiSelect), by its label; with a choice the label also names the count. */
const filterField = (label: string) => screen.getByRole("combobox", { name: new RegExp(`^${label}`) });
/** The text in a filter's field: the chosen names, or the text for all. */
const fieldText = (label: string) => filterField(label).querySelector(".cds--list-box__label")?.textContent;
/**
 * Toggles options of a filter as a user clicks them, one after the other,
 * each once the field shows the last one (Carbon reports a choice after a
 * timeout), then closes the menu.
 */
async function choose(label: string, ...texts: string[]) {
  const field = filterField(label);
  if (field.getAttribute("aria-expanded") !== "true") fireEvent.click(field);
  for (const text of texts) {
    const was = screen.getByRole("option", { name: text }).getAttribute("aria-selected");
    fireEvent.click(screen.getByRole("option", { name: text }));
    await waitFor(() => expect(screen.getByRole("option", { name: text }).getAttribute("aria-selected"), text).not.toBe(was));
  }
  fireEvent.click(filterField(label));
  expect(screen.queryByRole("listbox", { name: new RegExp(`^${label}`) })?.querySelector("[role=option]") ?? null).toBeNull();
}
/** The local days a request covers: since's day, and the day before until (#402: instants of local midnight, until exclusive). */
const daysOf = (url: string) => {
  const p = new URL(url, "http://x").searchParams;
  const since = p.get("since");
  const until = p.get("until");
  return { since: since ? dayOfDate(new Date(since)) : "", until: until ? shiftDay(dayOfDate(new Date(until)), -1) : "" };
};

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
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: "2026-10-01" })));
    act(() => fpOf(t.logFrom).setDate([new Date(2026, 9, 1), new Date(2026, 9, 31)], true));
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: "2026-10-01", until: "2026-10-31" })));
    await choose(t.logAction, t.logGroup("read"));
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: "2026-10-01", until: "2026-10-31", actions: ["read"] })));

    fireEvent.click(filterField(t.logWho));
    await waitFor(() => expect(screen.getAllByRole("option").map((o) => o.getAttribute("aria-label"))).toEqual([t.logCommandLine, t.logApiKeys, "alice", "bob"]));
    await choose(t.logWho, "bob");
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: "2026-10-01", until: "2026-10-31", actions: ["read"], users: ["bob"] })));
    await choose(t.logWho, "bob", t.logCommandLine);
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: "2026-10-01", until: "2026-10-31", actions: ["read"], actors: ["cli"] })));
    await choose(t.logWho, t.logCommandLine, t.logApiKeys);
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: "2026-10-01", until: "2026-10-31", actions: ["read"], actors: ["api_key"] })));
    await choose(t.logAction, t.logGroup("read"));
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: "2026-10-01", until: "2026-10-31", actors: ["api_key"] })));
  });

  it("offers every action group, and the Who filter only to admins", async () => {
    render(<LogPage admin={false} open={() => {}} />);
    await screen.findByText("Invoice 42");
    expect(fieldText(t.logAction)).toBe(t.logAllActions);
    fireEvent.click(filterField(t.logAction));
    const groups = screen.getAllByRole("option").map((o) => o.getAttribute("aria-label"));
    expect(groups).toEqual(["login", "search", "read", "export", "accounts", "users", "profile", "api_keys", "sync", "retention"].map((g) => t.logGroup(g)));
    expect(screen.queryByRole("combobox", { name: new RegExp(`^${t.logWho}`) })).toBeNull();
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

  it("shows a non-admin the Who column for other users' reads of a shared account, without their client (#389)", async () => {
    const shared = [
      entry("message.page", "00:20", {
        user: "pat", viaShare: true, actor: "api_key",
        details: { search: true, via_accounts: [{ id: 3, owner: 2, key: "alice/work", n: 2 }] },
      }),
      ...page1,
    ];
    vi.mocked(globalThis.fetch).mockImplementation(async () => new Response(JSON.stringify({ entries: shared }), { status: 200, headers: { "Content-Type": "application/json" } }));
    render(<LogPage admin={false} open={() => {}} />);
    await screen.findByText("Invoice 42");
    const rows = screen.getAllByRole("row");
    expect(within(rows[0]!).getAllByRole("columnheader").map((h) => h.textContent)).toEqual([t.logTime, t.logAction, t.logWho, t.logTarget, t.logClient]);
    const pat = within(rows[1]!);
    expect(pat.getAllByRole("cell")[2]!.textContent).toBe("pat");
    expect(pat.getByText(t.logShared("pat", "search", 2, "work"))).toBeTruthy();
    // The actor, not the key's name (decision 16).
    expect(pat.getByText(t.logSharedViaKey)).toBeTruthy();
    expect(pat.getAllByRole("cell")[4]!.textContent).toBe("");
    expect(screen.getByText(t.logOwnNote)).toBeTruthy();
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

// Several action groups and Who values, kept in the address (#376). The mock
// filters as the server does: any of the groups, and an entry of any of the
// users or actors; three entries a page.
describe("LogPage filters", () => {
  const people = [
    entry("message.open", "00:05", { target: { type: "account", id: "1", name: "alice reads" } }),
    entry("auth.login", "00:04", { user: "bob", target: { type: "account", id: "2", name: "bob logs in" } }),
    entry("account.add", "00:03", { actor: "cli", user: undefined, target: { type: "account", id: "3", name: "cli adds" } }),
    entry("message.search", "00:02", { user: "carol", actor: "api_key", apiKey: "k", target: { type: "account", id: "4", name: "carol's key searches" } }),
  ];
  const groupOf: Record<string, string> = { "message.open": "read", "auth.login": "login", "account.add": "accounts", "message.search": "search" };
  const names = people.map((e) => e.target!.name!);
  let userList: () => Promise<Response>;
  const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });

  beforeEach(() => {
    userList = async () => json({ users: [{ name: "alice" }, { name: "bob" }, { name: "carol" }, { name: "cli" }] });
    vi.mocked(globalThis.fetch).mockImplementation(async (url) => {
      const u = new URL(String(url), "http://x");
      urls.push(String(url));
      if (u.pathname === "/api/users") return userList();
      const p = u.searchParams;
      const [actions, users, actors] = [p.getAll("action"), p.getAll("user"), p.getAll("actor")];
      if (users.includes("gone")) return json({ error: "unknown user" }, 404);
      const match = people.filter(
        (e) =>
          (actions.length === 0 || actions.includes(groupOf[e.action]!)) &&
          ((users.length === 0 && actors.length === 0) || users.includes(e.user ?? "") || actors.includes(e.actor)),
      );
      const start = p.get("cursor") === "next" ? 3 : 0;
      return json({ entries: match.slice(start, start + 3), nextCursor: start + 3 < match.length ? "next" : undefined });
    });
  });

  /** The targets in the table, in its order. */
  const shownRows = () => names.filter((n) => screen.queryByText(n)).sort((a, b) => names.indexOf(a) - names.indexOf(b));
  const since = () => defaultFilter().since;

  it("filters by several groups and several Who values together, and the table shows what matches", async () => {
    render(<LogPage admin open={() => {}} />);
    await waitFor(() => expect(shownRows()).toEqual(names.slice(0, 3)));
    expect([fieldText(t.logAction), fieldText(t.logWho)]).toEqual([t.logAllActions, t.logEveryone]);

    // One group, then two: any of them.
    await choose(t.logAction, t.logGroup("read"));
    await waitFor(() => expect(shownRows()).toEqual(["alice reads"]));
    expect(audit().at(-1)).toBe(query({ since: since(), actions: ["read"] }));
    await choose(t.logAction, t.logGroup("accounts"));
    await waitFor(() => expect(shownRows()).toEqual(["alice reads", "cli adds"]));
    expect(audit().at(-1)).toBe(query({ since: since(), actions: ["read", "accounts"] }));
    // The field names the choice in the list's order, with Carbon's count tag.
    expect(fieldText(t.logAction)).toBe(`${t.logGroup("read")}, ${t.logGroup("accounts")}`);
    expect(screen.getByRole("combobox", { name: new RegExp(`^${t.logAction}`) }).closest(".cds--multi-select")!.querySelector(".cds--tag__label")?.textContent).toBe("2");

    // One user: within the groups, only theirs.
    await choose(t.logWho, "alice");
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: since(), actions: ["read", "accounts"], users: ["alice"] })));
    await waitFor(() => expect(shownRows()).toEqual(["alice reads"]));
    // A user and the command line: entries of either (union).
    await choose(t.logWho, t.logCommandLine);
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: since(), actions: ["read", "accounts"], users: ["alice"], actors: ["cli"] })));
    await waitFor(() => expect(shownRows()).toEqual(["alice reads", "cli adds"]));
    expect(fieldText(t.logWho)).toBe(`${t.logCommandLine}, alice`);
    // Several users and both actors, all groups.
    await choose(t.logWho, t.logApiKeys, "bob");
    await choose(t.logAction, t.logGroup("read"), t.logGroup("accounts"));
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: since(), users: ["alice", "bob"], actors: ["cli", "api_key"] })));
    await waitFor(() => expect(shownRows()).toEqual(names.slice(0, 3)));
    expect(fieldText(t.logAction)).toBe(t.logAllActions);

    // The tag's clear button empties the choice: no parameter at all.
    fireEvent.click(within(filterField(t.logWho).closest(".cds--multi-select")!).getByRole("button", { name: t.carbon.ids["clear.all"] }));
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: since() })));
    expect(fieldText(t.logWho)).toBe(t.logEveryone);
    expect(window.location.search).toBe("?view=log");
  });

  it("loads more with the cursor and every value of the filter", async () => {
    window.history.replaceState(null, "", "/?view=log&action=read&action=login&action=accounts&action=search&user=alice&user=bob&actor=cli&actor=api_key");
    render(<LogPage admin open={() => {}} />);
    await waitFor(() => expect(shownRows()).toEqual(names.slice(0, 3)));
    fireEvent.click(screen.getByRole("button", { name: t.logMore }));
    await waitFor(() => expect(shownRows()).toEqual(names));
    const f = { since: since(), actions: ["login", "search", "read", "accounts"], users: ["alice", "bob"], actors: ["cli", "api_key"] };
    expect(audit()).toEqual([query(f), query(f, "next")]);
    expect(screen.queryByRole("button", { name: t.logMore })).toBeNull();
  });

  it("sends a user named cli as a user, and the command line as the actor", async () => {
    render(<LogPage admin open={() => {}} />);
    await waitFor(() => expect(shownRows()).toHaveLength(3));
    await choose(t.logWho, "cli");
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: since(), users: ["cli"] })));
    expect(window.location.search).toBe("?view=log&user=cli");
    await choose(t.logWho, "cli", t.logCommandLine);
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: since(), actors: ["cli"] })));
    expect(window.location.search).toBe("?view=log&actor=cli");
  });

  it("keeps the filters in the address as a replace, and reads them back on a reload and on Back", async () => {
    const push = vi.spyOn(window.history, "pushState");
    render(<LogPage admin open={() => {}} />);
    await waitFor(() => expect(shownRows()).toHaveLength(3));
    await choose(t.logAction, t.logGroup("read"), t.logGroup("export"));
    await choose(t.logWho, "bob", t.logCommandLine);
    await waitFor(() => expect(window.location.search).toBe("?view=log&action=read&action=export&user=bob&actor=cli"));
    act(() => fpOf(t.logFrom).setDate([new Date(2025, 0, 1), new Date(2025, 0, 20)], true));
    await waitFor(() => expect(window.location.search).toBe("?view=log&since=2025-01-01&until=2025-01-20&action=read&action=export&user=bob&actor=cli"));
    expect(push).not.toHaveBeenCalled();

    // A reload: the same request, the fields show the choice.
    const address = window.location.search;
    restart();
    window.history.replaceState(null, "", `/${address}`);
    render(<LogPage admin open={() => {}} />);
    const f = { since: "2025-01-01", until: "2025-01-20", actions: ["read", "export"], users: ["bob"], actors: ["cli"] };
    await waitFor(() => expect(audit()).toEqual([query(f)]));
    expect(fieldText(t.logAction)).toBe(`${t.logGroup("read")}, ${t.logGroup("export")}`);
    await waitFor(() => expect(fieldText(t.logWho)).toBe(`${t.logCommandLine}, bob`));
    expect([field(t.logFrom).value, field(t.logTo).value]).toEqual(["01/01/2025", "01/20/2025"]);

    // Back to an entry with another filter (a message opened from the Log pushed one).
    window.history.pushState(null, "", "/?m=abc");
    window.history.pushState(null, "", "/?view=log&action=login");
    act(() => {
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: since(), actions: ["login"] })));
    await waitFor(() => expect(shownRows()).toEqual(["bob logs in"]));
    expect([fieldText(t.logAction), fieldText(t.logWho)]).toEqual([t.logGroup("login"), t.logEveryone]);
  });

  it("writes since only when it is not the default, and since= for no From", async () => {
    render(<LogPage admin open={() => {}} />);
    await waitFor(() => expect(shownRows()).toHaveLength(3));
    expect(window.location.search).toBe("?view=log");
    // Not the default (30 days back from today).
    act(() => fpOf(t.logFrom).setDate([new Date(2025, 0, 1)], true));
    await waitFor(() => expect(window.location.search).toBe("?view=log&since=2025-01-01"));
    act(() => fpOf(t.logFrom).clear());
    await waitFor(() => expect(window.location.search).toBe("?view=log&since="));
    await waitFor(() => expect(audit().at(-1)).toBe(query({})));
    // The default day picked again: not written, the window moves on.
    const [y, m, d] = since().split("-").map(Number);
    act(() => fpOf(t.logFrom).setDate([new Date(y!, m! - 1, d!)], true));
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: since() })));
    expect(window.location.search).toBe("?view=log");

    // since= on load: no From.
    restart();
    window.history.replaceState(null, "", "/?view=log&since=");
    render(<LogPage admin open={() => {}} />);
    await waitFor(() => expect(audit()).toEqual([query({})]));
    expect(field(t.logFrom).value).toBe("");
  });

  it("sends one request per change, and none when the page renders again unchanged", async () => {
    const { rerender } = render(<LogPage admin open={() => {}} />);
    await waitFor(() => expect(shownRows()).toHaveLength(3));
    expect(audit()).toHaveLength(1);
    rerender(<LogPage admin open={() => {}} />);
    act(() => {
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    expect(audit()).toHaveLength(1);
    await choose(t.logAction, t.logGroup("read"));
    await waitFor(() => expect(shownRows()).toEqual(["alice reads"]));
    rerender(<LogPage admin open={() => {}} />);
    expect(audit()).toEqual([query({ since: since() }), query({ since: since(), actions: ["read"] })]);
  });

  it("Escape leaves Aktion and keeps the choice; Delete and Backspace clear it (#411)", async () => {
    render(<LogPage admin open={() => {}} />);
    await waitFor(() => expect(shownRows()).toHaveLength(3));
    await choose(t.logAction, t.logGroup("login"), t.logGroup("search"));
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: since(), actions: ["login", "search"] })));
    const address = "?view=log&action=login&action=search";
    expect(window.location.search).toBe(address);
    const text = `${t.logGroup("login")}, ${t.logGroup("search")}`;
    expect(fieldText(t.logAction)).toBe(text);
    const settled = audit().length;

    // The menu open: Escape only closes it, the focus stays on the field.
    const action = filterField(t.logAction);
    act(() => action.focus());
    fireEvent.keyDown(action, { key: "ArrowDown", keyCode: 40 });
    await waitFor(() => expect(action.getAttribute("aria-expanded")).toBe("true"));
    fireEvent.keyDown(action, { key: "Escape", keyCode: 27 });
    await waitFor(() => expect(action.getAttribute("aria-expanded")).toBe("false"));
    expect(document.activeElement).toBe(action);

    // The menu closed: Escape leaves the field and keeps the choice.
    fireEvent.keyDown(action, { key: "Escape", keyCode: 27 });
    expect(document.activeElement).not.toBe(action);
    expect(fieldText(t.logAction)).toBe(text);
    expect(window.location.search).toBe(address);

    // Delete on the field clears it: exactly one request since the choice, so the Escapes sent none.
    act(() => action.focus());
    fireEvent.keyDown(action, { key: "Delete", keyCode: 46 });
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: since() })));
    expect(audit()).toHaveLength(settled + 1);
    expect(fieldText(t.logAction)).toBe(t.logAllActions);
    expect(window.location.search).toBe("?view=log");

    // Backspace on Wer clears it as well.
    await choose(t.logWho, "bob");
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: since(), users: ["bob"] })));
    const who = filterField(t.logWho);
    act(() => who.focus());
    fireEvent.keyDown(who, { key: "Backspace", keyCode: 8 });
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: since() })));
    expect(fieldText(t.logWho)).toBe(t.logEveryone);
  });

  it("drops users that are gone and unknown actors once the user list is there, with one request", async () => {
    let release!: () => void;
    const listed = new Promise<void>((r) => {
      release = r;
    });
    const answer = userList;
    userList = async () => {
      await listed;
      return answer();
    };
    const replace = vi.spyOn(window.history, "replaceState");
    window.history.replaceState(null, "", "/?view=log&user=alice&user=removed&actor=session&actor=cli");
    replace.mockClear();
    render(<LogPage admin open={() => {}} />);
    await waitFor(() => expect(urls).toEqual(["/api/users"]));
    // The request waits for the list.
    expectLoading();
    await act(async () => release());
    await waitFor(() => expect(shownRows()).toEqual(["alice reads", "cli adds"]));
    expect(audit()).toEqual([query({ since: since(), users: ["alice"], actors: ["cli"] })]);
    expect(window.location.search).toBe("?view=log&user=alice&actor=cli");
    expect(replace).toHaveBeenCalledTimes(1);
    expect(fieldText(t.logWho)).toBe(`${t.logCommandLine}, alice`);
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("keeps the address as it is when the user list fails, and shows the error of the request", async () => {
    userList = async () => json({ error: "boom" }, 500);
    window.history.replaceState(null, "", "/?view=log&user=gone&user=bob");
    render(<LogPage admin open={() => {}} />);
    await screen.findByRole("alert");
    expect(audit()).toEqual([query({ since: since(), users: ["gone", "bob"] })]);
    expect(window.location.search).toBe("?view=log&user=gone&user=bob");
    // The chosen names stay in the field.
    expect(fieldText(t.logWho)).toBe("gone, bob");
  });

  it("ignores user and actor in the address for non-admins", async () => {
    window.history.replaceState(null, "", "/?view=log&action=read&user=bob&actor=cli");
    render(<LogPage admin={false} open={() => {}} />);
    await waitFor(() => expect(shownRows()).toEqual(["alice reads"]));
    expect(urls).toEqual([query({ since: since(), actions: ["read"] })]);
    expect(screen.queryByRole("combobox", { name: new RegExp(`^${t.logWho}`) })).toBeNull();
  });

  it("names the choice and Carbon's controls in German", async () => {
    applySettings({ language: "de", locale: "de" });
    const de = messagesFor("de");
    render(<LogPage admin open={() => {}} />);
    await waitFor(() => expect(shownRows()).toHaveLength(3));
    expect([fieldText(de.logAction), fieldText(de.logWho)]).toEqual(["Alle Aktionen", "Alle"]);
    await choose(de.logAction, de.logGroup("read"), de.logGroup("login"));
    await choose(de.logWho, de.logCommandLine);
    await waitFor(() => expect(fieldText(de.logAction)).toBe("Anmeldungen, Lesen"));
    expect(fieldText(de.logWho)).toBe("Kommandozeile");
    fireEvent.click(filterField(de.logAction));
    const html = document.body.innerHTML;
    for (const e of [/Total items/, /Clear selected/, /clear selection/i, /Open menu/, /Close menu/, /have been cleared/]) expect(html, String(e)).not.toMatch(e);
    expect(html).toContain(de.carbon.selection.description);
    expect(screen.getAllByRole("button", { name: "Auswahl leeren" })).toHaveLength(2);
    expect(screen.getByTitle("Menü schließen")).toBeTruthy();
    expect(screen.getAllByTitle("Menü öffnen")).toHaveLength(1);
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
// user's format with Carbon's calendar. The days are local, sent as the
// instants of local midnight (auditQuery, #402; api.test.ts pins them). The
// clock is frozen at noon UTC.
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
    // The default since: 30 local days back.
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

  it("sends typed German dates as local days, To inclusive", async () => {
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
      restart();
      // Only a start picked so far: since only.
      const open = await german();
      act(() => open.fp().setDate([new Date(2026, 9, 7)], true));
      await waitFor(() => expect(audit().at(-1), tz).toBe(query({ since: "2026-10-07" })));
      await waitFor(() => expect([open.from().value, open.to().value], tz).toEqual(["07.10.2026", ""]));
      restart();
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
        restart();
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
      await choose(de.logAction, de.logGroup("read"));
      await waitFor(() => expect(audit().at(-1)).toBe(query({ actions: ["read"] })));
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
      const d = daysOf(url);
      return { start: d.since, end: d.until };
    };

    it.each(cases)("$name", async (c) => {
      const { from, to, fp, de } = await german();
      if (c.initial === "both") {
        act(() => fp().setDate([new Date(2026, 9, 1), new Date(2026, 9, 20)], true));
        await waitFor(() => expect(audit().at(-1)).toBe(query({ since: "2026-10-01", until: "2026-10-20" })));
      }
      const action = () => filterField(de.logAction);
      const picker = { from, to, other: action };
      const count = audit().length;
      run(picker, c.steps);
      // The next change is sent after every request of the steps.
      await choose(de.logAction, de.logGroup("read"));
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

  it("never sends what is not a local midnight for invalid typing, and does what the search filters do", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const instant = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/;
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
    await choose(messagesFor("de").logAction, messagesFor("de").logGroup("read"));
    await waitFor(() => expect(audit().at(-1)).toContain("action=read"));
    for (const u of audit().slice(before)) {
      const p = new URL(u, "http://x").searchParams;
      for (const k of ["since", "until"]) if (p.has(k)) expect(p.get(k), u).toMatch(instant);
    }
    const last = daysOf(audit().at(-1)!);
    await waitFor(() => expect([from().value, to().value]).toEqual(filtersShow));
    expect(last.since).toBe(filtersAfter);
    // The filters keep an exclusive before:, the Log an inclusive To.
    expect(last.until).toBe(filtersBefore ? shiftDay(filtersBefore, -1) : "");
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
    await choose(de.logAction, de.logGroup("read"));
    await waitFor(() => expect(audit().at(-1)).toBe(query({ since: "2026-10-01", until: "2026-10-10", actions: ["read"] })));
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
