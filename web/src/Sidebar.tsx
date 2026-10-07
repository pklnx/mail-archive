import { t } from "./i18n";
import type { AccountsState } from "./useAccounts";
import type { ViewState } from "./urlState";
import type { User } from "./api";

interface Props {
  state: ViewState;
  accounts: AccountsState;
  select: (account: string, folder: string) => void;
  /** Opens a page: "accounts", "users" or "profile". */
  show: (view: string) => void;
  user: User;
  logout: () => void;
}

const item = "flex w-full items-center justify-between gap-2 rounded-md px-2 py-1 text-left text-sm";
const active = "bg-blue-600 text-white";
const idle = "hover:bg-zinc-200 dark:hover:bg-zinc-800";

export function Sidebar({ state, accounts, select, show, user, logout }: Props) {
  const { data, error } = accounts;
  const mail = state.view === "";
  const all = mail && !state.account && !state.folder;
  return (
    <nav aria-label={t.accountsAndFolders} className="flex h-full flex-col gap-3 overflow-y-auto p-3">
      <button type="button" className={`${item} font-medium ${all ? active : idle}`} onClick={() => select("", "")}>
        {t.allMail}
      </button>
      {!data && !error && <p className="px-2 text-sm text-zinc-500">{t.loading}</p>}
      {error && !data && <p className="px-2 text-sm text-red-600">{t.loadAccountsFailed(error)}</p>}
      {data && data.accounts.length === 0 && <p className="px-2 text-sm text-zinc-500">{t.noAccounts}</p>}
      {data?.accounts.map((a) => (
        <div key={a.name}>
          <button
            type="button"
            className={`${item} font-semibold ${mail && state.account === a.name && !state.folder ? active : idle}`}
            onClick={() => select(a.name, "")}
          >
            <span className="truncate">{a.name}</span>
            {a.sync.state !== "idle" ? (
              <span className="text-xs font-normal opacity-70" title={a.kind === "import" ? t.importing : t.syncing}>
                <span aria-hidden className="inline-block animate-spin">↻</span>
                <span className="sr-only">{a.kind === "import" ? t.importing : t.syncing}</span>
              </span>
            ) : a.removed ? (
              <span className="text-xs font-normal opacity-70">{t.removed}</span>
            ) : a.kind === "import" ? (
              <span className="text-xs font-normal opacity-70">{t.imported}</span>
            ) : (
              !a.enabled && <span className="text-xs font-normal opacity-70">{t.disabled}</span>
            )}
          </button>
          <ul className="mt-1 ml-2 flex flex-col gap-0.5">
            {a.folders.map((f) => {
              const on = mail && state.account === a.name && state.folder === f.name;
              return (
                <li key={f.name}>
                  <button type="button" className={`${item} ${on ? active : idle}`} onClick={() => select(a.name, f.name)}>
                    <span className="truncate">{f.name}</span>
                    <span className={`text-xs tabular-nums ${on ? "" : "text-zinc-500"}`}>{f.messages}</span>
                  </button>
                </li>
              );
            })}
          </ul>
        </div>
      ))}
      <button
        type="button"
        className={`${item} mt-auto ${state.view === "accounts" ? active : idle}`}
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
      <div className="flex items-center justify-between gap-2 border-t border-zinc-200 px-2 pt-2 text-sm dark:border-zinc-800">
        <button
          type="button"
          className={`truncate hover:underline ${state.view === "profile" ? "font-semibold" : "text-zinc-500"}`}
          title={`${t.loggedInAs(user.name)} · ${t.profile}`}
          aria-current={state.view === "profile" ? "page" : undefined}
          onClick={() => show("profile")}
        >
          {user.name}
        </button>
        <button type="button" className="shrink-0 text-blue-600 hover:underline dark:text-blue-400" onClick={logout}>
          {t.logOut}
        </button>
      </div>
    </nav>
  );
}
