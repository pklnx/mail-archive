import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Account, AccountsResponse, Folder } from "./api";
import { t } from "./i18n";
import { Sidebar } from "./Sidebar";
import { useCollapsed } from "./sidebarState";
import type { ViewState } from "./urlState";

const f = (name: string, messages = 1): Folder => ({ name, delimiter: "/", messages, lastSyncedAt: null, lastReconciledAt: null });

function account(name: string, folders: Folder[]): Account {
  return {
    name, kind: "imap", enabled: true, removed: false, host: "h", port: 993, tls: "tls", username: "u",
    includedFolders: [], excludedFolders: [], syncInterval: "default", folders, goneMessages: 0, lastReconciledAt: null,
    addresses: [], lastReceivedAt: null, fill: null, firstReceived: null,
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
