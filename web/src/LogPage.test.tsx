// The Log page on Carbon (#168 part 5): the filters build the same query
// as before, the table, folded rows, the message link and Load more.
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { auditQuery, type AuditEntry } from "./api";
import { applySettings, messagesFor, t } from "./i18n";
import { defaultFilter, LogPage } from "./LogPage";
import { expectLoaded, expectLoading } from "./test/loading";

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
});

const audit = () => urls.filter((u) => u.startsWith("/api/audit"));

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

    fireEvent.change(screen.getByLabelText(t.logFrom), { target: { value: "2026-10-01" } });
    await waitFor(() => expect(audit().at(-1)).toBe("/api/audit?since=2026-10-01"));
    fireEvent.change(screen.getByLabelText(t.logTo), { target: { value: "2026-10-31" } });
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
