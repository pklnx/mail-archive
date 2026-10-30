// The sidebar's width and whether it is collapsed to a rail, per user and
// browser. Applies only where the sidebar is always visible (lg and up).
// A convenience, not data: storage may be blocked.
import { useCallback, useRef, useState } from "react";

/** Widths in rem. */
export const minWidth = 12;
export const maxWidth = 28;
export const defaultWidth = 14;
/** One arrow key press. */
export const step = 1;
export const railWidth = 3.5;

export interface Layout {
  width: number;
  collapsed: boolean;
}

export const defaultLayout: Layout = { width: defaultWidth, collapsed: false };

const key = (user: string) => `mail-archive.sidebar-layout.${user}`;

/**
 * Clamps to the bounds and rounds to 0.25 rem; anything not finite is the
 * default. `max` is a lower upper bound (the cap), itself a multiple of 0.25.
 */
export function clampWidth(n: number, max = maxWidth): number {
  if (!Number.isFinite(n)) n = defaultWidth;
  return Math.round(Math.min(max, Math.max(minWidth, n)) * 4) / 4;
}

/** What the lg grid gives the list column at most, and what the reader keeps at least (layout.css). */
const listWidth = 26;
const readerWidth = 23;

/**
 * The widest usable sidebar for a mail view this wide, both in rem (#310).
 * Widening the sidebar never makes the reader narrower than the default
 * width makes it at the narrowest desktop window: 14 rem at a 66 rem
 * window, the full 28 rem from 80 rem. Rounded down to 0.25 rem, so a
 * clamped width cannot round past it. Not measured (0, or not finite):
 * the full maximum.
 */
export function sidebarCap(mailView: number): number {
  if (!Number.isFinite(mailView) || mailView <= 0) return maxWidth;
  return Math.floor(Math.min(maxWidth, Math.max(minWidth, mailView - listWidth - readerWidth)) * 4) / 4;
}

/**
 * The cap for the mail view that the returned callback ref is attached to,
 * from its width (ResizeObserver) in rem of the root font size. The mail
 * view mounts and unmounts with the page (App), so the observer follows the
 * element, not the first render. Without ResizeObserver: the full maximum.
 */
export function useSidebarCap(): [cap: number, ref: (el: HTMLElement | null) => void] {
  const [cap, setCap] = useState(maxWidth);
  const observer = useRef<ResizeObserver | null>(null);
  const ref = useCallback((el: HTMLElement | null) => {
    observer.current?.disconnect();
    observer.current = null;
    if (!el || typeof ResizeObserver === "undefined") return;
    const measure = () => {
      const rem = parseFloat(getComputedStyle(document.documentElement).fontSize) || 16;
      setCap(sidebarCap(el.getBoundingClientRect().width / rem));
    };
    observer.current = new ResizeObserver(measure);
    observer.current.observe(el);
    // Before the first paint: the stored width may be wider than the cap.
    measure();
  }, []);
  return [cap, ref];
}

export function loadLayout(user: string): Layout {
  try {
    const v: unknown = JSON.parse(localStorage.getItem(key(user)) ?? "null");
    if (!v || typeof v !== "object" || !("v" in v) || v.v !== 1) return { ...defaultLayout };
    const width = "width" in v && typeof v.width === "number" ? clampWidth(v.width) : defaultWidth;
    const collapsed = "collapsed" in v && typeof v.collapsed === "boolean" ? v.collapsed : false;
    return { width, collapsed };
  } catch {
    return { ...defaultLayout }; // invalid or blocked storage
  }
}

export function saveLayout(user: string, l: Layout) {
  try {
    if (l.width === defaultWidth && !l.collapsed) localStorage.removeItem(key(user));
    else localStorage.setItem(key(user), JSON.stringify({ v: 1, width: l.width, collapsed: l.collapsed }));
  } catch {
    // not persisted; still applied for this page
  }
}

export interface SidebarLayout extends Layout {
  /** The usable maximum (the cap); `width` is never above it. */
  max: number;
  /** Changes the width without saving it (while dragging). */
  setWidth: (n: number) => void;
  /** Changes the width and saves it. */
  commitWidth: (n: number) => void;
  resetWidth: () => void;
  setCollapsed: (collapsed: boolean) => void;
}

/**
 * Read synchronously on the first render, so the first paint has the stored
 * layout. `cap` bounds the width that is used and saved by a user action;
 * the stored preference itself stays, so a narrower window does not
 * overwrite it and a wider one brings it back.
 */
export function useSidebarLayout(user: string, cap = maxWidth): SidebarLayout {
  const [layout, setLayout] = useState(() => loadLayout(user));
  const latest = useRef(layout);
  const apply = useCallback(
    (change: Partial<Layout>, persist: boolean) => {
      const next = { ...latest.current, ...change };
      latest.current = next;
      setLayout(next);
      if (persist) saveLayout(user, next);
    },
    [user],
  );
  const setWidth = useCallback((n: number) => apply({ width: clampWidth(n, cap) }, false), [apply, cap]);
  const commitWidth = useCallback((n: number) => apply({ width: clampWidth(n, cap) }, true), [apply, cap]);
  const resetWidth = useCallback(() => apply({ width: defaultWidth }, true), [apply]);
  const setCollapsed = useCallback((collapsed: boolean) => apply({ collapsed }, true), [apply]);
  return { ...layout, width: clampWidth(layout.width, cap), max: cap, setWidth, commitWidth, resetWidth, setCollapsed };
}
