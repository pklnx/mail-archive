// The mail view's folder column: accounts and their folders in Carbon's
// TreeView. From lg up it is a column that can be resized and collapsed to a
// rail of short account labels (#160); below lg it opens as an overlay from
// the mail toolbar's Folders button (App).
import { Fragment, useEffect, useId, useRef, useState, type KeyboardEvent, type PointerEvent, type Ref, type SyntheticEvent } from "react";
import { Button, FeatureFlags, IconButton, SkeletonText, TreeNode, TreeView } from "@carbon/react";
import { Close, SidePanelClose, SidePanelOpen, WarningFilled } from "@carbon/icons-react";
import { t } from "./i18n";
import { railLabel } from "./accountName";
import { maxWidth, minWidth, step, type SidebarLayout } from "./sidebarLayout";
import type { AccountsState } from "./useAccounts";
import type { ViewState } from "./urlState";
import type { Account } from "./api";
import { buildFolderTree, subtreeMessages, type FolderNode } from "./folderTree";
import type { CollapsedState } from "./sidebarState";

interface Props {
  state: ViewState;
  accounts: AccountsState;
  select: (account: string, folder: string) => void;
  /** Opens a page, here only "accounts" (no accounts yet). */
  show: (view: string) => void;
  /** Which accounts and folders are collapsed. */
  collapsed: CollapsedState;
  /** Collapses the column to the rail (lg and up); no button without it. */
  onCollapse?: () => void;
  collapseRef?: Ref<HTMLButtonElement>;
  /** The id of the element the collapse button controls. */
  controls?: string;
  /** Closes the overlay (below lg); no close button without it. */
  onClose?: () => void;
}

/** A DOM id from any text: its UTF-8 bytes in hex, so names with spaces or quotes stay valid in id references. */
function hex(s: string): string {
  return Array.from(new TextEncoder().encode(s), (b) => b.toString(16).padStart(2, "0")).join("");
}

/** Node ids: the account, its "All folders" node, and a folder path in it. */
export function nodeIds(base: string) {
  return {
    account: (a: string) => `${base}-a${hex(a)}`,
    all: (a: string) => `${base}-a${hex(a)}-all`,
    folder: (a: string, path: string) => `${base}-a${hex(a)}-f${hex(path)}`,
  };
}

/** A node's name and count; the space keeps them apart in the accessible name and in the tooltip. */
function Label({ name, count, title, countTitle }: { name: string; count?: number; title?: string; countTitle?: string }) {
  return (
    <span className="folder-tree__label" title={title}>
      <span className="folder-tree__name">{name}</span>
      {count !== undefined && " "}
      {count !== undefined && (
        <span className={`folder-tree__count${countTitle ? " folder-tree__count--subtree" : ""}`} title={countTitle}>
          {count}
        </span>
      )}
    </span>
  );
}

export function FolderTree({ state, accounts, select, show, collapsed, onCollapse, collapseRef, controls, onClose }: Props) {
  const { data, error } = accounts;
  const mail = state.view === "" && state.gone !== "1";
  const ids = nodeIds(useId());

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

  const selectedAccount = mail ? data?.accounts.find((a) => a.name === state.account) : undefined;
  const selected = !selectedAccount
    ? ""
    : state.folder
      ? ids.folder(selectedAccount.name, state.folder)
      : selectedAccount.folders.length
        ? ids.all(selectedAccount.name)
        : ids.account(selectedAccount.name);

  return (
    <nav aria-label={t.accountsAndFolders} className="folder-tree">
      <div className="folder-tree__head">
        <h2 className="folder-tree__title">{t.navFolders}</h2>
        {onCollapse && (
          // Only where the column is always visible; the overlay closes instead.
          <IconButton
            ref={collapseRef}
            kind="ghost"
            size="sm"
            align="bottom"
            label={t.collapseSidebar}
            wrapperClasses="folder-tree__collapse"
            aria-expanded="true"
            aria-controls={controls}
            onClick={onCollapse}
          >
            <SidePanelClose />
          </IconButton>
        )}
        {onClose && (
          <IconButton kind="ghost" size="sm" align="bottom-end" label={t.closeFolders} wrapperClasses="folder-tree__close" data-drawer-focus onClick={onClose}>
            <Close />
          </IconButton>
        )}
      </div>
      {data && data.accounts.some((a) => a.folders.length > 0) && (
        <div className="folder-tree__actions">
          <Button kind="ghost" size="sm" disabled={!collapsed.canCollapse} onClick={collapsed.collapseAll}>
            {t.collapseAll}
          </Button>
          <Button kind="ghost" size="sm" disabled={!collapsed.canExpand} onClick={collapsed.expandAll}>
            {t.expandAll}
          </Button>
        </div>
      )}
      {!data && !error && <SkeletonText className="folder-tree__skeleton" paragraph lineCount={5} />}
      {error && !data && <p className="folder-tree__note app-text-error">{t.loadAccountsFailed(error)}</p>}
      {data && data.accounts.length === 0 && (
        <div className="folder-tree__note">
          <p className="app-text-secondary">{t.noAccounts}</p>
          <Button size="sm" onClick={() => show("accounts")}>
            {t.addAccount}
          </Button>
        </div>
      )}
      {data && data.accounts.length > 0 && (
        // Controlled: the selection follows the URL, and the expanded nodes
        // follow sidebarState (saved per user).
        <FeatureFlags enableTreeviewControllable>
          <TreeView className="folder-tree__tree" label={t.accountsAndFolders} hideLabel size="xs" selected={selected ? [selected] : []} active={selected}>
            {data.accounts.map((a) => (
              <AccountNode key={a.name} a={a} ids={ids} state={state} mail={mail} select={select} collapsed={collapsed} />
            ))}
          </TreeView>
        </FeatureFlags>
      )}
    </nav>
  );
}

type Ids = ReturnType<typeof nodeIds>;

/**
 * Carbon calls a node's onSelect for a click and for Enter or Space. On a
 * node with children Enter also toggles it, so a node that only toggles
 * leaves Enter to that.
 */
const isEnter = (e: SyntheticEvent) => e.type === "keydown" && (e as KeyboardEvent).key === "Enter";

function AccountNode({ a, ids, state, mail, select, collapsed }: { a: Account; ids: Ids; state: ViewState; mail: boolean; select: Props["select"]; collapsed: CollapsedState }) {
  const open = !collapsed.isCollapsed(a.name, "");
  const id = ids.account(a.name);
  const selectedHere = mail && state.account === a.name;
  const nodes = a.folders.length ? buildFolderTree(a.folders, a.kind) : [];
  return (
    <TreeNode
      id={id}
      aria-labelledby={`${id}__label`}
      className={selectedHere && !open && a.folders.length ? "folder-tree__node--within" : undefined}
      label={<AccountLabel a={a} />}
      isExpanded={open}
      onToggle={() => collapsed.toggle(a.name, "")}
      onSelect={() => select(a.name, "")}
    >
      {nodes.length > 0 && [
        <TreeNode
          key="\0all"
          id={ids.all(a.name)}
          aria-labelledby={`${ids.all(a.name)}__label`}
          label={<Label name={t.allFolders} />}
          isExpanded
          onSelect={() => select(a.name, "")}
        />,
        ...nodes.map((n) => <FolderItem key={n.path} node={n} account={a.name} ids={ids} selected={selectedHere ? state.folder : null} select={select} collapsed={collapsed} />),
      ]}
    </TreeNode>
  );
}

/** The account's name with its state: syncing or importing, removed, imported, receiving, disabled, failing. */
function AccountLabel({ a }: { a: Account }) {
  const states: { text: string; title?: string }[] = [];
  if (a.sync.state !== "idle") states.push({ text: a.kind !== "imap" ? t.importing : t.syncing });
  if (a.removed) states.push({ text: t.removed });
  else if (a.kind === "import") states.push({ text: t.imported });
  else if (!a.enabled) states.push({ text: t.disabled });
  else if (a.kind === "smtp") states.push({ text: t.receiving, title: t.sourceCompleteHint });
  const failing = a.sync.health === "failing";
  return (
    <span className="folder-tree__label">
      <span className="folder-tree__name folder-tree__name--account">{a.name}</span>
      {states.map((s) => (
        <Fragment key={s.text}>
          {" "}
          <span className="folder-tree__state" title={s.title}>
            {s.text}
          </span>
        </Fragment>
      ))}
      {failing && " "}
      {failing && (
        <span className="folder-tree__state folder-tree__state--failing">
          <WarningFilled aria-hidden className="folder-tree__warning" />
          {t.tagFailing}
        </span>
      )}
    </span>
  );
}

interface ItemProps {
  node: FolderNode;
  account: string;
  ids: Ids;
  /** The selected folder in this account, or null. */
  selected: string | null;
  select: (account: string, folder: string) => void;
  collapsed: CollapsedState;
}

function FolderItem({ node, account, ids, selected, select, collapsed }: ItemProps) {
  const parent = node.children.length > 0;
  const open = !parent || !collapsed.isCollapsed(account, node.path);
  const f = node.folder;
  // The selection inside a collapsed node: show where it is.
  const inside = !open && selected !== null && contains(node, selected);
  const id = ids.folder(account, node.path);
  // A collapsed parent shows the sum of its subtree; a path part only then.
  const count = !open ? subtreeMessages(node) : f ? f.messages : undefined;
  const countTitle = open ? undefined : t.subtreeCount(subtreeMessages(node));
  return (
    <TreeNode
      id={id}
      aria-labelledby={`${id}__label`}
      className={[f ? "" : "folder-tree__node--path", inside ? "folder-tree__node--within" : ""].filter(Boolean).join(" ") || undefined}
      label={<Label name={node.label} count={count} title={f ? f.name : node.path} countTitle={countTitle} />}
      // Always a boolean: Carbon's controlled state must not switch to
      // uncontrolled when a folder gains or loses subfolders.
      isExpanded={open}
      onToggle={() => collapsed.toggle(account, node.path)}
      onSelect={(e: SyntheticEvent) => {
        if (f) select(account, f.name);
        // Only a path part: no messages of its own, nothing to select. It toggles.
        else if (!isEnter(e)) collapsed.toggle(account, node.path);
      }}
    >
      {parent ? node.children.map((c) => <FolderItem key={c.path} node={c} account={account} ids={ids} selected={selected} select={select} collapsed={collapsed} />) : undefined}
    </TreeNode>
  );
}

function contains(node: FolderNode, folder: string): boolean {
  return node.children.some((c) => c.folder?.name === folder || contains(c, folder));
}

/**
 * What the overlay focuses when it opens: the selected node, else the first
 * node, else its close button. Not simply the first button: the rail and
 * the collapse button come first in the page but are not displayed below
 * lg, and a hidden element cannot take focus.
 */
export function drawerFocusTarget(root: ParentNode | null | undefined): HTMLElement | null {
  if (!root) return null;
  const visible = (el: HTMLElement) => !el.closest(".cds--tree-node--hidden");
  const nodes = Array.from(root.querySelectorAll<HTMLElement>(".folder-pane__full [role=treeitem]")).filter(visible);
  return nodes.find((n) => n.getAttribute("aria-selected") === "true") ?? nodes[0] ?? root.querySelector<HTMLElement>(".folder-pane__full [data-drawer-focus]");
}

type PanelProps = Omit<Props, "onCollapse" | "collapseRef" | "controls"> & {
  layout: SidebarLayout;
  /** The id of the column, for aria-controls. */
  controls: string;
};

/**
 * The content of the folder column. From lg up: the full tree with its
 * resize handle, or the rail when collapsed. Below lg the column is an
 * overlay that always shows the full tree. A CSS switch only, so the first
 * paint is final.
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
  // The full tree keeps its place, so collapsing and expanding does not
  // remount it: its scroll position and the folders the user collapsed stay.
  return (
    <>
      {collapsed && <SidebarRail state={props.state} accounts={props.accounts} select={props.select} controls={controls} expandRef={expandButton} onExpand={() => toggle(false)} />}
      <div className={collapsed ? "folder-pane__full folder-pane__full--collapsed" : "folder-pane__full"}>
        <FolderTree {...props} onCollapse={collapsed ? undefined : () => toggle(true)} collapseRef={collapseButton} controls={controls} />
      </div>
      {!collapsed && <SidebarResizer layout={layout} controls={controls} onCollapse={() => toggle(true)} />}
    </>
  );
}

/** The drag handle on the column's right edge (window-splitter pattern). */
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
      className={dragging ? "folder-pane__resizer folder-pane__resizer--dragging" : "folder-pane__resizer"}
      onPointerDown={onPointerDown}
      onKeyDown={onKeyDown}
      onDoubleClick={resetWidth}
    />
  );
}

interface RailProps {
  state: ViewState;
  accounts: AccountsState;
  select: (account: string, folder: string) => void;
  controls: string;
  expandRef: Ref<HTMLButtonElement>;
  onExpand: () => void;
}

/** The collapsed column: a short label per account. Its folders are reached by expanding. */
function SidebarRail({ state, accounts, select, controls, expandRef, onExpand }: RailProps) {
  const mail = state.view === "" && state.gone !== "1";
  return (
    <nav aria-label={t.accountsAndFolders} className="folder-rail">
      <IconButton ref={expandRef} kind="ghost" size="sm" align="right" label={t.expandSidebar} aria-expanded="false" aria-controls={controls} onClick={onExpand}>
        <SidePanelOpen />
      </IconButton>
      {/* Only the accounts scroll, so the expand button stays reachable with many of them. */}
      <ul className="folder-rail__accounts">
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
  const cls = ["folder-rail__account", selected ? "folder-rail__account--selected" : "", off ? "folder-rail__account--off" : ""].filter(Boolean).join(" ");
  return (
    <button type="button" className={cls} aria-label={label} title={label} aria-current={selected ? "true" : undefined} onClick={() => select(a.name, "")}>
      <span aria-hidden>{railLabel(a.name)}</span>
      {a.sync.state !== "idle" && (
        <span aria-hidden className="folder-rail__busy">
          ↻
        </span>
      )}
      {a.sync.health !== "ok" && <span aria-hidden className={`folder-rail__dot folder-rail__dot--${a.sync.health}`} />}
    </button>
  );
}
