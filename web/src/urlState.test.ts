import { describe, expect, it } from "vitest";
import { formatState, parseState } from "./urlState";

describe("url state", () => {
  it("round-trips and omits empty fields", () => {
    const s = { q: "rechnung oktober", account: "privat", folder: "[Gmail]/All Mail", m: "", view: "" };
    const qs = formatState(s);
    expect(qs).not.toContain("m=");
    expect(parseState(qs)).toEqual(s);
    expect(formatState({ q: "", account: "", folder: "", m: "", view: "" })).toBe("");
  });
  it("ignores unknown parameters", () => {
    expect(parseState("?x=1&m=abc")).toEqual({ q: "", account: "", folder: "", m: "abc", view: "" });
  });
});
