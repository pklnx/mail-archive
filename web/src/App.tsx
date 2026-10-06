import { useEffect, useState } from "react";
import { AccountsPage } from "./AccountsPage";
import { ProfilePage } from "./Profile";
import { UsersPage } from "./UsersPage";
import { MessageList } from "./MessageList";
import { MessageView } from "./MessageView";
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
  const accounts = useAccounts();

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

  const filter = { q: state.q.trim(), account: state.account, folder: state.folder };
  const reading = state.m !== "";

  return (
    <div className="grid h-full grid-cols-1 md:grid-cols-[minmax(18rem,26rem)_1fr] lg:grid-cols-[14rem_minmax(18rem,26rem)_1fr]">
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
            update({ account, folder, m: "", view: "" });
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
              placeholder={state.account ? t.searchIn(state.folder || state.account) : t.searchAll}
              className="w-full rounded-md border border-zinc-300 bg-white px-3 py-1.5 text-sm outline-none focus:border-blue-500 dark:border-zinc-700 dark:bg-zinc-900"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
            <ThemeToggle />
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
                <MessageView id={state.m} />
              </div>
            </>
          ) : (
            <p className="m-auto text-sm text-zinc-500">{t.selectMessage}</p>
          )}
        </main>
        </>
      )}
    </div>
  );
}
