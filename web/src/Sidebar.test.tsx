import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Account, AccountsResponse, Folder } from "./api";
import { applySettings, messagesFor, t } from "./i18n";
import { FolderTree, SidebarPanel, drawerFocusTarget } from "./Sidebar";
import { useSidebarLayout } from "./sidebarLayout";
import { useCollapsed } from "./sidebarState";
import type { ViewState } from "./urlState";

const f = (name: string, messages = 1): Folder => ({ name, delimiter: "/", messages, lastSyncedAt: null, lastReconciledAt: null });

function account(name: string, folders: Folder[]): Account {
  return {
    name, kind: "imap", enabled: true, removed: false, purging: false, purge: null, host: "h", port: 993, tls: "tls", username: "u",
    includedFolders: [], excludedFolders: [], syncInterval: "default", folders, goneMessages: 0, lastReconciledAt: null,
    addresses: [], lastReceivedAt: null, fill: null, fills: [], firstReceived: null,
    sync: { state: "idle", lastRun: null, failureStreak: 0, failingSince: null, health: "ok", syncEvery: null, nextAt: null },
  };
}

const data = (): AccountsResponse => ({
  accounts: [account("work", [f("INBOX", 5), f("Archive", 2), f("Archive/2024", 3), f("Lists/dev", 4)])],
  manage: true, syncInterval: "6h0m0s", syncIntervalMin: "5m0s", reconcileInterval: "", alertAfter: 3, smtp: null,
});

const view = (account = "", folder = ""): ViewState => ({ q: "", account, folder, group: "", gone: "", m: "", view: "", tab: "", id: "", add: "", import: "", since: null, until: "", action: [], user: [], actor: [] });

function Harness({ state, d, select, show = () => {} }: { state: ViewState; d: AccountsResponse | null; select: (a: string, f: string) => void; show?: (v: string) => void }) {
  const collapsed = useCollapsed("alice", d);
  return <FolderTree state={state} accounts={{ data: d, error: "", reload: () => {} }} select={select} show={show} collapsed={collapsed} />;
}

afterEach(() => {
  cleanup();
  localStorage.clear();
});

// The tree's nodes by their accessible name (the label: name, then count or state).
const node = (name: string | RegExp) => screen.getByRole("treeitem", { name, hidden: true });
const expanded = (name: string | RegExp) => node(name).getAttribute("aria-expanded");
/** Carbon's caret, which toggles a node without selecting it. */
const caret = (name: string | RegExp) => node(name).querySelector<HTMLElement>(":scope > .cds--tree-node__label .cds--tree-parent-node__toggle")!;

describe("Folder tree", () => {
  it("is a Carbon TreeView with short names, counts and full paths", () => {
    render(<Harness state={view()} d={data()} select={() => {}} />);
    const tree = screen.getByRole("tree", { name: t.accountsAndFolders });
    expect(tree.className).toContain("cds--tree--xs");
    const year = node("2024 3");
    expect(year.querySelector("[title]")?.getAttribute("title")).toBe("Archive/2024");
    expect(node("INBOX 5")).toBeTruthy();
    // Lists is only a path part: a parent without a count, expanded.
    expect(expanded("Lists")).toBe("true");
    expect(node(/^work$/).getAttribute("aria-expanded")).toBe("true");
  });

  it("toggles with the caret and keeps the collapsed children in the page", () => {
    render(<Harness state={view()} d={data()} select={() => {}} />);
    const group = document.getElementById(node("Archive 2").getAttribute("aria-owns")!)!;
    expect(group.classList.contains("cds--tree-node--hidden")).toBe(false);
    fireEvent.click(caret("Archive 2"));
    // A collapsed parent shows the sum of its subtree.
    const archive = node("Archive 5");
    expect(archive.getAttribute("aria-expanded")).toBe("false");
    expect(group.classList.contains("cds--tree-node--hidden")).toBe(true);
    expect(within(archive).getByTitle(t.subtreeCount(5)).textContent?.trim()).toBe("5");
    expect(JSON.parse(localStorage.getItem("mail-archive.sidebar.alice")!).collapsed).toEqual({ work: ["Archive"] });
  });

  it("selects the account with its node or All folders, and toggles only with the caret", () => {
    const select = vi.fn();
    render(<Harness state={view()} d={data()} select={select} />);
    fireEvent.click(node(/^work$/));
    expect(select).toHaveBeenCalledWith("work", "");
    fireEvent.click(node(t.allFolders));
    expect(select).toHaveBeenCalledTimes(2);
    expect(select).toHaveBeenLastCalledWith("work", "");
    expect(expanded(/^work$/)).toBe("true");
    fireEvent.click(caret(/^work$/));
    expect(expanded(/^work$/)).toBe("false");
    expect(select).toHaveBeenCalledTimes(2);
    fireEvent.click(node("Archive 2"));
    expect(select).toHaveBeenLastCalledWith("work", "Archive");
  });

  it("toggles a path part on click instead of selecting it", () => {
    const select = vi.fn();
    render(<Harness state={view()} d={data()} select={select} />);
    fireEvent.click(node("Lists"));
    expect(expanded(/^Lists/)).toBe("false");
    expect(select).not.toHaveBeenCalled();
  });

  it("collapses and expands all", () => {
    render(<Harness state={view()} d={data()} select={() => {}} />);
    const expand = screen.getByRole("button", { name: t.expandAll }) as HTMLButtonElement;
    const collapse = screen.getByRole("button", { name: t.collapseAll }) as HTMLButtonElement;
    expect(expand.disabled).toBe(true);
    fireEvent.click(collapse);
    expect(collapse.disabled).toBe(true);
    for (const n of [/^work$/, /^Archive/, /^Lists/]) expect(expanded(n)).toBe("false");
    fireEvent.click(expand);
    expect(expanded(/^work$/)).toBe("true");
    expect(localStorage.getItem("mail-archive.sidebar.alice")).toBeNull();
  });

  it("reveals the selected folder once, not on every poll", () => {
    localStorage.setItem("mail-archive.sidebar.alice", JSON.stringify({ v: 1, collapsed: { work: ["", "Archive"] } }));
    const { rerender } = render(<Harness state={view("work", "Archive/2024")} d={data()} select={() => {}} />);
    expect(expanded(/^work$/)).toBe("true");
    expect(expanded(/^Archive/)).toBe("true");
    // The selected folder is the tree's selected and active node.
    expect(node("2024 3").getAttribute("aria-selected")).toBe("true");
    // The user collapses the parent again; a poll brings new data.
    fireEvent.click(caret(/^Archive/));
    act(() => rerender(<Harness state={view("work", "Archive/2024")} d={data()} select={() => {}} />));
    expect(expanded(/^Archive/)).toBe("false");
    // The collapsed parent shows that it holds the selection.
    expect(node(/^Archive/).className).toContain("folder-tree__node--within");
  });

  it("follows the selection from the URL", () => {
    const { rerender } = render(<Harness state={view("work")} d={data()} select={() => {}} />);
    expect(node(t.allFolders).getAttribute("aria-selected")).toBe("true");
    rerender(<Harness state={view("work", "INBOX")} d={data()} select={() => {}} />);
    expect(node(t.allFolders).getAttribute("aria-selected")).toBe("false");
    expect(node("INBOX 5").getAttribute("aria-selected")).toBe("true");
    rerender(<Harness state={{ ...view(), gone: "1" }} d={data()} select={() => {}} />);
    expect(screen.queryAllByRole("treeitem", { selected: true, hidden: true })).toHaveLength(0);
  });

  it("works with the keyboard: arrows move, Right and Left expand and collapse, Enter selects", () => {
    const select = vi.fn();
    render(<Harness state={view()} d={data()} select={select} />);
    const work = node(/^work$/);
    // One tab stop: the first node.
    expect(work.tabIndex).toBe(0);
    work.focus();
    fireEvent.keyDown(work, { key: "ArrowLeft" });
    expect(expanded(/^work$/)).toBe("false");
    fireEvent.keyDown(work, { key: "ArrowRight" });
    expect(expanded(/^work$/)).toBe("true");
    fireEvent.keyDown(work, { key: "ArrowDown" });
    expect(document.activeElement).toBe(node(t.allFolders));
    fireEvent.keyDown(document.activeElement!, { key: "ArrowDown" });
    expect(document.activeElement).toBe(node("INBOX 5"));
    fireEvent.keyDown(document.activeElement!, { key: "Enter" });
    expect(select).toHaveBeenCalledWith("work", "INBOX");
  });

  it("toggles a path part once with Enter", () => {
    render(<Harness state={view()} d={data()} select={() => {}} />);
    const lists = node(/^Lists/);
    lists.focus();
    fireEvent.keyDown(lists, { key: "Enter" });
    expect(expanded(/^Lists/)).toBe("false");
    fireEvent.keyDown(lists, { key: " " });
    expect(expanded(/^Lists/)).toBe("true");
  });
});

describe("Folder tree account state", () => {
  it("says importing for a running fill, syncing for IMAP, and receiving for smtp", () => {
    const d = data();
    const smtp = { ...account("journal", [f("alice/INBOX"), f("JOURNAL")]), kind: "smtp" as const };
    const fill = { ...account("files", []), kind: "smtp" as const, sync: { ...d.accounts[0]!.sync, state: "running" as const } };
    d.accounts = [{ ...d.accounts[0]!, sync: { ...d.accounts[0]!.sync, state: "running" } }, smtp, fill];
    render(<Harness state={view()} d={d} select={() => {}} />);
    expect(node(`work ${t.syncing}`)).toBeTruthy();
    expect(node(`journal ${t.receiving}`)).toBeTruthy();
    expect(screen.getAllByTitle(t.sourceCompleteHint).map((e) => e.textContent)).toEqual([t.receiving, t.receiving]);
    expect(node(`files ${t.importing} ${t.receiving}`)).toBeTruthy();
  });

  it("shows removed, disabled, imported and failing with a warning icon", () => {
    const d = data();
    const base = d.accounts[0]!;
    d.accounts = [
      { ...account("old", []), removed: true },
      { ...account("off", []), enabled: false },
      { ...account("files", []), kind: "import" },
      { ...account("bad", []), sync: { ...base.sync, health: "failing" } },
    ];
    render(<Harness state={view()} d={d} select={() => {}} />);
    expect(node(`old ${t.removed}`)).toBeTruthy();
    expect(node(`off ${t.disabled}`)).toBeTruthy();
    expect(node(`files ${t.imported}`)).toBeTruthy();
    expect(node(`bad ${t.tagFailing}`).querySelector("svg.folder-tree__warning")).not.toBeNull();
  });

  it("lists JOURNAL first in an smtp account", () => {
    const d = data();
    d.accounts = [{ ...account("journal", [f("alice/INBOX"), f("JOURNAL")]), kind: "smtp" }];
    render(<Harness state={view()} d={d} select={() => {}} />);
    const names = screen
      .getAllByRole("treeitem", { hidden: true })
      .map((n) => n.querySelector(":scope > .cds--tree-node__label [title]")?.getAttribute("title"))
      .filter((n) => n === "JOURNAL" || n === "alice/INBOX");
    expect(names).toEqual(["JOURNAL", "alice/INBOX"]);
  });

  it("offers to add an account when there is none", () => {
    const show = vi.fn();
    const d = data();
    d.accounts = [];
    render(<Harness state={view()} d={d} select={() => {}} show={show} />);
    expect(screen.queryByRole("tree")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: t.addAccount }));
    expect(show).toHaveBeenCalledWith("accounts");
  });

  it("makes Add account primary only in the mail view (#211)", () => {
    const d = data();
    d.accounts = [];
    const kind = () => screen.getByRole("button", { name: t.addAccount }).className;
    const r = render(<Harness state={view()} d={d} select={() => {}} />);
    expect(kind()).toContain("cds--btn--primary");
    // The Accounts page has its own primary button next to the column.
    r.rerender(<Harness state={{ ...view(), view: "accounts" }} d={d} select={() => {}} />);
    expect(kind()).toContain("cds--btn--tertiary");
  });

  it("shows Carbon's tree in German", () => {
    applySettings({ language: "de", locale: "de" });
    try {
      const de = messagesFor("de");
      const d = data();
      d.accounts.push({ ...account("alt", []), removed: true });
      render(<Panel d={d} />);
      expect(screen.getByRole("tree", { name: de.accountsAndFolders })).toBeTruthy();
      expect(screen.getByRole("treeitem", { name: de.allFolders })).toBeTruthy();
      expect(screen.getByRole("treeitem", { name: `alt ${de.removed}` })).toBeTruthy();
      for (const name of [de.collapseAll, de.expandAll, de.collapseSidebar, de.closeFolders]) expect(screen.getByRole("button", { name })).toBeTruthy();
      expect(screen.getByRole("separator", { name: de.resizeSidebar }).getAttribute("aria-valuetext")).toBe(de.sidebarWidth("14"));
      const html = document.body.innerHTML;
      for (const s of ["All folders", "Collapse", "Expand", "Resize", "removed", "Close"]) expect(html).not.toContain(s);
    } finally {
      applySettings({ language: "en", locale: "en" });
    }
  });
});

// The column's content as App renders it: full tree with resize handle, or the rail.
function Panel({
  state = view(),
  d = data(),
  select = () => {},
  onClose = () => {},
  cap,
}: {
  state?: ViewState;
  d?: AccountsResponse;
  select?: (a: string, f: string) => void;
  onClose?: () => void;
  cap?: number;
}) {
  const collapsed = useCollapsed("alice", d);
  const layout = useSidebarLayout("alice", cap);
  return (
    <aside id="aside" data-testid="aside">
      <SidebarPanel
        layout={layout}
        controls="aside"
        state={state}
        accounts={{ data: d, error: "", reload: () => {} }}
        select={select}
        show={() => {}}
        collapsed={collapsed}
        onClose={onClose}
      />
    </aside>
  );
}

const layoutKey = "mail-archive.sidebar-layout.alice";
const stored = () => JSON.parse(localStorage.getItem(layoutKey) ?? "null") as { width: number; collapsed: boolean } | null;
const separator = () => screen.getByRole("separator", { name: t.resizeSidebar });
const now = () => Number(separator().getAttribute("aria-valuenow"));
const collapseButton = () => screen.getByRole("button", { name: t.collapseSidebar });
const expandButton = () => screen.getByRole("button", { name: t.expandSidebar });
const collapseStored = () => localStorage.setItem(layoutKey, JSON.stringify({ v: 1, width: 14, collapsed: true }));

describe("Folder column width", () => {
  it("has a separator with role, bounds and value", () => {
    render(<Panel />);
    const s = separator();
    expect(s.getAttribute("aria-valuemin")).toBe("12");
    expect(s.getAttribute("aria-valuemax")).toBe("28");
    expect(s.getAttribute("aria-valuenow")).toBe("14");
    expect(s.getAttribute("aria-valuetext")).toBe(t.sidebarWidth("14"));
    expect(s.getAttribute("aria-orientation")).toBe("vertical");
    expect(s.getAttribute("aria-controls")).toBe("aside");
    expect(s.tabIndex).toBe(0);
    // layout.css hides it below lg and widens it for touch.
    expect(s.className).toBe("folder-pane__resizer");
  });

  it("resizes with the keyboard and clamps", () => {
    render(<Panel />);
    const s = separator();
    const key = (k: string) => fireEvent.keyDown(s, { key: k });
    expect(key("ArrowRight")).toBe(false); // default prevented
    expect(now()).toBe(15);
    key("End");
    expect(now()).toBe(28);
    key("ArrowRight");
    expect(now()).toBe(28);
    key("Home");
    expect(now()).toBe(12);
    key("ArrowLeft");
    expect(now()).toBe(12);
    expect(stored()?.width).toBe(12);
    // Other keys are left alone.
    expect(key("a")).toBe(true);
  });

  it("resizes by dragging with a pointer and saves on release", () => {
    render(<Panel />);
    const s = separator();
    // A drag start prevents the default, so no text gets selected.
    expect(fireEvent.pointerDown(s, { clientX: 100, button: 0 })).toBe(false);
    expect(document.body.style.userSelect).toBe("none");
    expect(s.className).toContain("folder-pane__resizer--dragging");
    fireEvent.pointerMove(window, { clientX: 180 }); // 80px at 16px per rem
    expect(now()).toBe(19);
    expect(stored()).toBeNull();
    fireEvent.pointerUp(window);
    expect(stored()?.width).toBe(19);
    expect(document.body.style.userSelect).toBe("");
    // Moving after release changes nothing.
    fireEvent.pointerMove(window, { clientX: 400 });
    expect(now()).toBe(19);

    fireEvent.pointerDown(s, { clientX: 0, button: 0 });
    fireEvent.pointerMove(window, { clientX: 2000 });
    expect(now()).toBe(28);
    fireEvent.pointerCancel(window);
    expect(document.body.style.userSelect).toBe("");
    expect(stored()?.width).toBe(28);
    fireEvent.pointerMove(window, { clientX: 0 });
    expect(now()).toBe(28);
  });

  it("ignores other mouse buttons", () => {
    render(<Panel />);
    expect(fireEvent.pointerDown(separator(), { clientX: 100, button: 2 })).toBe(true);
    fireEvent.pointerMove(window, { clientX: 300 });
    expect(now()).toBe(14);
    expect(document.body.style.userSelect).toBe("");
  });

  it("restores the text selection when unmounted during a drag", () => {
    const { unmount } = render(<Panel />);
    fireEvent.pointerDown(separator(), { clientX: 100, button: 0 });
    unmount();
    expect(document.body.style.userSelect).toBe("");
  });

  it("reads the width in the user's number format (#310)", () => {
    localStorage.setItem(layoutKey, JSON.stringify({ v: 1, width: 13.25, collapsed: false }));
    render(<Panel />);
    expect(separator().getAttribute("aria-valuetext")).toBe("13.25 rem wide");
    expect(now()).toBe(13.25);
    cleanup();
    applySettings({ language: "de", locale: "de" });
    try {
      render(<Panel />);
      const s = screen.getByRole("separator", { name: messagesFor("de").resizeSidebar });
      expect(s.getAttribute("aria-valuetext")).toBe("13,25 rem breit");
      // The value itself stays a number.
      expect(s.getAttribute("aria-valuenow")).toBe("13.25");
    } finally {
      applySettings({ language: "en", locale: "en" });
    }
  });

  it("stops at the cap: value, maximum, End, arrows and dragging, and saves the clamped width (#310)", () => {
    localStorage.setItem(layoutKey, JSON.stringify({ v: 1, width: 28, collapsed: false }));
    const { rerender } = render(<Panel cap={16.75} />);
    const s = separator();
    expect(now()).toBe(16.75);
    expect(s.getAttribute("aria-valuemax")).toBe("16.75");
    expect(s.getAttribute("aria-valuetext")).toBe(t.sidebarWidth("16.75"));
    // Applying the cap saved nothing.
    expect(stored()?.width).toBe(28);
    fireEvent.keyDown(s, { key: "ArrowLeft" });
    expect(now()).toBe(15.75);
    fireEvent.keyDown(s, { key: "ArrowRight" });
    fireEvent.keyDown(s, { key: "ArrowRight" });
    expect(now()).toBe(16.75);
    expect(stored()?.width).toBe(16.75);
    fireEvent.keyDown(s, { key: "Home" });
    expect(now()).toBe(12);
    fireEvent.keyDown(s, { key: "End" });
    expect(now()).toBe(16.75);
    expect(stored()?.width).toBe(16.75);
    fireEvent.keyDown(s, { key: "Home" });
    fireEvent.pointerDown(s, { clientX: 0, button: 0 });
    fireEvent.pointerMove(window, { clientX: 2000 });
    expect(now()).toBe(16.75);
    fireEvent.pointerUp(window);
    expect(stored()?.width).toBe(16.75);
    // A wider window: the full range again.
    rerender(<Panel cap={28} />);
    expect(separator().getAttribute("aria-valuemax")).toBe("28");
    fireEvent.keyDown(separator(), { key: "End" });
    expect(now()).toBe(28);
  });

  it("resets the width with a double-click", () => {
    localStorage.setItem(layoutKey, JSON.stringify({ v: 1, width: 22, collapsed: false }));
    render(<Panel />);
    expect(now()).toBe(22);
    fireEvent.doubleClick(separator());
    expect(now()).toBe(14);
    expect(stored()).toBeNull();
  });
});

describe("Folder column rail", () => {
  it("collapses to a rail and back, moving focus", () => {
    render(<Panel />);
    const collapse = collapseButton();
    expect(collapse.getAttribute("aria-expanded")).toBe("true");
    expect(collapse.getAttribute("aria-controls")).toBe("aside");
    fireEvent.click(collapse);
    const expand = expandButton();
    expect(document.activeElement).toBe(expand);
    expect(expand.getAttribute("aria-expanded")).toBe("false");
    // layout.css shows the rail from lg up only.
    expect(expand.closest("nav")!.className).toBe("folder-rail");
    expect(screen.queryByRole("separator")).toBeNull();
    expect(stored()?.collapsed).toBe(true);
    fireEvent.click(expand);
    expect(screen.queryByRole("button", { name: t.expandSidebar })).toBeNull();
    expect(document.activeElement).toBe(collapseButton());
    expect(stored()).toBeNull();
  });

  it("keeps the full tree mounted across collapse and expand", () => {
    render(<Panel state={view("work", "Archive/2024")} />);
    const tree = screen.getByRole("tree", { hidden: true });
    // The selected folder's parent was revealed; the user collapses it again.
    expect(expanded(/^Archive/)).toBe("true");
    fireEvent.click(caret(/^Archive/));
    fireEvent.click(collapseButton());
    fireEvent.click(expandButton());
    expect(expanded(/^Archive/)).toBe("false");
    // Same element, so its scroll position is kept too.
    expect(screen.getByRole("tree", { hidden: true })).toBe(tree);
  });

  it("collapses with Enter on the separator and focuses the expand button", () => {
    render(<Panel />);
    separator().focus();
    expect(fireEvent.keyDown(separator(), { key: "Enter" })).toBe(false);
    expect(document.activeElement).toBe(expandButton());
    expect(stored()?.collapsed).toBe(true);
  });

  it("does not move focus for the stored state on load", () => {
    collapseStored();
    render(<Panel />);
    expect(document.activeElement).toBe(document.body);
  });

  it("shows each account in the rail with its full name, selection and state", () => {
    collapseStored();
    const d = data();
    const base = d.accounts[0]!;
    d.accounts = [
      base,
      account("Private Mail", []),
      { ...account("old", []), enabled: false },
      { ...account("bad", []), sync: { ...base.sync, health: "failing" } },
      { ...account("late", []), sync: { ...base.sync, health: "stale" } },
      { ...account("busy", []), sync: { ...base.sync, state: "running" } },
      { ...account("gone", []), removed: true },
    ];
    render(<Panel state={view("work", "INBOX")} d={d} />);
    const rail = within(expandButton().closest("nav")!);
    const work = rail.getByRole("button", { name: "work" });
    expect(work.textContent).toBe("WO");
    expect(work.title).toBe("work");
    expect(work.getAttribute("aria-current")).toBe("true");
    const pm = rail.getByRole("button", { name: "Private Mail" });
    expect(pm.textContent).toBe("PM");
    expect(pm.getAttribute("aria-current")).toBeNull();
    const old = rail.getByRole("button", { name: `old, ${t.disabled}` });
    expect(old.title).toBe(`old, ${t.disabled}`);
    expect(old.className).toContain("folder-rail__account--off");
    expect(rail.getByRole("button", { name: `gone, ${t.removed}` }).className).toContain("folder-rail__account--off");
    expect(rail.getByRole("button", { name: `bad, ${t.railFailing}` }).querySelector(".folder-rail__dot--failing")).not.toBeNull();
    expect(rail.getByRole("button", { name: `late, ${t.railStale}` }).querySelector(".folder-rail__dot--stale")).not.toBeNull();
    expect(rail.getByRole("button", { name: `busy, ${t.syncing}` }).querySelector(".folder-rail__busy")).not.toBeNull();
  });

  it("marks no account in the rail in the archive-only view", () => {
    collapseStored();
    const { rerender } = render(<Panel state={view("work")} />);
    const work = () => within(expandButton().closest("nav")!).getByRole("button", { name: "work" });
    expect(work().getAttribute("aria-current")).toBe("true");
    rerender(<Panel state={{ ...view("work"), gone: "1" }} />);
    expect(work().getAttribute("aria-current")).toBeNull();
  });

  it("selects an account from the rail", () => {
    collapseStored();
    const select = vi.fn();
    render(<Panel select={select} />);
    fireEvent.click(within(expandButton().closest("nav")!).getByRole("button", { name: "work" }));
    expect(select).toHaveBeenCalledWith("work", "");
  });

  it("keeps the full tree for the overlay while collapsed", () => {
    collapseStored();
    render(<Panel />);
    const full = screen.getByRole("tree", { hidden: true }).closest(".folder-pane__full")!;
    // layout.css hides it from lg up while collapsed.
    expect(full.className).toContain("folder-pane__full--collapsed");
    expect(within(full as HTMLElement).getByRole("treeitem", { name: /^work$/, hidden: true })).toBeTruthy();
    // No collapse button inside the overlay's copy.
    expect(screen.queryByRole("button", { name: t.collapseSidebar })).toBeNull();
  });

  it("focuses what the overlay shows: the selected node, else the first one", () => {
    // Below lg the rail and the collapse button are display:none and cannot
    // take focus; the overlay must focus a node of the full tree.
    collapseStored();
    const { unmount } = render(<Panel state={view("work", "INBOX")} />);
    let target = drawerFocusTarget(screen.getByTestId("aside"));
    expect(target).toBe(node("INBOX 5"));
    expect(target?.closest(".folder-pane__full")).not.toBeNull();
    unmount();
    render(<Panel />);
    act(() => {
      fireEvent.click(expandButton());
    });
    target = drawerFocusTarget(screen.getByTestId("aside"));
    expect(target).toBe(node(/^work$/));
  });

  it("closes the overlay with its close button", () => {
    const onClose = vi.fn();
    render(<Panel onClose={onClose} />);
    fireEvent.click(screen.getByRole("button", { name: t.closeFolders }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
