import { useCallback, useSyncExternalStore } from "react";

// The UI state lives in the URL query (?q=&account=&folder=&m=&view=), so the back
// button, reloads and bookmarks work without a router library.

export interface ViewState {
  q: string;
  account: string;
  folder: string;
  /** Open message id. */
  m: string;
  /** "accounts" shows account management instead of the mail columns. */
  view: string;
}

const keys: (keyof ViewState)[] = ["q", "account", "folder", "m", "view"];

export function parseState(search: string): ViewState {
  const p = new URLSearchParams(search);
  return {
    q: p.get("q") ?? "",
    account: p.get("account") ?? "",
    folder: p.get("folder") ?? "",
    m: p.get("m") ?? "",
    view: p.get("view") ?? "",
  };
}

export function formatState(s: ViewState): string {
  const p = new URLSearchParams();
  for (const k of keys) if (s[k]) p.set(k, s[k]);
  const qs = p.toString();
  return qs ? `?${qs}` : "";
}

const listeners = new Set<() => void>();
function subscribe(fn: () => void) {
  listeners.add(fn);
  window.addEventListener("popstate", fn);
  return () => {
    listeners.delete(fn);
    window.removeEventListener("popstate", fn);
  };
}
const getSearch = () => window.location.search;

export function useViewState(): [ViewState, (patch: Partial<ViewState>, opts?: { replace?: boolean }) => void] {
  const search = useSyncExternalStore(subscribe, getSearch);
  const state = parseState(search);
  const update = useCallback((patch: Partial<ViewState>, opts?: { replace?: boolean }) => {
    const next = formatState({ ...parseState(window.location.search), ...patch });
    if (next === window.location.search) return;
    const url = window.location.pathname + next;
    if (opts?.replace) window.history.replaceState(null, "", url);
    else window.history.pushState(null, "", url);
    listeners.forEach((fn) => fn());
  }, []);
  return [state, update];
}
