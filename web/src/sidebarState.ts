// Which accounts and folders are collapsed in the sidebar, per user and
// browser. Only collapsed nodes are stored, so the default is everything
// expanded. A convenience, not data: storage may be blocked.
import { useCallback, useEffect, useRef, useState } from "react";
import type { AccountsResponse } from "./api";
import { ancestorPaths, buildFolderTree, nodePaths, parentPaths } from "./folderTree";

/** Collapsed node paths per account name; "" is the account itself. */
export type Collapsed = Record<string, string[]>;

const key = (user: string) => `mail-archive.sidebar.${user}`;

export function loadCollapsed(user: string): Collapsed {
  try {
    const v: unknown = JSON.parse(localStorage.getItem(key(user)) ?? "null");
    if (!v || typeof v !== "object" || !("v" in v) || v.v !== 1 || !("collapsed" in v)) return {};
    const c = v.collapsed;
    if (!c || typeof c !== "object") return {};
    const out: Collapsed = {};
    for (const [account, paths] of Object.entries(c)) {
      if (Array.isArray(paths)) out[account] = paths.filter((p): p is string => typeof p === "string");
    }
    return out;
  } catch {
    return {}; // invalid or blocked storage
  }
}

export function saveCollapsed(user: string, c: Collapsed) {
  try {
    if (Object.keys(c).length === 0) localStorage.removeItem(key(user));
    else localStorage.setItem(key(user), JSON.stringify({ v: 1, collapsed: c }));
  } catch {
    // not persisted; still applied for this page
  }
}

function withAccount(c: Collapsed, account: string, paths: string[]): Collapsed {
  const out = { ...c };
  if (paths.length) out[account] = paths;
  else delete out[account];
  return out;
}

export function isCollapsed(c: Collapsed, account: string, path: string): boolean {
  return c[account]?.includes(path) ?? false;
}

export function toggle(c: Collapsed, account: string, path: string): Collapsed {
  const paths = c[account] ?? [];
  return withAccount(c, account, paths.includes(path) ? paths.filter((p) => p !== path) : [...paths, path]);
}

/** Expands the account and the nodes above a folder. An empty folder changes nothing. */
export function reveal(c: Collapsed, data: AccountsResponse, account: string, folder: string): Collapsed {
  if (!folder || !c[account]) return c;
  const a = data.accounts.find((x) => x.name === account);
  if (!a) return c;
  const open = new Set(["", ...ancestorPaths(a.folders, folder)]);
  const paths = c[account].filter((p) => !open.has(p));
  return paths.length === c[account].length ? c : withAccount(c, account, paths);
}

/** Moves an account's state to its new name. */
export function rename(c: Collapsed, from: string, to: string): Collapsed {
  if (!c[from] || from === to) return c;
  const out = { ...c, [to]: c[from] };
  delete out[from];
  return out;
}

/** Drops accounts and nodes that no longer exist; c itself when nothing changes. */
export function prune(c: Collapsed, data: AccountsResponse): Collapsed {
  const out: Collapsed = {};
  let changed = false;
  for (const [account, paths] of Object.entries(c)) {
    const a = data.accounts.find((x) => x.name === account);
    const known = new Set(a ? ["", ...nodePaths(buildFolderTree(a.folders))] : []);
    const kept = paths.filter((p) => known.has(p));
    if (kept.length !== paths.length) changed = true;
    if (kept.length) out[account] = kept;
  }
  return changed ? out : c;
}

/** Every account with folders and every folder node with children. */
export function collapseAll(data: AccountsResponse): Collapsed {
  const out: Collapsed = {};
  for (const a of data.accounts) {
    if (a.folders.length) out[a.name] = ["", ...parentPaths(buildFolderTree(a.folders))];
  }
  return out;
}

export interface CollapsedState {
  isCollapsed: (account: string, path: string) => boolean;
  toggle: (account: string, path: string) => void;
  collapseAll: () => void;
  expandAll: () => void;
  /** Whether collapse all or expand all would change anything. */
  canCollapse: boolean;
  canExpand: boolean;
  reveal: (account: string, folder: string) => void;
  rename: (from: string, to: string) => void;
}

/** The sidebar's collapsed nodes for user, pruned after each load of data. */
export function useCollapsed(user: string, data: AccountsResponse | null): CollapsedState {
  const [c, setC] = useState<Collapsed>(() => loadCollapsed(user));
  const saved = useRef(JSON.stringify(c));
  const dataRef = useRef(data);
  dataRef.current = data;

  useEffect(() => {
    if (data) setC((prev) => prune(prev, data));
  }, [data]);

  useEffect(() => {
    const v = JSON.stringify(c);
    if (v === saved.current) return;
    saved.current = v;
    saveCollapsed(user, c);
  }, [c, user]);

  const all = data ? collapseAll(data) : {};
  return {
    isCollapsed: (account, path) => isCollapsed(c, account, path),
    toggle: useCallback((account: string, path: string) => setC((prev) => toggle(prev, account, path)), []),
    collapseAll: useCallback(() => {
      if (dataRef.current) setC(collapseAll(dataRef.current));
    }, []),
    expandAll: useCallback(() => setC({}), []),
    canCollapse: Object.entries(all).some(([a, paths]) => paths.some((p) => !isCollapsed(c, a, p))),
    canExpand: Object.keys(c).length > 0,
    reveal: useCallback((account: string, folder: string) => {
      const d = dataRef.current;
      if (d) setC((prev) => reveal(prev, d, account, folder));
    }, []),
    rename: useCallback((from: string, to: string) => setC((prev) => rename(prev, from, to)), []),
  };
}
