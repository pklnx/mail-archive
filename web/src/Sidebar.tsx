import { useEffect, useId, useRef } from "react";
import { t } from "./i18n";
import type { AccountsState } from "./useAccounts";
import type { ViewState } from "./urlState";
import type { Account, User } from "./api";
import { buildFolderTree, subtreeMessages, type FolderNode } from "./folderTree";
import type { CollapsedState } from "./sidebarState";

interface Props {
  state: ViewState;
  accounts: AccountsState;
  select: (account: string, folder: string) => void;
  /** Lists the messages no longer on any server. */
  selectGone: () => void;
  /** Opens a page: "accounts", "users", "retention", "log" or "profile". */
  show: (view: string) => void;
  user: User;
  logout: () => void;
  /** Which accounts and folders are collapsed. */
  collapsed: CollapsedState;
}

const item = "flex w-full items-center justify-between gap-2 rounded-md px-2 py-1 text-left text-sm";
const active = "bg-blue-600 text-white";
const idle = "hover:bg-zinc-200 dark:hover:bg-zinc-800";
/** A collapsed node that contains the selection. */
const within = "bg-blue-100 dark:bg-blue-950";
/** Deeper levels are not indented further, so names keep some room. */
const maxIndent = 6;

function Chevron({ open }: { open: boolean }) {
  return (
    <svg aria-hidden width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className={open ? "" : "-rotate-90"}>
      <path d="M6 9l6 6 6-6" />
    </svg>
  );
}

const toggleClass = "flex size-6 shrink-0 items-center justify-center rounded text-zinc-500 hover:bg-zinc-200 hover:text-zinc-800 dark:hover:bg-zinc-800 dark:hover:text-zinc-100";

export function Sidebar({ state, accounts, select, selectGone, show, user, logout, collapsed }: Props) {
  const { data, error } = accounts;
  const mail = state.view === "" && state.gone !== "1";
  const all = mail && !state.account && !state.folder;
  const gone = data?.accounts.reduce((n, a) => n + a.goneMessages, 0) ?? 0;
  const goneOn = state.view === "" && state.gone === "1";
  const baseId = useId();

  // The selected folder stays visible: its path is expanded once when the
  // selection changes, or when the folder first shows up in the data. Not
  // on every poll, so the user can still collapse a parent of it.
  const { reveal } = collapsed;
  const revealed = useRef("");
  useEffect(() => {
    if (!state.account || !state.folder || !data) return;
    const selection = `${state.account}\0${state.folder}`;
    if (revealed.current === selection) return;
    if (!data.accounts.some((a) => a.name === state.account && a.folders.some((f) => f.name === state.folder))) return;
    revealed.current = selection;
    reveal(state.account, state.folder);
  }, [state.account, state.folder, data, reveal]);
  return (
    <nav aria-label={t.accountsAndFolders} className="flex h-full flex-col gap-3 overflow-y-auto p-3">
      <button type="button" className={`${item} font-medium ${all ? active : idle}`} onClick={() => select("", "")}>
        {t.allMail}
      </button>
      {(gone > 0 || goneOn) && (
        <button
          type="button"
          className={`${item} -mt-2 ${goneOn ? active : idle}`}
          title={t.onlyInArchiveHint}
          onClick={selectGone}
        >
          <span className="truncate">{t.onlyInArchive}</span>
          <span className={`text-xs tabular-nums ${goneOn ? "" : "text-zinc-500"}`}>{gone}</span>
        </button>
      )}
      {data && data.accounts.some((a) => a.folders.length > 0) && (
        <div className="-mt-2 flex gap-3 px-2 text-xs">
          <button type="button" className="text-blue-600 hover:underline disabled:text-zinc-400 disabled:no-underline dark:text-blue-400 dark:disabled:text-zinc-600" disabled={!collapsed.canCollapse} onClick={collapsed.collapseAll}>
            {t.collapseAll}
          </button>
          <button type="button" className="text-blue-600 hover:underline disabled:text-zinc-400 disabled:no-underline dark:text-blue-400 dark:disabled:text-zinc-600" disabled={!collapsed.canExpand} onClick={collapsed.expandAll}>
            {t.expandAll}
          </button>
        </div>
      )}
      {!data && !error && <p className="px-2 text-sm text-zinc-500">{t.loading}</p>}
      {error && !data && <p className="px-2 text-sm text-red-600">{t.loadAccountsFailed(error)}</p>}
      {data && data.accounts.length === 0 && <p className="px-2 text-sm text-zinc-500">{t.noAccounts}</p>}
      {data?.accounts.map((a, ai) => {
        const open = !collapsed.isCollapsed(a.name, "");
        const listId = `${baseId}-${ai}`;
        const selected = mail && state.account === a.name;
        return (
          <div key={a.name}>
            <div className="flex items-center gap-0.5">
              {a.folders.length > 0 ? (
                <button
                  type="button"
                  className={toggleClass}
                  aria-expanded={open}
                  aria-controls={listId}
                  aria-label={t.foldersIn(a.name)}
                  title={t.foldersIn(a.name)}
                  onClick={() => collapsed.toggle(a.name, "")}
                >
                  <Chevron open={open} />
                </button>
              ) : (
                <span className="size-6 shrink-0" />
              )}
              <button
                type="button"
                className={`${item} min-w-0 font-semibold ${selected && !state.folder ? active : selected && !open ? within : idle}`}
                onClick={() => select(a.name, "")}
              >
                <span className="truncate">{a.name}</span>
                <AccountState a={a} />
              </button>
            </div>
            {a.folders.length > 0 && (
              <FolderList
                id={listId}
                hidden={!open}
                nodes={buildFolderTree(a.folders)}
                account={a.name}
                depth={0}
                selected={selected ? state.folder : null}
                select={select}
                collapsed={collapsed}
              />
            )}
          </div>
        );
      })}
      <button
        type="button"
        className={`${item} mt-auto ${state.view === "accounts" ? active : idle}`}
        aria-current={state.view === "accounts" ? "page" : undefined}
        onClick={() => show("accounts")}
      >
        <span className="flex items-center gap-2">
          <svg aria-hidden width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
            <circle cx="12" cy="12" r="3" />
            <path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z" />
          </svg>
          {t.manageAccounts}
        </span>
      </button>
      {user.admin && (
        <button
          type="button"
          className={`${item} ${state.view === "users" ? active : idle}`}
          aria-current={state.view === "users" ? "page" : undefined}
          onClick={() => show("users")}
        >
          <span className="flex items-center gap-2">
            <svg aria-hidden width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
              <circle cx="9" cy="8" r="3.5" />
              <path d="M2.5 20a6.5 6.5 0 0 1 13 0M16 4.5a3.5 3.5 0 0 1 0 7M18.5 14a6 6 0 0 1 3 6" />
            </svg>
            {t.users}
          </span>
        </button>
      )}
      {user.admin && (
        <button
          type="button"
          className={`${item} ${state.view === "retention" ? active : idle}`}
          aria-current={state.view === "retention" ? "page" : undefined}
          onClick={() => show("retention")}
        >
          <span className="flex items-center gap-2">
            <svg aria-hidden width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
              <rect x="3" y="4" width="18" height="5" rx="1" />
              <path d="M5 9v10a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V9M10 13h4" />
            </svg>
            {t.retention}
          </span>
        </button>
      )}
      <button
        type="button"
        className={`${item} ${state.view === "log" ? active : idle}`}
        aria-current={state.view === "log" ? "page" : undefined}
        onClick={() => show("log")}
      >
        <span className="flex items-center gap-2">
          <svg aria-hidden width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
            <path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z" />
            <path d="M14 3v6h6M8 13h8M8 17h5" />
          </svg>
          {t.log}
        </span>
      </button>
      <div className="flex items-center justify-between gap-2 border-t border-zinc-200 px-2 pt-2 text-sm dark:border-zinc-800">
        <button
          type="button"
          className={`truncate hover:underline ${state.view === "profile" ? "font-semibold" : "text-zinc-500"}`}
          title={`${t.loggedInAs(user.name)} · ${t.profile}`}
          aria-current={state.view === "profile" ? "page" : undefined}
          onClick={() => show("profile")}
        >
          {user.name}
        </button>
        <button type="button" className="shrink-0 text-blue-600 hover:underline dark:text-blue-400" onClick={logout}>
          {t.logOut}
        </button>
      </div>
    </nav>
  );
}

function AccountState({ a }: { a: Account }) {
  if (a.sync.state !== "idle") {
    return (
      <span className="text-xs font-normal opacity-70" title={a.kind !== "imap" ? t.importing : t.syncing}>
        <span aria-hidden className="inline-block animate-spin">↻</span>
        <span className="sr-only">{a.kind !== "imap" ? t.importing : t.syncing}</span>
      </span>
    );
  }
  if (a.removed) return <span className="text-xs font-normal opacity-70">{t.removed}</span>;
  if (a.kind === "import") return <span className="text-xs font-normal opacity-70">{t.imported}</span>;
  if (a.kind === "smtp" && a.enabled) {
    return (
      <span className="text-xs font-normal opacity-70" title={t.sourceCompleteHint}>
        {t.receiving}
      </span>
    );
  }
  return !a.enabled ? <span className="text-xs font-normal opacity-70">{t.disabled}</span> : null;
}

interface ListProps {
  id: string;
  hidden: boolean;
  nodes: FolderNode[];
  account: string;
  depth: number;
  /** The selected folder in this account, or null. */
  selected: string | null;
  select: (account: string, folder: string) => void;
  collapsed: CollapsedState;
}

/** The folders of one level. Collapsed lists stay in the page, hidden, so aria-controls always has a target. */
function FolderList({ id, hidden, nodes, depth, ...rest }: ListProps) {
  return (
    <ul id={id} hidden={hidden} className={`mt-0.5 flex flex-col gap-0.5 ${depth > 0 && depth <= maxIndent ? "ml-2" : ""}`}>
      {nodes.map((n, i) => (
        <FolderItem key={n.path} node={n} id={`${id}-${i}`} depth={depth} {...rest} />
      ))}
    </ul>
  );
}

function FolderItem({ node, id, account, depth, selected, select, collapsed }: Omit<ListProps, "nodes" | "hidden"> & { node: FolderNode }) {
  const parent = node.children.length > 0;
  const open = !parent || !collapsed.isCollapsed(account, node.path);
  const f = node.folder;
  const on = f !== null && selected === f.name;
  // The selection inside a collapsed node: show where it is.
  const inside = !open && selected !== null && contains(node, selected);
  const count = open ? (f?.messages ?? 0) : subtreeMessages(node);
  const countLabel = open ? undefined : t.subtreeCount(subtreeMessages(node));
  const chevron = (
    <button
      type="button"
      className={toggleClass}
      aria-expanded={open}
      aria-controls={`${id}-c`}
      aria-label={t.foldersIn(node.path)}
      title={t.foldersIn(node.path)}
      onClick={() => collapsed.toggle(account, node.path)}
    >
      <Chevron open={open} />
    </button>
  );
  return (
    <li>
      {f ? (
        <div className="flex items-center gap-0.5">
          {parent ? chevron : <span className="size-6 shrink-0" />}
          <button
            type="button"
            className={`${item} min-w-0 ${on ? active : inside ? within : idle}`}
            title={f.name}
            onClick={() => select(account, f.name)}
          >
            <span className="truncate">{node.label}</span>
            <span className={`text-xs tabular-nums ${on ? "" : "text-zinc-500"} ${open ? "" : "italic"}`} title={countLabel}>
              {count}
            </span>
          </button>
        </div>
      ) : (
        // Only a path part: no messages of its own, nothing to select. The
        // whole row toggles.
        <button
          type="button"
          className={`flex w-full items-center gap-0.5 rounded-md py-1 pr-2 text-left text-sm text-zinc-600 dark:text-zinc-400 ${inside ? within : idle}`}
          aria-expanded={open}
          aria-controls={`${id}-c`}
          title={node.path}
          onClick={() => collapsed.toggle(account, node.path)}
        >
          <span className="flex size-6 shrink-0 items-center justify-center text-zinc-500">
            <Chevron open={open} />
          </span>
          <span className="min-w-0 flex-1 truncate">{node.label}</span>
          {!open && (
            <span className="text-xs tabular-nums text-zinc-500 italic" title={countLabel}>
              {count}
            </span>
          )}
        </button>
      )}
      {parent && (
        <FolderList
          id={`${id}-c`}
          hidden={!open}
          nodes={node.children}
          account={account}
          depth={depth + 1}
          selected={selected}
          select={select}
          collapsed={collapsed}
        />
      )}
    </li>
  );
}

function contains(node: FolderNode, folder: string): boolean {
  return node.children.some((c) => c.folder?.name === folder || contains(c, folder));
}
