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

/** Clamps to the bounds and rounds to 0.25 rem; anything not finite is the default. */
export function clampWidth(n: number): number {
  if (!Number.isFinite(n)) return defaultWidth;
  return Math.round(Math.min(maxWidth, Math.max(minWidth, n)) * 4) / 4;
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
  /** Changes the width without saving it (while dragging). */
  setWidth: (n: number) => void;
  /** Changes the width and saves it. */
  commitWidth: (n: number) => void;
  resetWidth: () => void;
  setCollapsed: (collapsed: boolean) => void;
}

/** Read synchronously on the first render, so the first paint has the stored layout. */
export function useSidebarLayout(user: string): SidebarLayout {
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
  const setWidth = useCallback((n: number) => apply({ width: clampWidth(n) }, false), [apply]);
  const commitWidth = useCallback((n: number) => apply({ width: clampWidth(n) }, true), [apply]);
  const resetWidth = useCallback(() => apply({ width: defaultWidth }, true), [apply]);
  const setCollapsed = useCallback((collapsed: boolean) => apply({ collapsed }, true), [apply]);
  return { ...layout, setWidth, commitWidth, resetWidth, setCollapsed };
}
