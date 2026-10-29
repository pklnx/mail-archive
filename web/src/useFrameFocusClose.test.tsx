import { cleanup, renderHook } from "@testing-library/react";
import { act } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useFrameFocusClose } from "./useFrameFocusClose";

afterEach(() => {
  cleanup();
  document.body.replaceChildren();
});

function focused(tag: "iframe" | "button") {
  const el = document.body.appendChild(document.createElement(tag));
  act(() => el.focus());
  expect(document.activeElement).toBe(el);
}

const blur = () => act(() => void window.dispatchEvent(new Event("blur")));

describe("useFrameFocusClose (#349)", () => {
  it("closes when the window blurs with an iframe active", () => {
    const close = vi.fn();
    renderHook(() => useFrameFocusClose(true, close));
    focused("iframe");
    blur();
    expect(close).toHaveBeenCalledTimes(1);
  });

  it("stays open when the window blurs with another element active (tab switch)", () => {
    const close = vi.fn();
    renderHook(() => useFrameFocusClose(true, close));
    focused("button");
    blur();
    expect(close).not.toHaveBeenCalled();
  });

  it("does nothing while closed", () => {
    const close = vi.fn();
    renderHook(() => useFrameFocusClose(false, close));
    focused("iframe");
    blur();
    expect(close).not.toHaveBeenCalled();
  });

  it("drops the listener on close and on unmount, and calls the latest callback", () => {
    const first = vi.fn();
    const second = vi.fn();
    const { rerender, unmount } = renderHook(({ open, close }) => useFrameFocusClose(open, close), { initialProps: { open: true, close: first } });
    focused("iframe");
    rerender({ open: true, close: second });
    blur();
    expect(first).not.toHaveBeenCalled();
    expect(second).toHaveBeenCalledTimes(1);
    rerender({ open: false, close: second });
    blur();
    expect(second).toHaveBeenCalledTimes(1);
    rerender({ open: true, close: second });
    unmount();
    blur();
    expect(second).toHaveBeenCalledTimes(1);
  });
});
