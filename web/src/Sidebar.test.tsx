import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Account, AccountsResponse, Folder } from "./api";
import { t } from "./i18n";
import { Sidebar, SidebarPanel, drawerFocusTarget } from "./Sidebar";
import { useSidebarLayout } from "./sidebarLayout";
import { useCollapsed } from "./sidebarState";
import type { ViewState } from "./urlState";

const f = (name: string, messages = 1): Folder => ({ name, delimiter: "/", messages, lastSyncedAt: null, lastReconciledAt: null });

function account(name: string, folders: Folder[]): Account {
  return {
    name, kind: "imap", enabled: true, removed: false, host: "h", port: 993, tls: "tls", username: "u",
    includedFolders: [], excludedFolders: [], syncInterval: "default", folders, goneMessages: 0, lastReconciledAt: null,
    addresses: [], lastReceivedAt: null, fill: null, fills: [], firstReceived: null,
    sync: { state: "idle", lastRun: null, failureStreak: 0, failingSince: null, health: "ok", syncEvery: null, nextAt: null },
  };
}

const data = (): AccountsResponse => ({
  accounts: [account("work", [f("INBOX", 5), f("Archive", 2), f("Archive/2024", 3), f("Lists/dev", 4)])],
  manage: true, syncInterval: "6h0m0s", syncIntervalMin: "5m0s", reconcileInterval: "", alertAfter: 3, smtp: null,
});

const view = (account = "", folder = ""): ViewState => ({ q: "", account, folder, group: "", gone: "", m: "", view: "" });

function Harness({ state, d, select }: { state: ViewState; d: AccountsResponse; select: (a: string, f: string) => void }) {
  const collapsed = useCollapsed("alice", d);
  return (
    <Sidebar
      state={state}
      accounts={{ data: d, error: "", reload: () => {} }}
      select={select}
      selectGone={() => {}}
      show={() => {}}
      user={{ name: "alice", admin: false, mustChangePassword: false, twoFactorEnabled: false, twoFactorRequired: false, language: "auto", locale: "auto" }}
      logout={() => {}}
      collapsed={collapsed}
    />
  );
}

afterEach(() => {
  cleanup();
  localStorage.clear();
});

// hidden: also inside a collapsed list.
const toggleFor = (name: string) => screen.getByRole("button", { name: t.foldersIn(name), hidden: true });
const pathOnly = (label: string) => screen.getByRole("button", { name: new RegExp(`^${label}`), hidden: true });

describe("Sidebar tree", () => {
  it("shows folders as a tree with short names and full paths", () => {
    render(<Harness state={view()} d={data()} select={() => {}} />);
    const year = screen.getByRole("button", { name: /^2024/ });
    expect(year.title).toBe("Archive/2024");
    // Lists is only a path part: a button that toggles, no count.
    expect(pathOnly("Lists").getAttribute("aria-expanded")).toBe("true");
  });

  it("toggles with the chevron and points aria-controls at the list", () => {
    render(<Harness state={view()} d={data()} select={() => {}} />);
    const chevron = toggleFor("Archive");
    const list = document.getElementById(chevron.getAttribute("aria-controls")!)!;
    expect(list.hidden).toBe(false);
    fireEvent.click(chevron);
    expect(chevron.getAttribute("aria-expanded")).toBe("false");
    expect(list.hidden).toBe(true);
    // A collapsed parent shows the sum of its subtree.
    const sum = screen.getByTitle(t.subtreeCount(5));
    expect(sum.textContent).toBe("5");
    expect(JSON.parse(localStorage.getItem("mail-archive.sidebar.alice")!).collapsed).toEqual({ work: ["Archive"] });
  });

  it("selects the account with its name and toggles only with the chevron", () => {
    const select = vi.fn();
    render(<Harness state={view()} d={data()} select={select} />);
    fireEvent.click(screen.getByRole("button", { name: "work" }));
    expect(select).toHaveBeenCalledWith("work", "");
    expect(toggleFor("work").getAttribute("aria-expanded")).toBe("true");
    fireEvent.click(toggleFor("work"));
    expect(toggleFor("work").getAttribute("aria-expanded")).toBe("false");
    expect(select).toHaveBeenCalledTimes(1);
  });

  it("collapses and expands all", () => {
    render(<Harness state={view()} d={data()} select={() => {}} />);
    const expand = screen.getByRole("button", { name: t.expandAll }) as HTMLButtonElement;
    const collapse = screen.getByRole("button", { name: t.collapseAll }) as HTMLButtonElement;
    expect(expand.disabled).toBe(true);
    fireEvent.click(collapse);
    expect(collapse.disabled).toBe(true);
    for (const b of [toggleFor("work"), toggleFor("Archive"), pathOnly("Lists")]) expect(b.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(expand);
    expect(toggleFor("work").getAttribute("aria-expanded")).toBe("true");
    expect(localStorage.getItem("mail-archive.sidebar.alice")).toBeNull();
  });

  it("reveals the selected folder once, not on every poll", () => {
    localStorage.setItem("mail-archive.sidebar.alice", JSON.stringify({ v: 1, collapsed: { work: ["", "Archive"] } }));
    const { rerender } = render(<Harness state={view("work", "Archive/2024")} d={data()} select={() => {}} />);
    expect(toggleFor("work").getAttribute("aria-expanded")).toBe("true");
    expect(toggleFor("Archive").getAttribute("aria-expanded")).toBe("true");
    // The user collapses the parent again; a poll brings new data.
    fireEvent.click(toggleFor("Archive"));
    act(() => rerender(<Harness state={view("work", "Archive/2024")} d={data()} select={() => {}} />));
    expect(toggleFor("Archive").getAttribute("aria-expanded")).toBe("false");
    // The collapsed parent shows that it holds the selection.
    expect(screen.getByRole("button", { name: /^Archive/ }).className).toContain("text-accent-soft-ink");
  });

  it("works with the keyboard, as buttons do", () => {
    render(<Harness state={view()} d={data()} select={() => {}} />);
    const chevron = toggleFor("Archive");
    chevron.focus();
    expect(document.activeElement).toBe(chevron);
    expect(chevron.tagName).toBe("BUTTON");
    expect(chevron.getAttribute("type")).toBe("button");
  });
});

describe("Sidebar account state", () => {
  it("says importing for a running fill, syncing for IMAP", () => {
    const d = data();
    const smtp = { ...account("journal", [f("alice/INBOX"), f("JOURNAL")]), kind: "smtp" as const, sync: { ...d.accounts[0]!.sync, state: "running" as const } };
    d.accounts = [{ ...d.accounts[0]!, sync: { ...d.accounts[0]!.sync, state: "running" } }, smtp];
    render(<Harness state={view()} d={d} select={() => {}} />);
    expect(screen.getAllByTitle(t.importing)).toHaveLength(1);
    expect(screen.getAllByTitle(t.syncing)).toHaveLength(1);
  });

  it("lists JOURNAL first in an smtp account", () => {
    const d = data();
    d.accounts = [{ ...account("journal", [f("alice/INBOX"), f("JOURNAL")]), kind: "smtp" }];
    render(<Harness state={view()} d={d} select={() => {}} />);
    const names = screen.getAllByRole("button", { hidden: true }).map((b) => b.title).filter((n) => n === "JOURNAL" || n === "alice/INBOX");
    expect(names).toEqual(["JOURNAL", "alice/INBOX"]);
  });
});

// The aside's content as App renders it: full sidebar with resize handle, or the rail.
function Panel({ state = view(), d = data(), select = () => {} }: { state?: ViewState; d?: AccountsResponse; select?: (a: string, f: string) => void }) {
  const collapsed = useCollapsed("alice", d);
  const layout = useSidebarLayout("alice");
  return (
    <aside id="aside" data-testid="aside">
      <SidebarPanel
        layout={layout}
        controls="aside"
        state={state}
        accounts={{ data: d, error: "", reload: () => {} }}
        select={select}
        selectGone={() => {}}
        show={() => {}}
        user={{ name: "alice", admin: false, mustChangePassword: false, twoFactorEnabled: false, twoFactorRequired: false, language: "auto", locale: "auto" }}
        logout={() => {}}
        collapsed={collapsed}
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

describe("Sidebar width", () => {
  it("has a separator with role, bounds and value", () => {
    render(<Panel />);
    const s = separator();
    expect(s.getAttribute("aria-valuemin")).toBe("12");
    expect(s.getAttribute("aria-valuemax")).toBe("28");
    expect(s.getAttribute("aria-valuenow")).toBe("14");
    expect(s.getAttribute("aria-valuetext")).toBe(t.sidebarWidth(14));
    expect(s.getAttribute("aria-orientation")).toBe("vertical");
    expect(s.getAttribute("aria-controls")).toBe("aside");
    expect(s.tabIndex).toBe(0);
    expect([...s.classList]).toEqual(expect.arrayContaining(["hidden", "lg:block", "touch-none"]));
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

  it("resets the width with a double-click", () => {
    localStorage.setItem(layoutKey, JSON.stringify({ v: 1, width: 22, collapsed: false }));
    render(<Panel />);
    expect(now()).toBe(22);
    fireEvent.doubleClick(separator());
    expect(now()).toBe(14);
    expect(stored()).toBeNull();
  });
});

describe("Sidebar rail", () => {
  it("collapses to a rail and back, moving focus", () => {
    render(<Panel />);
    const collapse = collapseButton();
    expect(collapse.getAttribute("aria-expanded")).toBe("true");
    expect(collapse.getAttribute("aria-controls")).toBe("aside");
    fireEvent.click(collapse);
    const expand = expandButton();
    expect(document.activeElement).toBe(expand);
    expect(expand.getAttribute("aria-expanded")).toBe("false");
    expect([...expand.closest("nav")!.classList]).toEqual(expect.arrayContaining(["hidden", "lg:flex"]));
    expect(screen.queryByRole("separator")).toBeNull();
    expect(stored()?.collapsed).toBe(true);
    fireEvent.click(expand);
    expect(screen.queryByRole("button", { name: t.expandSidebar })).toBeNull();
    expect(document.activeElement).toBe(collapseButton());
    expect(stored()).toBeNull();
  });

  it("keeps the full sidebar mounted across collapse and expand", () => {
    render(<Panel state={view("work", "Archive/2024")} />);
    const nav = screen.getByRole("button", { name: t.manageAccounts }).closest("nav");
    // The selected folder's parent was revealed; the user collapses it again.
    expect(toggleFor("Archive").getAttribute("aria-expanded")).toBe("true");
    fireEvent.click(toggleFor("Archive"));
    fireEvent.click(collapseButton());
    fireEvent.click(expandButton());
    expect(toggleFor("Archive").getAttribute("aria-expanded")).toBe("false");
    // Same element, so its scroll position is kept too.
    expect(screen.getByRole("button", { name: t.manageAccounts }).closest("nav")).toBe(nav);
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
    expect(old.className).toContain("border-dashed");
    expect(rail.getByRole("button", { name: `gone, ${t.removed}` }).className).toContain("opacity-60");
    expect(rail.getByRole("button", { name: `bad, ${t.railFailing}` }).querySelector(".bg-danger")).not.toBeNull();
    expect(rail.getByRole("button", { name: `late, ${t.railStale}` }).querySelector(".bg-warning")).not.toBeNull();
    expect(rail.getByRole("button", { name: `busy, ${t.syncing}` }).querySelector(".animate-spin")).not.toBeNull();
    expect(rail.getByRole("button", { name: t.allMail }).getAttribute("aria-current")).toBeNull();
  });

  it("marks All mail in the rail when nothing is selected, not in the archive-only view", () => {
    collapseStored();
    const { rerender } = render(<Panel />);
    const allMail = () => within(expandButton().closest("nav")!).getByRole("button", { name: t.allMail });
    expect(allMail().getAttribute("aria-current")).toBe("true");
    rerender(<Panel state={{ ...view(), gone: "1" }} />);
    expect(allMail().getAttribute("aria-current")).toBeNull();
  });

  it("selects an account from the rail", () => {
    collapseStored();
    const select = vi.fn();
    render(<Panel select={select} />);
    fireEvent.click(within(expandButton().closest("nav")!).getByRole("button", { name: "work" }));
    expect(select).toHaveBeenCalledWith("work", "");
  });

  it("keeps the full sidebar for the drawer while collapsed", () => {
    collapseStored();
    render(<Panel />);
    const full = screen.getByRole("button", { name: t.manageAccounts }).closest("nav")!;
    expect(full.parentElement!.className).toContain("lg:hidden");
    expect(within(full).getByRole("button", { name: t.foldersIn("work") })).toBeTruthy();
    // No collapse button inside the drawer's copy.
    expect(screen.queryByRole("button", { name: t.collapseSidebar })).toBeNull();
  });

  it("focuses a button the drawer shows, also while collapsed", () => {
    // Below lg the rail and the collapse button are display:none and
    // cannot take focus; the drawer must focus the full sidebar's All mail.
    collapseStored();
    const { unmount } = render(<Panel />);
    let target = drawerFocusTarget(screen.getByTestId("aside"));
    expect(target?.textContent).toBe(t.allMail);
    expect(target?.closest(".lg\\:hidden")).not.toBeNull();
    unmount();
    render(<Panel />);
    act(() => fireEvent.click(expandButton()));
    target = drawerFocusTarget(screen.getByTestId("aside"));
    expect(target?.textContent).toBe(t.allMail);
    expect(target?.closest(".hidden")).toBeNull();
  });
});
