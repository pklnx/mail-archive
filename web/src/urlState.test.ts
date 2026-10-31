import { act, renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { formatState, parseState, useViewState, type ViewState } from "./urlState";

const empty: ViewState = { q: "", account: "", folder: "", group: "", gone: "", m: "", view: "", tab: "", id: "", add: "", import: "", since: null, until: "", action: [], user: [], actor: [] };

describe("url state", () => {
  it("round-trips and omits empty fields", () => {
    const s = { ...empty, q: "rechnung oktober", account: "privat", folder: "[Gmail]/All Mail", group: "1" };
    const qs = formatState(s);
    expect(qs).not.toContain("m=");
    expect(parseState(qs)).toEqual(s);
    expect(formatState(empty)).toBe("");
  });
  it("ignores unknown parameters", () => {
    expect(parseState("?x=1&m=abc")).toEqual({ ...empty, m: "abc" });
  });
  it("keeps only-in-archive only as gone=1", () => {
    expect(parseState("?gone=1&account=work")).toMatchObject({ gone: "1", account: "work" });
    expect(parseState("?gone=true").gone).toBe("");
    expect(formatState({ ...empty, account: "work", gone: "1" })).toBe("?account=work&gone=1");
  });
  it("keeps grouping only as group=1", () => {
    expect(parseState("?group=1").group).toBe("1");
    expect(parseState("?group=yes").group).toBe("");
    expect(formatState({ ...empty, group: "1" })).toBe("?group=1");
  });
});

describe("the page keys", () => {
  it("round-trip per view", () => {
    const pages: Partial<ViewState>[] = [
      { view: "accounts", add: "1" },
      { view: "accounts", id: "work" },
      { view: "accounts", import: "journal" },
      { view: "users", id: "bob" },
      { view: "retention", tab: "holds" },
      { view: "retention", tab: "runs", id: "12" },
      { view: "retention", tab: "rules", add: "rule" },
      { view: "retention", tab: "holds", add: "hold" },
    ];
    for (const page of pages) {
      const s = { ...empty, ...page };
      expect(parseState(formatState(s)), JSON.stringify(page)).toEqual(s);
    }
    expect(formatState({ ...empty, view: "accounts", add: "1" })).toBe("?view=accounts&add=1");
    expect(formatState({ ...empty, view: "users", id: "bob" })).toBe("?view=users&id=bob");
    expect(formatState({ ...empty, view: "retention", tab: "runs", id: "12" })).toBe("?view=retention&tab=runs&id=12");
    // Names are encoded, so any account name comes back as it was.
    const odd = { ...empty, view: "accounts", id: "a&b=c d/ä" };
    expect(parseState(formatState(odd)).id).toBe("a&b=c d/ä");
  });
  it("keep only the values of their view", () => {
    expect(parseState("?view=accounts&add=1").add).toBe("1");
    expect(parseState("?view=accounts&add=yes").add).toBe("");
    expect(parseState("?view=accounts&add=rule").add).toBe("");
    expect(parseState("?view=retention&add=hold").add).toBe("hold");
    expect(parseState("?view=retention&add=1").add).toBe("");
    expect(parseState("?view=retention&tab=other").tab).toBe("");
  });
  it("read edit= as id on Accounts, for old links", () => {
    expect(parseState("?view=accounts&edit=work")).toEqual({ ...empty, view: "accounts", id: "work" });
    expect(parseState("?view=accounts&id=new&edit=old").id).toBe("new");
    expect(formatState(parseState("?view=accounts&edit=work"))).toBe("?view=accounts&id=work");
    expect(parseState("?view=users&edit=bob").id).toBe("");
  });
  it("mean nothing outside their views", () => {
    expect(parseState("?add=1&id=work&import=journal&tab=holds")).toEqual(empty);
    expect(parseState("?view=users&add=1&import=journal&tab=runs")).toEqual({ ...empty, view: "users" });
    expect(parseState("?view=log&id=7")).toEqual({ ...empty, view: "log" });
    expect(parseState("?view=accounts&tab=holds").tab).toBe("");
    expect(formatState({ ...empty, view: "log", import: "journal", id: "x", tab: "runs", add: "1" })).toBe("?view=log");
  });
  it("go when the view changes, so coming back shows the list", () => {
    for (const url of ["?view=accounts&id=work", "?view=users&id=bob", "?view=retention&tab=holds&id=3"]) {
      const leaving = { ...parseState(url), view: "" };
      expect(formatState(leaving), url).toBe("");
      const back = parseState(formatState({ ...parseState(formatState(leaving)), view: parseState(url).view }));
      expect(back, url).toEqual({ ...empty, view: parseState(url).view });
    }
  });
});

// The Log's filters (#376): repeated keys, only under view=log.
describe("the Log's filters", () => {
  const log = { ...empty, view: "log" };
  it("round-trip with repeated keys", () => {
    const s = { ...log, since: "2026-10-01", until: "2026-10-20", action: ["read", "export"], user: ["alice", "a&b c"], actor: ["cli", "api_key"] };
    const qs = formatState(s);
    expect(qs).toBe("?view=log&since=2026-10-01&until=2026-10-20&action=read&action=export&user=alice&user=a%26b+c&actor=cli&actor=api_key");
    expect(parseState(qs)).toEqual(s);
    expect(formatState(log)).toBe("?view=log");
  });
  it("tell the default From (no key) from no From (since=)", () => {
    expect(parseState("?view=log").since).toBeNull();
    expect(parseState("?view=log&since=").since).toBe("");
    expect(formatState({ ...log, since: "" })).toBe("?view=log&since=");
    expect(formatState({ ...log, since: null })).toBe("?view=log");
  });
  it("drop unknown groups and actors, empty values, repeats and what is not a day", () => {
    const s = parseState("?view=log&action=nope&action=export&action=read&action=read&actor=session&actor=api_key&actor=cli&user=&user=bob&user=bob&user=cli&since=yesterday&until=soon");
    // Groups and actors in the list's order; users as given.
    expect(s).toEqual({ ...log, action: ["read", "export"], actor: ["cli", "api_key"], user: ["bob", "cli"] });
    expect(parseState("?view=log&until=2026-10-20").until).toBe("2026-10-20");
  });
  it("mean nothing outside the Log", () => {
    expect(parseState("?since=2026-10-01&until=2026-10-20&action=read&user=bob&actor=cli")).toEqual(empty);
    expect(parseState("?view=users&action=read&user=bob")).toEqual({ ...empty, view: "users" });
    expect(formatState({ ...empty, since: "2026-10-01", until: "2026-10-20", action: ["read"], user: ["bob"], actor: ["cli"] })).toBe("");
  });
  it("stay behind when another view opens (its own history entry keeps them for Back)", () => {
    window.history.replaceState(null, "", "/?view=log&action=read&user=bob");
    const { result } = renderHook(() => useViewState());
    act(() => result.current[1]({ view: "", m: "abc" }));
    expect(window.location.search).toBe("?m=abc");
    window.history.replaceState(null, "", "/");
  });
});

describe("useViewState", () => {
  it("leaves the page keys behind when the view changes", () => {
    window.history.replaceState(null, "", "/?view=users&id=work");
    const { result } = renderHook(() => useViewState());
    // Accounts also has id=, but the user's name is no account.
    act(() => result.current[1]({ view: "accounts", m: "" }));
    expect(window.location.search).toBe("?view=accounts");
    act(() => result.current[1]({ id: "work" }));
    expect(window.location.search).toBe("?view=accounts&id=work");
    // A patch in the same view keeps them.
    act(() => result.current[1]({ q: "x" }));
    expect(window.location.search).toBe("?q=x&view=accounts&id=work");
    act(() => result.current[1]({ view: "retention", tab: "holds" }));
    expect(window.location.search).toBe("?q=x&view=retention&tab=holds");
    window.history.replaceState(null, "", "/");
  });
});
