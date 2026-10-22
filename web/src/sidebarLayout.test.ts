import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { clampWidth, loadLayout, saveLayout, useSidebarLayout } from "./sidebarLayout";

const key = "mail-archive.sidebar-layout.alice";

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  localStorage.clear();
});

describe("storage", () => {
  it("keeps the layout per user", () => {
    saveLayout("alice", { width: 20, collapsed: true });
    expect(loadLayout("alice")).toEqual({ width: 20, collapsed: true });
    expect(loadLayout("bob")).toEqual({ width: 14, collapsed: false });
    expect(JSON.parse(localStorage.getItem(key)!)).toEqual({ v: 1, width: 20, collapsed: true });
    // The folder state of the sidebar has its own key.
    expect(localStorage.getItem("mail-archive.sidebar.alice")).toBeNull();
    saveLayout("alice", { width: 14, collapsed: false });
    expect(localStorage.getItem(key)).toBeNull();
  });

  it("reads invalid or blocked storage as the default", () => {
    localStorage.setItem(key, "{nope");
    expect(loadLayout("alice")).toEqual({ width: 14, collapsed: false });
    localStorage.setItem(key, JSON.stringify({ v: 2, width: 20, collapsed: true }));
    expect(loadLayout("alice")).toEqual({ width: 14, collapsed: false });
    localStorage.setItem(key, JSON.stringify({ v: 1, width: "wide", collapsed: true }));
    expect(loadLayout("alice")).toEqual({ width: 14, collapsed: true });
    localStorage.setItem(key, JSON.stringify({ v: 1, width: 20, collapsed: "yes" }));
    expect(loadLayout("alice")).toEqual({ width: 20, collapsed: false });
    localStorage.setItem(key, "null");
    expect(loadLayout("alice")).toEqual({ width: 14, collapsed: false });
    const fail = () => {
      throw new Error("blocked");
    };
    vi.stubGlobal("localStorage", { getItem: fail, setItem: fail, removeItem: fail });
    expect(loadLayout("alice")).toEqual({ width: 14, collapsed: false });
    expect(() => saveLayout("alice", { width: 20, collapsed: true })).not.toThrow();
  });

  it("clamps the width", () => {
    expect(clampWidth(5)).toBe(12);
    expect(clampWidth(40)).toBe(28);
    expect(clampWidth(NaN)).toBe(14);
    expect(clampWidth(Infinity)).toBe(14);
    expect(clampWidth(13.3)).toBe(13.25);
    localStorage.setItem(key, JSON.stringify({ v: 1, width: 50, collapsed: false }));
    expect(loadLayout("alice").width).toBe(28);
  });
});

describe("useSidebarLayout", () => {
  it("reads the stored layout on the first render", () => {
    localStorage.setItem(key, JSON.stringify({ v: 1, width: 20, collapsed: true }));
    const { result } = renderHook(() => useSidebarLayout("alice"));
    expect(result.current.width).toBe(20);
    expect(result.current.collapsed).toBe(true);
  });

  it("saves on commit, not while dragging", () => {
    const setItem = vi.spyOn(Storage.prototype, "setItem");
    const { result } = renderHook(() => useSidebarLayout("alice"));
    act(() => result.current.setWidth(18));
    act(() => result.current.setWidth(19));
    expect(result.current.width).toBe(19);
    expect(setItem).not.toHaveBeenCalled();
    act(() => result.current.commitWidth(19.1));
    expect(setItem).toHaveBeenCalledTimes(1);
    expect(JSON.parse(localStorage.getItem(key)!)).toEqual({ v: 1, width: 19, collapsed: false });
    act(() => result.current.setCollapsed(true));
    expect(JSON.parse(localStorage.getItem(key)!)).toEqual({ v: 1, width: 19, collapsed: true });
    act(() => result.current.resetWidth());
    expect(result.current.width).toBe(14);
    expect(JSON.parse(localStorage.getItem(key)!)).toEqual({ v: 1, width: 14, collapsed: true });
    expect(setItem).toHaveBeenCalledTimes(3);
  });
});
