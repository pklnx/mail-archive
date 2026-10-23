import { useCallback, useEffect, useId, useRef, useState, type CSSProperties, type KeyboardEvent as ReactKeyboardEvent, type MouseEvent } from "react";
import { Header, HeaderGlobalAction, HeaderGlobalBar, HeaderMenuButton, HeaderName, IconButton } from "@carbon/react";
import { Folders, Keyboard, Logout, UserAvatar } from "@carbon/icons-react";
import { AccountsPage } from "./AccountsPage";
import { isPlainClick } from "./clicks";
import { dialogOpen } from "./dialogs";
import { HealthBanner } from "./HealthBanner";
import { ProfilePage } from "./Profile";
import { UsersPage } from "./UsersPage";
import { MessageList } from "./MessageList";
import { LogPage } from "./LogPage";
import { RetentionPage } from "./RetentionPage";
import { MessageView } from "./MessageView";
import { SearchFilters } from "./SearchFilters";
import { hasPrefix, searchFilter } from "./searchSyntax";
import { SidebarPanel, drawerFocusTarget } from "./Sidebar";
import { ShellNav } from "./ShellNav";
import { ThemeToggle } from "./ThemeToggle";
import { lg, useMediaQuery } from "./breakpoints";
import { BackIcon, DownIcon, FilterIcon, UpIcon, iconButton } from "./icons";
import { FilterChips } from "./FilterChips";
import { Sheet } from "./Sheet";
import { primary } from "./formStyles";
import { noList, pager, type ListNav } from "./pager";
import logo from "./logo.svg";
import { useAccounts } from "./useAccounts";
import { useCollapsed } from "./sidebarState";
import { railWidth, useSidebarLayout } from "./sidebarLayout";
import { useViewState } from "./urlState";
import { t } from "./i18n";
import type { User } from "./api";

interface Props {
  user: User;
  logout: () => void;
}

export function App({ user, logout }: Props) {
  const [state, update] = useViewState();
  const [query, setQuery] = useState(state.q);
  // Below lg the shell's SideNav (menuOpen) and the folder column
  // (foldersOpen) open as overlays, never both. From lg up both are always
  // on screen, the SideNav as Carbon's rail.
  const wide = useMediaQuery(lg);
  const [menuOpen, setMenuOpen] = useState(false);
  const [foldersOpen, setFoldersOpen] = useState(false);
  useEffect(() => {
    if (wide) {
      setMenuOpen(false);
      setFoldersOpen(false);
    }
  }, [wide]);
  // Phones show the filters as chips and a sheet; the filter button is
  // highlighted while a prefix or grouping is set.
  const prefixed = hasPrefix(query) || state.group === "1";
  const [sheet, setSheet] = useState<"" | "filters" | "shortcuts">("");
  const closeSheet = useCallback(() => setSheet(""), []);
  const accounts = useAccounts();
  const collapsed = useCollapsed(user.name, accounts.data);
  const layout = useSidebarLayout(user.name);
  const asideId = useId();
  const navId = useId();

  // Follow the URL when it changes from outside (back button).
  useEffect(() => setQuery(state.q), [state.q]);

  // Search as you type, without a history entry per keystroke. Only the
  // query changes here; an open message stays open.
  const typed = query.trim() ? query : "";
  useEffect(() => {
    if (typed === state.q) return;
    const t = window.setTimeout(() => update({ q: typed }, { replace: true }), 300);
    return () => window.clearTimeout(t);
  }, [typed, state.q, update]);

  const filter = {
    ...searchFilter(state.q),
    account: state.account,
    folder: state.folder,
    group: state.group === "1",
    gone: state.gone === "1",
  };
  const reading = state.m !== "";
  const page =
    state.view === "profile" || state.view === "log" || state.view === "accounts" || (user.admin && (state.view === "users" || state.view === "retention"));
  const mailView = !page;

  // Previous and next message in list order. Paging replaces the history
  // entry, so Back still returns to the list.
  const [nav, setNav] = useState<ListNav>(noList);
  const onNav = useCallback((n: ListNav) => setNav(n), []);
  const pos = pager(nav, state.m);
  const [advance, setAdvance] = useState(false);
  const go = useCallback((m: string) => update({ m }, { replace: true }), [update]);
  const next = () => {
    if (pos.next) go(pos.next);
    else if (pos.nextNeedsMore) {
      setAdvance(true);
      nav.loadMore();
    }
  };
  // After "next" at the end of a page: move on once the next page is there.
  useEffect(() => {
    if (!advance) return;
    const p = pager(nav, state.m);
    if (p.next) {
      setAdvance(false);
      go(p.next);
    } else if (!p.nextNeedsMore) setAdvance(false);
  }, [advance, nav, state.m, go]);

  // The list's title on phones: where the user is.
  const place =
    state.gone === "1"
      ? { title: t.onlyInArchive, sub: "" }
      : state.folder
        ? { title: state.folder, sub: state.account }
        : { title: state.account || t.allMail, sub: "" };
  const setGroup = (on: boolean) => update({ group: on ? "1" : "" }, { replace: true });
  const show = (view: string) => {
    update({ view, m: "" });
    setMenuOpen(false);
    setFoldersOpen(false);
    setSheet("");
  };
  // The folder overlay belongs to the mail view.
  useEffect(() => {
    if (!mailView) setFoldersOpen(false);
  }, [mailView]);

  // The overlays: focus moves into them and back to where it was, and the
  // rest of the page is inert meanwhile (below), so Tab stays inside. The
  // header stays usable next to the SideNav, so its menu button closes it.
  // The folder overlay is modal: the header is inert too, so nothing behind
  // it opens another overlay.
  const aside = useRef<HTMLElement>(null);
  const sideNav = useRef<HTMLElement>(null);
  const opener = useRef<HTMLElement | null>(null);
  const overlay = menuOpen ? "menu" : foldersOpen ? "folders" : "";
  useEffect(() => {
    if (overlay) {
      opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      const target = overlay === "menu" ? sideNav.current?.querySelector<HTMLElement>("a[href]") : drawerFocusTarget(aside.current);
      target?.focus();
    } else if (opener.current) {
      opener.current.focus();
      opener.current = null;
    }
  }, [overlay]);
  const hide = overlay !== "" || undefined;
  const closeFolders = useCallback(() => setFoldersOpen(false), []);
  // Escape closes the folder overlay. In the capture phase: Carbon's
  // TreeView keeps the keys it gets from bubbling.
  const foldersKey = (e: ReactKeyboardEvent) => {
    if (foldersOpen && e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      closeFolders();
    }
  };

  // Keyboard shortcuts (? lists them). Not while typing or in a dialog (the
  // sheets, a Carbon Modal, the folder overlay). In the capture phase, so
  // they also work while a folder in Carbon's TreeView has focus: the tree
  // keeps its keys from bubbling.
  const search = useRef<HTMLInputElement>(null);
  const chord = useRef(0);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey || dialogOpen()) return;
      if (e.key === "Escape" && menuOpen) {
        setMenuOpen(false);
        return;
      }
      const el = e.target instanceof HTMLElement ? e.target : null;
      if (el && (el.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(el.tagName))) {
        if (e.key === "Escape") el.blur();
        return;
      }
      const mail = state.view === "";
      const afterG = Date.now() - chord.current < 1000;
      chord.current = 0;
      switch (e.key) {
        case "/":
          if (mail) {
            e.preventDefault();
            search.current?.focus();
          }
          break;
        case "?":
          setSheet("shortcuts");
          break;
        case "j":
          if (mail && reading) next();
          else if (mail && nav.ids[0]) update({ m: nav.ids[0] });
          break;
        case "k":
          if (mail && pos.prev) go(pos.prev);
          break;
        case "u":
        case "Escape":
          if (mail && reading) update({ m: "" });
          break;
        case "g":
          chord.current = Date.now();
          break;
        case "a":
          if (afterG) update({ account: "", folder: "", gone: "", m: "", view: "" });
          break;
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  });

  const gone = accounts.data?.accounts.reduce((n, a) => n + a.goneMessages, 0) ?? 0;
  const pageInert = hide;

  return (
    <div className="app-shell">
      {/* Usable next to the SideNav overlay (its menu button closes it); inert behind the folder overlay. */}
      {/* Header passes inert on to its <header>; its types only lack the attribute. */}
      <Header aria-label={t.appName} {...({ inert: foldersOpen || undefined } as object)}>
        {/* Below lg (Carbon's breakpoint) it opens the SideNav; Carbon hides it from lg up. */}
        <HeaderMenuButton
          aria-label={menuOpen ? t.hideMenu : t.showMenu}
          aria-controls={navId}
          aria-expanded={menuOpen}
          isActive={menuOpen}
          // Never from behind the folder overlay (inert there anyway): one overlay does not open another.
          onClick={() => !foldersOpen && setMenuOpen((open) => !open)}
        />
        <HeaderName
          href="/"
          prefix=""
          onClick={(e: MouseEvent) => {
            // All mail, as "g a" does. A Ctrl/Cmd, Shift or Alt click is left
            // to the browser, so it still opens "/" in a new tab or window.
            if (!isPlainClick(e)) return;
            e.preventDefault();
            update({ account: "", folder: "", gone: "", m: "", view: "" });
            setMenuOpen(false);
          }}
        >
          <img src={logo} alt="" className="app-header__logo" />
          {t.appName}
        </HeaderName>
        <HeaderGlobalBar>
          <span className="app-header__user" aria-hidden>
            {user.name}
          </span>
          <HeaderGlobalAction className="app-header__shortcuts" aria-label={t.shortcuts} onClick={() => setSheet("shortcuts")}>
            <Keyboard size={20} />
          </HeaderGlobalAction>
          <ThemeToggle header />
          <HeaderGlobalAction
            aria-label={t.profileOf(user.name)}
            isActive={state.view === "profile"}
            onClick={() => show("profile")}
          >
            <UserAvatar size={20} />
          </HeaderGlobalAction>
          <HeaderGlobalAction aria-label={t.logOut} tooltipAlignment="end" onClick={logout}>
            <Logout size={20} />
          </HeaderGlobalAction>
        </HeaderGlobalBar>
      </Header>
      <ShellNav
        ref={sideNav}
        id={navId}
        state={state}
        admin={user.admin}
        gone={gone}
        rail={wide}
        expanded={menuOpen && !wide}
        onClose={() => setMenuOpen(false)}
        go={(patch) => {
          update(patch);
          setFoldersOpen(false);
          setSheet("");
        }}
      />
      <div className="app-shell__body">
      <div inert={pageInert}>
        <HealthBanner
          data={accounts.data}
          admin={user.admin}
          openAccounts={() => update({ view: "accounts", m: "" })}
          openUsers={() => update({ view: "users", m: "" })}
        />
      </div>
      {/* The views not converted to Carbon yet (#168 parts 4b to 6) carry
          .legacy each, for the reset in index.css; the converted markup
          (Accounts, the folder column) does not, so the reset never reaches
          Carbon's. */}
      {state.view === "profile" ? (
        <main inert={pageInert} className="legacy text-ink app-view">
          <ProfilePage name={user.name} close={() => update({ view: "" })} />
        </main>
      ) : state.view === "users" && user.admin ? (
        <main inert={pageInert} className="legacy text-ink app-view">
          <UsersPage close={() => update({ view: "" })} />
        </main>
      ) : state.view === "retention" && user.admin ? (
        <main inert={pageInert} className="legacy text-ink app-view">
          <RetentionPage close={() => update({ view: "" })} />
        </main>
      ) : state.view === "log" ? (
        <main inert={pageInert} className="legacy text-ink app-view">
          <LogPage admin={user.admin} close={() => update({ view: "" })} open={(m) => update({ view: "", m })} />
        </main>
      ) : state.view === "accounts" ? (
        <main inert={pageInert} className="app-view">
          <AccountsPage
            admin={user.admin}
            accounts={accounts}
            renamed={(from, to) => {
              collapsed.rename(from, to);
              if (state.account === from) update({ account: to }, { replace: true });
            }}
            showGone={(account) => update({ account, folder: "", gone: "1", m: "", view: "" })}
          />
        </main>
      ) : (
        <div
          className="app-view mail-view"
          // Only the lg template uses it; no transition, so nothing animates on load or while dragging.
          style={{ "--sidebar-w": `${layout.collapsed ? railWidth : layout.width}rem` } as CSSProperties}
        >
          {/* Folder column from lg up; below lg an overlay, a modal dialog. */}
          <aside
            ref={aside}
            id={asideId}
            className={foldersOpen ? "folder-pane folder-pane--overlay" : "folder-pane"}
            inert={menuOpen || undefined}
            role={foldersOpen ? "dialog" : undefined}
            aria-modal={foldersOpen || undefined}
            aria-label={foldersOpen ? t.navFolders : undefined}
            onKeyDownCapture={foldersKey}
          >
            <SidebarPanel
              layout={layout}
              controls={asideId}
              state={state}
              accounts={accounts}
              collapsed={collapsed}
              select={(account, folder) => {
                update({ account, folder, gone: "", m: "", view: "" });
                setFoldersOpen(false);
              }}
              show={show}
              onClose={closeFolders}
            />
          </aside>
          {foldersOpen && <div aria-hidden className="folder-pane__scrim" onClick={closeFolders} />}

          <section inert={pageInert} className={`legacy text-ink min-h-0 flex-col border-r border-line ${reading ? "hidden md:flex" : "flex"}`}>
            {/* Phones: where the user is, the search below at full width. Menu
                and theme are in the header. */}
            <div className="flex min-h-12 shrink-0 flex-col justify-center border-b border-line px-3 py-1 md:hidden">
              <h1 className="truncate text-lg leading-6 font-semibold">{place.title}</h1>
              {place.sub && <p className="truncate text-xs text-ink-muted">{place.sub}</p>}
            </div>
            <div className="flex items-center gap-2 border-b border-line p-2">
              {/* Below lg: opens the folder column as an overlay. */}
              <div className="folder-pane__open">
                <IconButton
                  kind="ghost"
                  size="md"
                  align="bottom-start"
                  label={t.navFolders}
                  aria-haspopup="dialog"
                  aria-expanded={foldersOpen}
                  aria-controls={asideId}
                  // Never from behind the SideNav overlay (inert there anyway).
                  onClick={() => !menuOpen && setFoldersOpen(true)}
                >
                  <Folders />
                </IconButton>
              </div>
              <input
                ref={search}
                type="search"
                aria-label={t.searchMail}
                placeholder={
                  state.gone === "1" ? t.searchIn(t.onlyInArchive) : state.account ? t.searchIn(state.folder || state.account) : t.searchAll
                }
                className="w-full rounded-md border border-line-control bg-field px-3 py-1.5 text-sm pointer-coarse:min-h-11"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
              />
              <button
                type="button"
                aria-label={t.filters}
                title={t.filters}
                aria-haspopup="dialog"
                className={`${iconButton} md:hidden ${prefixed ? "text-accent-ink" : ""}`}
                onClick={() => setSheet("filters")}
              >
                <FilterIcon />
              </button>
            </div>
            <FilterChips query={query} setQuery={setQuery} group={state.group === "1"} setGroup={setGroup} openFilters={() => setSheet("filters")} />
            <div className="hidden md:block">
              <SearchFilters query={query} setQuery={setQuery} group={state.group === "1"} setGroup={setGroup} />
            </div>
            <div className="min-h-0 flex-1">
              <MessageList
                filter={filter}
                selected={state.m}
                onNav={onNav}
                clearSearch={() => {
                  setQuery("");
                  update({ q: "" }, { replace: true });
                }}
                searchEverywhere={state.account || state.gone === "1" ? () => update({ account: "", folder: "", gone: "" }) : undefined}
                // Commit a pending search first (same history entry), then open
                // the message as a new entry: the debounced update cannot close it
                // again, and Back returns to the search results.
                open={(m) => {
                  if (typed !== state.q) update({ q: typed }, { replace: true });
                  update({ m });
                }}
              />
            </div>
          </section>

          <main inert={pageInert} className={`legacy text-ink min-h-0 flex-col ${reading ? "flex" : "hidden md:flex"}`}>
            {reading ? (
              <>
                {/* Back on phones, previous and next everywhere (when the message is in the list). */}
                <div className={`flex h-14 shrink-0 items-center gap-1 border-b border-line px-1 md:h-11 md:px-2 ${pos.index < 0 ? "md:hidden" : ""}`}>
                  <button
                    type="button"
                    aria-label={t.backToList}
                    title={t.backToList}
                    className={`${iconButton} md:hidden`}
                    onClick={() => update({ m: "" })}
                  >
                    <BackIcon />
                  </button>
                  {pos.index >= 0 && (
                    <div className="ml-auto flex items-center">
                      <button type="button" aria-label={t.previousMessage} title={t.previousMessage} className={iconButton} disabled={!pos.prev} onClick={() => pos.prev && go(pos.prev)}>
                        <UpIcon />
                      </button>
                      <span className="px-1 text-xs text-ink-muted tabular-nums">{t.messagePosition(pos.index + 1, nav.ids.length, nav.more)}</span>
                      <button
                        type="button"
                        aria-label={t.nextMessage}
                        title={t.nextMessage}
                        className={iconButton}
                        disabled={!pos.next && !pos.nextNeedsMore}
                        onClick={next}
                      >
                        <DownIcon />
                      </button>
                    </div>
                  )}
                </div>
                <div className="min-h-0 flex-1">
                  <MessageView id={state.m} open={(m) => update({ m })} />
                </div>
              </>
            ) : (
              <div className="m-auto flex flex-col items-center gap-3 p-6 text-center">
                <img src={logo} alt="" className="size-12 opacity-80" />
                <p className="text-sm text-ink-muted">{t.selectMessage}</p>
                <button type="button" className="hidden text-xs text-ink-muted hover:text-ink pointer-fine:block" onClick={() => setSheet("shortcuts")}>
                  {t.shortcutsHint} <kbd className={kbd}>?</kbd>
                </button>
              </div>
            )}
          </main>
        </div>
      )}

      <div className="legacy text-ink contents">
      <Sheet open={sheet === "filters"} onClose={closeSheet} title={t.filters}>
        <SearchFilters query={query} setQuery={setQuery} group={state.group === "1"} setGroup={setGroup} />
        <button type="button" className={`${primary} self-end`} onClick={closeSheet}>
          {t.done}
        </button>
      </Sheet>
      <Sheet open={sheet === "shortcuts"} onClose={closeSheet} title={t.shortcuts}>
        <dl className="grid grid-cols-[auto_1fr] items-center gap-x-4 gap-y-2 text-sm">
          {(
            [
              [["/"], t.keySearch],
              [["j"], t.keyNext],
              [["k"], t.keyPrevious],
              [["u", "Esc"], t.keyBack],
              [["g", "a"], t.keyAllMail],
              [["?"], t.keyHelp],
            ] as const
          ).map(([keys, label]) => (
            <div key={label} className="contents">
              <dt className="flex gap-1">
                {keys.map((k) => (
                  <kbd key={k} className={kbd}>
                    {k}
                  </kbd>
                ))}
              </dt>
              <dd className="text-ink-muted">{label}</dd>
            </div>
          ))}
        </dl>
      </Sheet>
      </div>
      </div>
    </div>
  );
}

const kbd = "inline-grid min-w-6 place-items-center rounded border border-b-2 border-line-control bg-bg px-1.5 font-mono text-xs leading-5";
