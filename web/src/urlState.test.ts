import { describe, expect, it } from "vitest";
import { formatState, parseState } from "./urlState";

describe("url state", () => {
  it("round-trips and omits empty fields", () => {
    const s = { q: "rechnung oktober", account: "privat", folder: "[Gmail]/All Mail", group: "1", gone: "", m: "", view: "" };
    const qs = formatState(s);
    expect(qs).not.toContain("m=");
    expect(parseState(qs)).toEqual(s);
    expect(formatState({ q: "", account: "", folder: "", group: "", gone: "", m: "", view: "" })).toBe("");
  });
  it("ignores unknown parameters", () => {
    expect(parseState("?x=1&m=abc")).toEqual({ q: "", account: "", folder: "", group: "", gone: "", m: "abc", view: "" });
  });
  it("keeps only-in-archive only as gone=1", () => {
    expect(parseState("?gone=1&account=work")).toMatchObject({ gone: "1", account: "work" });
    expect(parseState("?gone=true").gone).toBe("");
    expect(formatState({ q: "", account: "work", folder: "", group: "", gone: "1", m: "", view: "" })).toBe("?account=work&gone=1");
  });
  it("keeps grouping only as group=1", () => {
    expect(parseState("?group=1").group).toBe("1");
    expect(parseState("?group=yes").group).toBe("");
    expect(formatState({ q: "", account: "", folder: "", group: "1", gone: "", m: "", view: "" })).toBe("?group=1");
  });
});
