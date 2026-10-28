import { useCallback, useSyncExternalStore } from "react";

// The UI state lives in the URL query (?q=&account=&folder=&group=&gone=&m=&view=,
// and the page keys tab=&id=&add=&import= of the settings views), so the back
// button, reloads and bookmarks work without a router library.

export interface ViewState {
  q: string;
  account: string;
  folder: string;
  /** "1" lists one row per conversation. */
  group: string;
  /** "1" lists only messages no longer on any server. */
  gone: string;
  /** Open message id. */
  m: string;
  /** "accounts" shows account management instead of the mail columns. */
  view: string;
  /** With view=retention: "rules", "holds" or "runs"; empty is rules. */
  tab: string;
  /**
   * With view=accounts, users or retention: the entry whose detail page is
   * open. An old edit=NAME of Accounts reads as id=NAME.
   */
  id: string;
  /** With view=accounts: "1" opens the Add account page; with view=retention: "rule" or "hold". */
  add: string;
  /** With view=accounts: the name of the smtp account whose Import from IMAP page is open. */
  import: string;
}

const keys: (keyof ViewState)[] = ["q", "account", "folder", "group", "gone", "m", "view", "tab", "id", "add", "import"];

/**
 * The page keys and the views they belong to. Other views drop them, so
 * returning to a view shows its list.
 */
const pageKeys: Partial<Record<keyof ViewState, readonly string[]>> = {
  tab: ["retention"],
  id: ["accounts", "users", "retention"],
  add: ["accounts", "retention"],
  import: ["accounts"],
};
const inView = (k: keyof ViewState, view: string) => pageKeys[k]?.includes(view) ?? true;
const oneOf = (v: string | null, allowed: readonly string[]) => (v !== null && allowed.includes(v) ? v : "");

export function parseState(search: string): ViewState {
  const p = new URLSearchParams(search);
  const view = p.get("view") ?? "";
  const accounts = view === "accounts";
  return {
    q: p.get("q") ?? "",
    account: p.get("account") ?? "",
    folder: p.get("folder") ?? "",
    group: p.get("group") === "1" ? "1" : "",
    gone: p.get("gone") === "1" ? "1" : "",
    m: p.get("m") ?? "",
    view,
    tab: inView("tab", view) ? oneOf(p.get("tab"), ["rules", "holds", "runs"]) : "",
    id: inView("id", view) ? (p.get("id") ?? (accounts ? p.get("edit") : null) ?? "") : "",
    add: inView("add", view) ? oneOf(p.get("add"), accounts ? ["1"] : ["rule", "hold"]) : "",
    import: accounts ? (p.get("import") ?? "") : "",
  };
}

export function formatState(s: ViewState): string {
  const p = new URLSearchParams();
  for (const k of keys) {
    if (!inView(k, s.view)) continue;
    if (s[k]) p.set(k, s[k]);
  }
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
    const cur = parseState(window.location.search);
    // Another view starts on its list: the page keys stay behind.
    const base = patch.view !== undefined && patch.view !== cur.view ? { ...cur, tab: "", id: "", add: "", import: "" } : cur;
    const next = formatState({ ...base, ...patch });
    if (next === window.location.search) return;
    const url = window.location.pathname + next;
    if (opts?.replace) window.history.replaceState(null, "", url);
    else window.history.pushState(null, "", url);
    listeners.forEach((fn) => fn());
  }, []);
  return [state, update];
}
