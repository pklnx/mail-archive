import { useCallback, useEffect, useState } from "react";
import { AccountsPage } from "./AccountsPage";
import { HealthBanner } from "./HealthBanner";
import { ProfilePage } from "./Profile";
import { UsersPage } from "./UsersPage";
import { MessageList } from "./MessageList";
import { LogPage } from "./LogPage";
import { RetentionPage } from "./RetentionPage";
import { MessageView } from "./MessageView";
import { SearchFilters } from "./SearchFilters";
import { hasPrefix, searchFilter } from "./searchSyntax";
import { Sidebar } from "./Sidebar";
import { ThemeToggle } from "./ThemeToggle";
import { BackIcon, DownIcon, MenuIcon, UpIcon, iconButton } from "./icons";
import { noList, pager, type ListNav } from "./pager";
import logo from "./logo.svg";
import { useAccounts } from "./useAccounts";
import { useCollapsed } from "./sidebarState";
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
  // On small screens the filter row is folded away until needed.
  const prefixed = hasPrefix(query) || state.group === "1";
  const [filtersOpen, setFiltersOpen] = useState(prefixed);
  const accounts = useAccounts();
  const collapsed = useCollapsed(user.name, accounts.data);

  // A typed prefix or grouping (also from the URL) shows the filters it set.
  useEffect(() => {
    if (prefixed) setFiltersOpen(true);
  }, [prefixed]);

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

  return (
    <div className="flex h-full flex-col">
      <HealthBanner
        data={accounts.data}
        admin={user.admin}
        openAccounts={() => update({ view: "accounts", m: "" })}
        openUsers={() => update({ view: "users", m: "" })}
      />
      <div className="grid min-h-0 flex-1 grid-cols-1 md:grid-cols-[minmax(18rem,26rem)_1fr] lg:grid-cols-[14rem_minmax(18rem,26rem)_1fr]">
        {/* Sidebar: always visible on large screens, a drawer on small ones. */}
        <aside
          className={`border-r border-line bg-surface-sunken ${
            menuOpen
              ? "fixed inset-y-0 left-0 z-20 w-64 pt-[env(safe-area-inset-top)] pb-[env(safe-area-inset-bottom)] pl-[env(safe-area-inset-left)] shadow-xl"
              : "hidden"
          } lg:static lg:block lg:w-auto lg:shadow-none`}
        >
          <Sidebar
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
            show={(view) => {
              update({ view, m: "" });
              setMenuOpen(false);
            }}
          />
        </aside>
        {menuOpen && <div className="fixed inset-0 z-10 bg-scrim lg:hidden" onClick={() => setMenuOpen(false)} />}

        {state.view === "profile" ? (
          <main className="min-h-0 md:col-span-2">
            <ProfilePage name={user.name} close={() => update({ view: "" })} />
          </main>
        ) : state.view === "users" && user.admin ? (
          <main className="min-h-0 md:col-span-2">
            <UsersPage close={() => update({ view: "" })} />
          </main>
        ) : state.view === "retention" && user.admin ? (
          <main className="min-h-0 md:col-span-2">
            <RetentionPage close={() => update({ view: "" })} />
          </main>
        ) : state.view === "log" ? (
          <main className="min-h-0 md:col-span-2">
            <LogPage admin={user.admin} close={() => update({ view: "" })} open={(m) => update({ view: "", m })} />
          </main>
        ) : state.view === "accounts" ? (
          <main className="min-h-0 md:col-span-2">
            <AccountsPage
              admin={user.admin}
              accounts={accounts}
              close={() => update({ view: "" })}
              renamed={(from, to) => {
                collapsed.rename(from, to);
                if (state.account === from) update({ account: to }, { replace: true });
              }}
              showGone={(account) => update({ account, folder: "", gone: "1", m: "", view: "" })}
            />
          </main>
        ) : (
          <>
          <section className={`min-h-0 flex-col border-r border-line ${reading ? "hidden md:flex" : "flex"}`}>
            {/* Phones: an app bar with the place, the search below at full width. */}
            <header className="flex h-14 shrink-0 items-center gap-1 border-b border-line px-1 md:hidden">
              <button type="button" aria-label={t.showMenu} title={t.showMenu} className={iconButton} onClick={() => setMenuOpen(true)}>
                <MenuIcon />
              </button>
              <div className="min-w-0 flex-1 px-1">
                <h1 className="truncate text-lg leading-6 font-semibold">{place.title}</h1>
                {place.sub && <p className="truncate text-xs text-ink-muted">{place.sub}</p>}
              </div>
              <ThemeToggle />
            </header>
            <div className="flex items-center gap-2 border-b border-line p-2">
              <button
                type="button"
                aria-label={t.showMenu}
                title={t.showMenu}
                className={`${iconButton} hidden md:flex lg:hidden`}
                onClick={() => setMenuOpen(true)}
              >
                <MenuIcon />
              </button>
              <input
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
                aria-expanded={filtersOpen}
                aria-controls="search-filters"
                className={`flex shrink-0 items-center justify-center rounded-md p-1.5 hover:bg-surface-hover md:hidden pointer-coarse:size-11 ${
                  prefixed ? "text-accent-ink" : "text-ink-muted"
                }`}
                onClick={() => setFiltersOpen((o) => !o)}
              >
                <svg aria-hidden width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
                  <path d="M3 5h18l-7 8.5V19l-4 2v-7.5z" />
                </svg>
              </button>
              <span className="hidden md:flex">
                <ThemeToggle />
              </span>
            </div>
            <div id="search-filters" className={filtersOpen ? "" : "hidden md:block"}>
              <SearchFilters
                query={query}
                setQuery={setQuery}
                group={state.group === "1"}
                setGroup={(on) => update({ group: on ? "1" : "" }, { replace: true })}
              />
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

          <main className={`min-h-0 flex-col ${reading ? "flex" : "hidden md:flex"}`}>
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
              </div>
            )}
          </main>
          </>
        )}
      </div>
    </div>
  );
}
