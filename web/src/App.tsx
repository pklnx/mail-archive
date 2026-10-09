import { useEffect, useState } from "react";
import { AccountsPage } from "./AccountsPage";
import { HealthBanner } from "./HealthBanner";
import { ProfilePage } from "./Profile";
import { UsersPage } from "./UsersPage";
import { MessageList } from "./MessageList";
import { MessageView } from "./MessageView";
import { SearchFilters } from "./SearchFilters";
import { hasPrefix, searchFilter } from "./searchSyntax";
import { Sidebar } from "./Sidebar";
import { ThemeToggle } from "./ThemeToggle";
import { useAccounts } from "./useAccounts";
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
          className={`border-r border-zinc-200 bg-zinc-50 dark:border-zinc-800 dark:bg-zinc-900 ${
            menuOpen ? "fixed inset-y-0 left-0 z-20 w-64 shadow-xl" : "hidden"
          } lg:static lg:block lg:w-auto lg:shadow-none`}
        >
          <Sidebar
            state={state}
            accounts={accounts}
            user={user}
            logout={logout}
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
        {menuOpen && <div className="fixed inset-0 z-10 bg-black/30 lg:hidden" onClick={() => setMenuOpen(false)} />}

        {state.view === "profile" ? (
          <main className="min-h-0 md:col-span-2">
            <ProfilePage name={user.name} close={() => update({ view: "" })} />
          </main>
        ) : state.view === "users" && user.admin ? (
          <main className="min-h-0 md:col-span-2">
            <UsersPage close={() => update({ view: "" })} />
          </main>
        ) : state.view === "accounts" ? (
          <main className="min-h-0 md:col-span-2">
            <AccountsPage
              accounts={accounts}
              close={() => update({ view: "" })}
              renamed={(from, to) => {
                if (state.account === from) update({ account: to }, { replace: true });
              }}
              showGone={(account) => update({ account, folder: "", gone: "1", m: "", view: "" })}
            />
          </main>
        ) : (
          <>
          <section className={`min-h-0 flex-col border-r border-zinc-200 dark:border-zinc-800 ${reading ? "hidden md:flex" : "flex"}`}>
            <div className="flex items-center gap-2 border-b border-zinc-200 p-2 dark:border-zinc-800">
              <button
                type="button"
                aria-label={t.showMenu}
                className="rounded px-2 py-1 text-lg lg:hidden"
                onClick={() => setMenuOpen(true)}
              >
                ☰
              </button>
              <input
                type="search"
                aria-label={t.searchMail}
                placeholder={
                  state.gone === "1" ? t.searchIn(t.onlyInArchive) : state.account ? t.searchIn(state.folder || state.account) : t.searchAll
                }
                className="w-full rounded-md border border-zinc-300 bg-white px-3 py-1.5 text-sm outline-none focus:border-blue-500 dark:border-zinc-700 dark:bg-zinc-900"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
              />
              <button
                type="button"
                aria-label={t.filters}
                title={t.filters}
                aria-expanded={filtersOpen}
                aria-controls="search-filters"
                className={`shrink-0 rounded-md p-1.5 hover:bg-zinc-200 dark:hover:bg-zinc-800 md:hidden ${
                  prefixed ? "text-blue-600 dark:text-blue-400" : "text-zinc-500 dark:text-zinc-400"
                }`}
                onClick={() => setFiltersOpen((o) => !o)}
              >
                <svg aria-hidden width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
                  <path d="M3 5h18l-7 8.5V19l-4 2v-7.5z" />
                </svg>
              </button>
              <ThemeToggle />
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
                <button type="button" className="p-2 text-left text-sm text-blue-600 md:hidden" onClick={() => update({ m: "" })}>
                  {t.back}
                </button>
                <div className="min-h-0 flex-1">
                  <MessageView id={state.m} open={(m) => update({ m })} />
                </div>
              </>
            ) : (
              <p className="m-auto text-sm text-zinc-500">{t.selectMessage}</p>
            )}
          </main>
          </>
        )}
      </div>
    </div>
  );
}
