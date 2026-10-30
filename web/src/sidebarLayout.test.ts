import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { clampWidth, loadLayout, saveLayout, sidebarCap, useSidebarCap, useSidebarLayout } from "./sidebarLayout";

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

describe("the cap (#310)", () => {
  it("clamps to an upper bound", () => {
    expect(clampWidth(28, 16.75)).toBe(16.75);
    expect(clampWidth(16.8, 16.75)).toBe(16.75);
    expect(clampWidth(15.1, 16.75)).toBe(15);
    expect(clampWidth(5, 16.75)).toBe(12);
    expect(clampWidth(NaN, 12)).toBe(12);
  });

  it("leaves the reader 23rem next to a 26rem list, rounded down to 0.25rem", () => {
    expect(sidebarCap(63)).toBe(14); // a 66rem window
    expect(sidebarCap(77)).toBe(28); // an 80rem window
    expect(sidebarCap(120)).toBe(28);
    expect(sidebarCap(50)).toBe(12);
    // A 1102px window: 65.875rem; 16.875 is not a step, so 16.75, not 17.
    expect(sidebarCap(65.875)).toBe(16.75);
    expect(sidebarCap(65.9)).toBe(16.75);
    // Not measured: the full maximum.
    expect(sidebarCap(0)).toBe(28);
    expect(sidebarCap(NaN)).toBe(28);
  });

  it("uses the capped width without saving it; a wider window brings the preference back", () => {
    localStorage.setItem(key, JSON.stringify({ v: 1, width: 28, collapsed: false }));
    const setItem = vi.spyOn(Storage.prototype, "setItem");
    const { result, rerender } = renderHook(({ cap }) => useSidebarLayout("alice", cap), { initialProps: { cap: 16.75 } });
    expect(result.current.width).toBe(16.75);
    expect(result.current.max).toBe(16.75);
    rerender({ cap: 14 });
    expect(result.current.width).toBe(14);
    expect(setItem).not.toHaveBeenCalled();
    // Collapsing saves the preference, not the capped width.
    act(() => result.current.setCollapsed(true));
    expect(JSON.parse(localStorage.getItem(key)!)).toEqual({ v: 1, width: 28, collapsed: true });
    rerender({ cap: 28 });
    expect(result.current.width).toBe(28);
    // A user action saves the clamped width.
    rerender({ cap: 16.75 });
    act(() => result.current.commitWidth(30));
    expect(JSON.parse(localStorage.getItem(key)!)).toEqual({ v: 1, width: 16.75, collapsed: true });
  });

  it("measures the element on attach, follows its resizes, and stops on detach", () => {
    const observers: { cb: () => void; el?: Element; off: boolean }[] = [];
    vi.stubGlobal(
      "ResizeObserver",
      class {
        o: { cb: () => void; el?: Element; off: boolean };
        constructor(cb: () => void) {
          this.o = { cb, off: false };
          observers.push(this.o);
        }
        observe(el: Element) {
          this.o.el = el;
        }
        unobserve() {}
        disconnect() {
          this.o.off = true;
        }
      },
    );
    let width = 63 * 16;
    const el = document.createElement("div");
    el.getBoundingClientRect = () => DOMRect.fromRect({ width, height: 1 });
    const { result } = renderHook(() => useSidebarCap());
    expect(result.current[0]).toBe(28);
    act(() => result.current[1](el));
    expect(result.current[0]).toBe(14);
    expect(observers.map((o) => o.el)).toEqual([el]);
    width = 66 * 16;
    act(() => observers[0]!.cb());
    expect(result.current[0]).toBe(17);
    act(() => result.current[1](null));
    expect(observers[0]!.off).toBe(true);
  });

  it("is the full maximum without ResizeObserver", () => {
    vi.stubGlobal("ResizeObserver", undefined);
    const el = document.createElement("div");
    el.getBoundingClientRect = () => DOMRect.fromRect({ width: 100, height: 1 });
    const { result } = renderHook(() => useSidebarCap());
    act(() => result.current[1](el));
    expect(result.current[0]).toBe(28);
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
