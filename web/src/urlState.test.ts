import { describe, expect, it } from "vitest";
import { formatState, parseState, type ViewState } from "./urlState";

const empty: ViewState = { q: "", account: "", folder: "", group: "", gone: "", m: "", view: "", add: "", edit: "", import: "" };

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

describe("the pages under Accounts", () => {
  it("round-trip add, edit and import", () => {
    for (const page of [{ add: "1" }, { edit: "work" }, { import: "journal" }]) {
      const s = { ...empty, view: "accounts", ...page };
      expect(parseState(formatState(s))).toEqual(s);
    }
    expect(formatState({ ...empty, view: "accounts", add: "1" })).toBe("?view=accounts&add=1");
    // Names are encoded, so any account name comes back as it was.
    const odd = { ...empty, view: "accounts", edit: "a&b=c d/ä" };
    expect(parseState(formatState(odd)).edit).toBe("a&b=c d/ä");
  });
  it("keep add only as add=1", () => {
    expect(parseState("?view=accounts&add=1").add).toBe("1");
    expect(parseState("?view=accounts&add=yes").add).toBe("");
  });
  it("mean nothing outside the Accounts view", () => {
    expect(parseState("?add=1&edit=work&import=journal")).toEqual(empty);
    expect(parseState("?view=users&edit=work")).toEqual({ ...empty, view: "users" });
    // Leaving Accounts from a page drops the page, so coming back shows the list.
    const leaving = { ...parseState("?view=accounts&edit=work"), view: "" };
    expect(formatState(leaving)).toBe("");
    expect(formatState({ ...empty, view: "log", import: "journal" })).toBe("?view=log");
  });
});
