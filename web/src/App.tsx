import { useCallback, useEffect, useId, useRef, useState, type CSSProperties, type MouseEvent, type ReactNode } from "react";
import { Header, HeaderGlobalBar, HeaderMenuButton, HeaderName } from "@carbon/react";
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
import { ThemeToggle } from "./ThemeToggle";
import { BackIcon, DownIcon, FilterIcon, FolderIcon, InboxIcon, MoreIcon, ServerIcon, UpIcon, iconButton } from "./icons";
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
  const [menuOpen, setMenuOpen] = useState(false);
  // Phones show the filters as chips and a sheet; the filter button is
  // highlighted while a prefix or grouping is set.
  const prefixed = hasPrefix(query) || state.group === "1";
  const [sheet, setSheet] = useState<"" | "filters" | "more" | "shortcuts">("");
  const closeSheet = useCallback(() => setSheet(""), []);
  const accounts = useAccounts();
  const collapsed = useCollapsed(user.name, accounts.data);
  const layout = useSidebarLayout(user.name);
  const asideId = useId();

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
    setSheet("");
  };

  // The drawer: focus moves into it and back to where it was; the rest of
  // the page is inert meanwhile (see below), so Tab stays inside.
  const aside = useRef<HTMLElement>(null);
  const opener = useRef<HTMLElement | null>(null);
  useEffect(() => {
    if (menuOpen) {
      opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      // The drawer becomes visible as its transition starts, a frame later;
      // hidden elements cannot take focus.
      let frame = requestAnimationFrame(() => {
        frame = requestAnimationFrame(() => drawerFocusTarget(aside.current)?.focus());
      });
      return () => cancelAnimationFrame(frame);
    } else if (opener.current) {
      opener.current.focus();
      opener.current = null;
    }
  }, [menuOpen]);
  const hide = menuOpen || undefined;

  // Keyboard shortcuts (? lists them). Not while typing or in a dialog
  // (the sheets, or a Carbon Modal).
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
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });

  return (
    <div className="app-shell">
      {/* Stays usable while the drawer is open: its menu button closes it. */}
      <Header aria-label={t.appName}>
        {/* Below lg it opens the drawer with accounts and folders (#168 part 4a brings Carbon's SideNav). */}
        <HeaderMenuButton
          aria-label={menuOpen ? t.hideMenu : t.showMenu}
          aria-controls={asideId}
          aria-expanded={menuOpen}
          isActive={menuOpen}
          onClick={() => setMenuOpen((open) => !open)}
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
          <ThemeToggle header />
        </HeaderGlobalBar>
      </Header>
      <div className="app-shell__body">
      <div inert={hide}>
        <HealthBanner
          data={accounts.data}
          admin={user.admin}
          openAccounts={() => update({ view: "accounts", m: "" })}
          openUsers={() => update({ view: "users", m: "" })}
        />
      </div>
      {/* The views not converted to Carbon yet (#168 parts 4 to 6) carry
          .legacy each, for the reset in index.css; the converted Accounts
          page does not, so the reset never reaches its Carbon markup. */}
      <div className="flex min-h-0 flex-1 flex-col">
      <div
        className="grid min-h-0 flex-1 grid-cols-1 grid-rows-1 md:grid-cols-[minmax(18rem,26rem)_1fr] lg:grid-cols-[var(--sidebar-w)_minmax(18rem,26rem)_1fr]"
        // Only the lg template uses it; no transition, so nothing animates on load or while dragging.
        style={{ "--sidebar-w": `${layout.collapsed ? railWidth : layout.width}rem` } as CSSProperties}
      >
        {/* Sidebar: always visible on large screens, a drawer on small ones. */}
        <aside
          ref={aside}
          id={asideId}
          // Below the fixed header, which keeps its menu button usable to close the drawer.
          className={`legacy text-ink fixed top-[calc(3rem+env(safe-area-inset-top))] bottom-0 left-0 z-20 w-64 border-r border-line bg-surface-sunken pb-[env(safe-area-inset-bottom)] pl-[env(safe-area-inset-left)] shadow-xl transition-[translate,visibility] duration-200 ease-out motion-reduce:transition-none ${
            menuOpen ? "visible translate-x-0" : "invisible -translate-x-full"
          } lg:visible lg:relative lg:top-0 lg:w-auto lg:min-w-0 lg:translate-x-0 lg:shadow-none lg:transition-none`}
        >
          <SidebarPanel
            layout={layout}
            controls={asideId}
            state={state}
            accounts={accounts}
            user={user}
            logout={logout}
            collapsed={collapsed}
            select={(account, folder) => {
              update({ account, folder, gone: "", m: "", view: "" });
              setMenuOpen(false);
            }}
            selectGone={() => {
              update({ account: "", folder: "", gone: "1", m: "", view: "" });
              setMenuOpen(false);
            }}
            show={show}
          />
        </aside>
        <div
          aria-hidden
          className={`fixed inset-0 z-10 bg-scrim transition-opacity duration-200 motion-reduce:transition-none lg:hidden ${menuOpen ? "opacity-100" : "pointer-events-none opacity-0"}`}
          onClick={() => setMenuOpen(false)}
        />

        {state.view === "profile" ? (
          <main inert={hide} className="legacy text-ink min-h-0 md:col-span-2">
            <ProfilePage name={user.name} close={() => update({ view: "" })} />
          </main>
        ) : state.view === "users" && user.admin ? (
          <main inert={hide} className="legacy text-ink min-h-0 md:col-span-2">
            <UsersPage close={() => update({ view: "" })} />
          </main>
        ) : state.view === "retention" && user.admin ? (
          <main inert={hide} className="legacy text-ink min-h-0 md:col-span-2">
            <RetentionPage close={() => update({ view: "" })} />
          </main>
        ) : state.view === "log" ? (
          <main inert={hide} className="legacy text-ink min-h-0 md:col-span-2">
            <LogPage admin={user.admin} close={() => update({ view: "" })} open={(m) => update({ view: "", m })} />
          </main>
        ) : state.view === "accounts" ? (
          <main inert={hide} className="min-h-0 md:col-span-2">
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
          <>
          <section inert={hide} className={`legacy text-ink min-h-0 flex-col border-r border-line ${reading ? "hidden md:flex" : "flex"}`}>
            {/* Phones: where the user is, the search below at full width. Menu
                and theme are in the header. */}
            <div className="flex min-h-12 shrink-0 flex-col justify-center border-b border-line px-3 py-1 md:hidden">
              <h1 className="truncate text-lg leading-6 font-semibold">{place.title}</h1>
              {place.sub && <p className="truncate text-xs text-ink-muted">{place.sub}</p>}
            </div>
            <div className="flex items-center gap-2 border-b border-line p-2">
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

          <main inert={hide} className={`legacy text-ink min-h-0 flex-col ${reading ? "flex" : "hidden md:flex"}`}>
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
          </>
        )}
      </div>

      {/* Phones: the main places within thumb reach; hidden while reading. */}
      {!(reading && state.view === "") && (
        <nav inert={hide} aria-label={t.mainNavigation} className="legacy text-ink grid h-14 shrink-0 grid-cols-4 border-t border-line bg-bg md:hidden">
          <NavButton icon={<InboxIcon />} label={t.navMail} on={state.view === ""} onClick={() => show("")} />
          <NavButton icon={<FolderIcon />} label={t.navFolders} on={menuOpen} onClick={() => setMenuOpen(true)} />
          <NavButton icon={<ServerIcon />} label={t.navAccounts} on={state.view === "accounts"} onClick={() => show("accounts")} />
          <NavButton icon={<MoreIcon />} label={t.navMore} on={["log", "users", "retention", "profile"].includes(state.view)} onClick={() => setSheet("more")} />
        </nav>
      )}

      <div className="legacy text-ink contents">
      <Sheet open={sheet === "filters"} onClose={closeSheet} title={t.filters}>
        <SearchFilters query={query} setQuery={setQuery} group={state.group === "1"} setGroup={setGroup} />
        <button type="button" className={`${primary} self-end`} onClick={closeSheet}>
          {t.done}
        </button>
      </Sheet>
      <Sheet open={sheet === "more"} onClose={closeSheet} title={t.navMore}>
        <ul className="-mx-2 flex flex-col">
          {[
            ["log", t.log],
            ...(user.admin ? [["users", t.users], ["retention", t.retention]] : []),
            ["profile", `${t.profile} (${user.name})`],
          ].map(([view, label]) => (
            <li key={view}>
              <button type="button" className={moreItem} onClick={() => show(view ?? "")}>
                {label}
              </button>
            </li>
          ))}
          <li className="flex items-center justify-between px-2">
            <span className="text-sm text-ink-muted">{t.appearance}</span>
            <ThemeToggle />
          </li>
          <li>
            <button type="button" className={`${moreItem} text-danger`} onClick={logout}>
              {t.logOut}
            </button>
          </li>
        </ul>
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
    </div>
  );
}

const kbd = "inline-grid min-w-6 place-items-center rounded border border-b-2 border-line-control bg-bg px-1.5 font-mono text-xs leading-5";
const moreItem = "flex min-h-11 w-full items-center rounded-md px-2 text-left hover:bg-surface-hover";

function NavButton({ icon, label, on, onClick }: { icon: ReactNode; label: string; on: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      aria-current={on ? "page" : undefined}
      className={`flex flex-col items-center justify-center gap-0.5 text-[11px] ${on ? "font-semibold text-accent-soft-ink" : "text-ink-muted"}`}
      onClick={onClick}
    >
      <span className={`grid h-7 w-13 place-items-center rounded-full transition-colors ${on ? "bg-accent-soft" : ""}`}>{icon}</span>
      {label}
    </button>
  );
}
