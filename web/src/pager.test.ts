import { describe, expect, it } from "vitest";
import { noList, pager } from "./pager";

const nav = (ids: string[], more = false) => ({ ids, more, loadMore: () => {} });

describe("pager", () => {
  it("finds the neighbours in list order", () => {
    expect(pager(nav(["a", "b", "c"]), "b")).toEqual({ index: 1, prev: "a", next: "c", nextNeedsMore: false });
    expect(pager(nav(["a", "b", "c"]), "a")).toMatchObject({ prev: null, next: "b" });
  });
  it("asks for the next page at the end of a list with more", () => {
    expect(pager(nav(["a", "b"], true), "b")).toEqual({ index: 1, prev: "a", next: null, nextNeedsMore: true });
    expect(pager(nav(["a", "b"], false), "b")).toMatchObject({ next: null, nextNeedsMore: false });
  });
  it("has nothing for a message outside the list", () => {
    expect(pager(nav(["a", "b"]), "x")).toEqual({ index: -1, prev: null, next: null, nextNeedsMore: false });
    expect(pager(noList, "")).toMatchObject({ index: -1 });
  });
});
