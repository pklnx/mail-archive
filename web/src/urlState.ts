import { useCallback, useSyncExternalStore } from "react";

// The UI state lives in the URL query (?q=&account=&folder=&group=&gone=&m=&view=,
// the page keys tab=&id=&add=&import= of the settings views, and the Log's
// filters since=&until=&action=&user=&actor=), so the back button, reloads and
// bookmarks work without a router library.

/** The action groups of the Log's filter, as the API names them. */
export const auditGroups = ["login", "search", "read", "export", "accounts", "users", "profile", "api_keys", "sync", "retention"] as const;
/** The actors the Log's Who filter offers besides users. */
export const auditActors = ["cli", "api_key"] as const;

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
  /**
   * With view=log: From as a local day YYYY-MM-DD; "" for no From (since=),
   * null without the key: the default, 30 days back, so a bookmark keeps a
   * moving window (#376).
   */
  since: string | null;
  /** With view=log: To as a local day YYYY-MM-DD, or "". */
  until: string;
  /** With view=log: the action groups (repeated action=), any of them; none is all. */
  action: string[];
  /** With view=log: the user names (repeated user=); entries of any user or actor match. */
  user: string[];
  /** With view=log: the actors cli and api_key (repeated actor=). */
  actor: string[];
}

const keys: (keyof ViewState)[] = ["q", "account", "folder", "group", "gone", "m", "view", "tab", "id", "add", "import", "since", "until", "action", "user", "actor"];

/**
 * The page keys and the views they belong to. Other views drop them, so
 * returning to a view shows its list.
 */
const pageKeys: Partial<Record<keyof ViewState, readonly string[]>> = {
  tab: ["retention"],
  id: ["accounts", "users", "retention"],
  add: ["accounts", "retention"],
  import: ["accounts"],
  since: ["log"],
  until: ["log"],
  action: ["log"],
  user: ["log"],
  actor: ["log"],
};
const inView = (k: keyof ViewState, view: string) => pageKeys[k]?.includes(view) ?? true;
const oneOf = (v: string | null, allowed: readonly string[]) => (v !== null && allowed.includes(v) ? v : "");
const isDay = (v: string | null): v is string => v !== null && /^\d{4}-\d{2}-\d{2}$/.test(v);
/** The values of a repeated key, without empty ones and repeats. */
const allOf = (p: URLSearchParams, k: string) => [...new Set(p.getAll(k).filter((v) => v !== ""))];
/**
 * The allowed values of a repeated key, in the order of allowed: a
 * hand-edited link cannot make the API answer 400.
 */
const someOf = (p: URLSearchParams, k: string, allowed: readonly string[]) => {
  const v = p.getAll(k);
  return allowed.filter((a) => v.includes(a));
};

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
    ...logState(view === "log" ? p : new URLSearchParams()),
  };
}

/** The Log's filters; a value that is not a day counts as not given. */
function logState(p: URLSearchParams): Pick<ViewState, "since" | "until" | "action" | "user" | "actor"> {
  const since = p.get("since");
  const until = p.get("until");
  return {
    since: since === "" || isDay(since) ? since : null,
    until: isDay(until) ? until : "",
    action: someOf(p, "action", auditGroups),
    user: allOf(p, "user"),
    actor: someOf(p, "actor", auditActors),
  };
}

export function formatState(s: ViewState): string {
  const p = new URLSearchParams();
  for (const k of keys) {
    if (!inView(k, s.view)) continue;
    const v = s[k];
    if (Array.isArray(v)) for (const x of v) p.append(k, x);
    // since= without a day is no From; without the key, the default.
    else if (k === "since" ? v !== null : v) p.set(k, v ?? "");
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
