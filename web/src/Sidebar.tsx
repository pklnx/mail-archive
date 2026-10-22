import { useEffect, useId, useRef, useState, type KeyboardEvent, type PointerEvent, type Ref } from "react";
import { t } from "./i18n";
import { railLabel } from "./accountName";
import { CollapseIcon, ExpandIcon, InboxIcon, iconButton } from "./icons";
import { maxWidth, minWidth, step, type SidebarLayout } from "./sidebarLayout";
import { SidebarSkeleton } from "./Skeleton";
import { primary } from "./formStyles";
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
  /** Collapses the sidebar to the rail (lg and up); no button without it. */
  onCollapse?: () => void;
  collapseRef?: Ref<HTMLButtonElement>;
  /** The id of the element the collapse button controls. */
  controls?: string;
}

const item = "flex w-full items-center justify-between gap-2 rounded-md px-2 py-1 text-left text-sm pointer-coarse:min-h-11";
const active = "bg-accent-soft font-semibold text-accent-soft-ink";
const idle = "hover:bg-surface-hover";
/** A collapsed node that contains the selection. */
const within = "text-accent-soft-ink";
/** A heading over a group of entries. */
const section = "px-2 pt-2 text-xs font-semibold tracking-wide text-ink-muted uppercase";
/** Deeper levels are not indented further, so names keep some room. */
const maxIndent = 6;

function Chevron({ open }: { open: boolean }) {
  return (
    <svg aria-hidden width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className={open ? "" : "-rotate-90"}>
      <path d="M6 9l6 6 6-6" />
    </svg>
  );
}

const toggleClass = "relative flex size-6 shrink-0 items-center justify-center rounded text-ink-muted hover:bg-surface-hover hover:text-ink pointer-coarse:after:absolute pointer-coarse:after:-inset-2.5";

export function Sidebar({ state, accounts, select, selectGone, show, user, logout, collapsed, onCollapse, collapseRef, controls }: Props) {
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
      <div className="flex items-center gap-1">
        <button type="button" data-drawer-focus className={`${item} font-medium ${all ? active : idle}`} onClick={() => select("", "")}>
          {t.allMail}
        </button>
        {onCollapse && (
          // Only where the sidebar is always visible; the drawer closes instead.
          <div className="hidden lg:block">
            <button
              ref={collapseRef}
              type="button"
              className={iconButton}
              aria-label={t.collapseSidebar}
              title={t.collapseSidebar}
              aria-expanded="true"
              aria-controls={controls}
              onClick={onCollapse}
            >
              <CollapseIcon />
            </button>
          </div>
        )}
      </div>
      {(gone > 0 || goneOn) && (
        <button
          type="button"
          className={`${item} -mt-2 ${goneOn ? active : idle}`}
          title={t.onlyInArchiveHint}
          onClick={selectGone}
        >
          <span className="truncate">{t.onlyInArchive}</span>
          <span className={`text-xs tabular-nums ${goneOn ? "" : "text-ink-muted"}`}>{gone}</span>
        </button>
      )}
      {data && data.accounts.length > 0 && <h2 className={section}>{t.sectionAccounts}</h2>}
      {data && data.accounts.some((a) => a.folders.length > 0) && (
        <div className="-mt-2 flex gap-3 px-2 text-xs">
          <button type="button" className="text-accent-ink hover:underline disabled:text-ink-muted disabled:no-underline pointer-coarse:min-h-11" disabled={!collapsed.canCollapse} onClick={collapsed.collapseAll}>
            {t.collapseAll}
          </button>
          <button type="button" className="text-accent-ink hover:underline disabled:text-ink-muted disabled:no-underline pointer-coarse:min-h-11" disabled={!collapsed.canExpand} onClick={collapsed.expandAll}>
            {t.expandAll}
          </button>
        </div>
      )}
      {!data && !error && <SidebarSkeleton />}
      {error && !data && <p className="px-2 text-sm text-danger">{t.loadAccountsFailed(error)}</p>}
      {data && data.accounts.length === 0 && (
        <div className="flex flex-col items-start gap-2 px-2 text-sm text-ink-muted">
          <p>{t.noAccounts}</p>
          <button type="button" className={primary} onClick={() => show("accounts")}>
            {t.addAccount}
          </button>
        </div>
      )}
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
                nodes={buildFolderTree(a.folders, a.kind)}
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
      <h2 className={`${section} mt-auto`}>{t.sectionManage}</h2>
      <button
        type="button"
        className={`${item} -mt-2 ${state.view === "accounts" ? active : idle}`}
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
      <div className="flex items-center justify-between gap-2 border-t border-line px-2 pt-2 text-sm">
        <button
          type="button"
          className={`truncate hover:underline pointer-coarse:min-h-11 ${state.view === "profile" ? "font-semibold" : "text-ink-muted"}`}
          title={`${t.loggedInAs(user.name)} · ${t.profile}`}
          aria-current={state.view === "profile" ? "page" : undefined}
          onClick={() => show("profile")}
        >
          {user.name}
        </button>
        <button type="button" className="shrink-0 text-accent-ink hover:underline pointer-coarse:min-h-11" onClick={logout}>
          {t.logOut}
        </button>
      </div>
    </nav>
  );
}

/**
 * What the drawer focuses when it opens below lg: "All mail" of the full
 * sidebar. Not simply the first button: the rail and the collapse button
 * come first in the page but are not displayed below lg, and a hidden
 * button cannot take focus.
 */
export function drawerFocusTarget(root: ParentNode | null | undefined): HTMLElement | null {
  return root?.querySelector<HTMLElement>("[data-drawer-focus]") ?? null;
}

type PanelProps = Omit<Props, "onCollapse" | "collapseRef" | "controls"> & {
  layout: SidebarLayout;
  /** The id of the aside, for aria-controls. */
  controls: string;
};

/**
 * The content of the sidebar's aside. From lg up: the full sidebar with its
 * resize handle, or the rail when collapsed. Below lg the aside is a drawer
 * that always shows the full sidebar. A CSS switch only, so the first paint
 * is final.
 */
export function SidebarPanel({ layout, controls, ...props }: PanelProps) {
  const collapseButton = useRef<HTMLButtonElement>(null);
  const expandButton = useRef<HTMLButtonElement>(null);
  // Focus follows a toggle by the user, not the stored state on load.
  const toggled = useRef(false);
  const { collapsed, setCollapsed } = layout;
  useEffect(() => {
    if (!toggled.current) return;
    toggled.current = false;
    (collapsed ? expandButton : collapseButton).current?.focus();
  }, [collapsed]);
  const toggle = (c: boolean) => {
    toggled.current = true;
    setCollapsed(c);
  };
  if (collapsed) {
    return (
      <>
        <SidebarRail state={props.state} accounts={props.accounts} select={props.select} controls={controls} expandRef={expandButton} onExpand={() => toggle(false)} />
        <div className="h-full lg:hidden">
          <Sidebar {...props} />
        </div>
      </>
    );
  }
  return (
    <>
      <Sidebar {...props} onCollapse={() => toggle(true)} collapseRef={collapseButton} controls={controls} />
      <SidebarResizer layout={layout} controls={controls} onCollapse={() => toggle(true)} />
    </>
  );
}

/** The drag handle on the sidebar's right edge (window-splitter pattern). */
function SidebarResizer({ layout, controls, onCollapse }: { layout: SidebarLayout; controls: string; onCollapse: () => void }) {
  const { width, setWidth, commitWidth, resetWidth } = layout;
  const [dragging, setDragging] = useState(false);
  const stop = useRef<(() => void) | null>(null);
  useEffect(() => () => stop.current?.(), []);

  // Pointer Events cover mouse and touch; the listeners are on window, so
  // the drag goes on when the pointer leaves the thin handle.
  const onPointerDown = (e: PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return; // not with a right or middle click
    e.preventDefault(); // no text selection, no focus jump
    stop.current?.();
    const startX = e.clientX;
    const startWidth = width;
    const rem = parseFloat(getComputedStyle(document.documentElement).fontSize) || 16;
    let last = startWidth;
    const body = document.body.style;
    const userSelect = body.userSelect;
    body.userSelect = "none";
    setDragging(true);
    const move = (ev: globalThis.PointerEvent) => {
      last = startWidth + (ev.clientX - startX) / rem;
      setWidth(last);
    };
    const end = (commit: boolean) => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      window.removeEventListener("pointercancel", up);
      body.userSelect = userSelect;
      stop.current = null;
      if (commit) {
        setDragging(false);
        commitWidth(last);
      }
    };
    const up = () => end(true);
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
    window.addEventListener("pointercancel", up);
    stop.current = () => end(false);
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const next =
      e.key === "ArrowRight" ? width + step : e.key === "ArrowLeft" ? width - step : e.key === "Home" ? minWidth : e.key === "End" ? maxWidth : null;
    if (next !== null) {
      e.preventDefault();
      commitWidth(next);
    } else if (e.key === "Enter") {
      e.preventDefault();
      onCollapse();
    }
  };

  return (
    <div
      role="separator"
      aria-orientation="vertical"
      aria-label={t.resizeSidebar}
      aria-controls={controls}
      aria-valuemin={minWidth}
      aria-valuemax={maxWidth}
      aria-valuenow={width}
      aria-valuetext={t.sidebarWidth(width)}
      tabIndex={0}
      className="group absolute inset-y-0 -right-1 z-10 hidden w-2 cursor-col-resize touch-none outline-none select-none lg:block pointer-coarse:-right-3 pointer-coarse:w-6"
      onPointerDown={onPointerDown}
      onKeyDown={onKeyDown}
      onDoubleClick={resetWidth}
    >
      <div
        aria-hidden
        className={`mx-auto h-full group-hover:w-0.5 group-hover:bg-accent group-focus-visible:w-0.5 group-focus-visible:bg-focus ${dragging ? "w-0.5 bg-accent" : "w-px bg-transparent"}`}
      />
    </div>
  );
}

const railButton = "relative flex size-10 shrink-0 items-center justify-center rounded-md text-xs pointer-coarse:size-11";

interface RailProps {
  state: ViewState;
  accounts: AccountsState;
  select: (account: string, folder: string) => void;
  controls: string;
  expandRef: Ref<HTMLButtonElement>;
  onExpand: () => void;
}

/** The collapsed sidebar: a short label per account. The manage links are reached by expanding. */
function SidebarRail({ state, accounts, select, controls, expandRef, onExpand }: RailProps) {
  const mail = state.view === "" && state.gone !== "1";
  const all = mail && !state.account && !state.folder;
  return (
    <nav aria-label={t.accountsAndFolders} className="hidden h-full flex-col items-center gap-2 p-2 lg:flex pointer-coarse:p-1.5">
      <button
        ref={expandRef}
        type="button"
        className={iconButton}
        aria-label={t.expandSidebar}
        title={t.expandSidebar}
        aria-expanded="false"
        aria-controls={controls}
        onClick={onExpand}
      >
        <ExpandIcon />
      </button>
      <button
        type="button"
        className={`${railButton} ${all ? active : `text-ink-muted ${idle}`}`}
        aria-label={t.allMail}
        title={t.allMail}
        aria-current={all ? "true" : undefined}
        onClick={() => select("", "")}
      >
        <InboxIcon />
      </button>
      {/* Only the accounts scroll, so » stays reachable with many of them. */}
      <ul className="flex min-h-0 w-full flex-1 flex-col items-center gap-1 overflow-x-hidden overflow-y-auto py-0.5">
        {accounts.data?.accounts.map((a) => (
          <li key={a.name}>
            <RailAccount a={a} selected={mail && state.account === a.name} select={select} />
          </li>
        ))}
      </ul>
    </nav>
  );
}

/** The state texts of an account for the rail, in reading order. */
export function railState(a: Account): string[] {
  const out: string[] = [];
  if (a.sync.state !== "idle") out.push(a.kind !== "imap" ? t.importing : t.syncing);
  if (a.removed) out.push(t.removed);
  else if (!a.enabled) out.push(t.disabled);
  if (a.sync.health === "failing") out.push(t.railFailing);
  else if (a.sync.health === "stale") out.push(t.railStale);
  return out;
}

function RailAccount({ a, selected, select }: { a: Account; selected: boolean; select: (account: string, folder: string) => void }) {
  const label = [a.name, ...railState(a)].join(", ");
  const off = a.removed || !a.enabled;
  return (
    <button
      type="button"
      className={`${railButton} font-semibold ${selected ? active : idle} ${off ? "border border-dashed border-line-control opacity-60" : ""}`}
      aria-label={label}
      title={label}
      aria-current={selected ? "true" : undefined}
      onClick={() => select(a.name, "")}
    >
      <span aria-hidden className="whitespace-nowrap">
        {railLabel(a.name)}
      </span>
      {a.sync.state !== "idle" && (
        <span aria-hidden className="absolute -top-0.5 -right-0.5 inline-block animate-spin text-[10px] leading-none font-normal motion-reduce:animate-none">
          ↻
        </span>
      )}
      {a.sync.health !== "ok" && (
        <span aria-hidden className={`absolute right-0.5 bottom-0.5 size-2 rounded-full ${a.sync.health === "failing" ? "bg-danger" : "bg-warning"}`} />
      )}
    </button>
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
            <span className={`text-xs tabular-nums ${on ? "" : "text-ink-muted"} ${open ? "" : "italic"}`} title={countLabel}>
              {count}
            </span>
          </button>
        </div>
      ) : (
        // Only a path part: no messages of its own, nothing to select. The
        // whole row toggles.
        <button
          type="button"
          className={`flex w-full items-center gap-0.5 rounded-md py-1 pr-2 text-left text-sm pointer-coarse:min-h-11 ${inside ? within : `text-ink-muted ${idle}`}`}
          aria-expanded={open}
          aria-controls={`${id}-c`}
          title={node.path}
          onClick={() => collapsed.toggle(account, node.path)}
        >
          <span className="flex size-6 shrink-0 items-center justify-center text-ink-muted">
            <Chevron open={open} />
          </span>
          <span className="min-w-0 flex-1 truncate">{node.label}</span>
          {!open && (
            <span className="text-xs tabular-nums text-ink-muted italic" title={countLabel}>
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
