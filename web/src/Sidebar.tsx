import { listAccounts } from "./api";
import { useAsync } from "./useAsync";
import type { ViewState } from "./urlState";
import { t } from "./i18n";

interface Props {
  state: ViewState;
  select: (account: string, folder: string) => void;
}

const item = "flex w-full items-center justify-between gap-2 rounded-md px-2 py-1 text-left text-sm";
const active = "bg-blue-600 text-white";
const idle = "hover:bg-zinc-200 dark:hover:bg-zinc-800";

export function Sidebar({ state, select }: Props) {
  const res = useAsync((signal) => listAccounts(signal), []);
  const all = !state.account && !state.folder;
  return (
    <nav aria-label={t.accountsAndFolders} className="flex h-full flex-col gap-3 overflow-y-auto p-3">
      <button type="button" className={`${item} font-medium ${all ? active : idle}`} onClick={() => select("", "")}>
        {t.allMail}
      </button>
      {res.status === "loading" && <p className="px-2 text-sm text-zinc-500">{t.loading}</p>}
      {res.status === "error" && <p className="px-2 text-sm text-red-600">{t.loadAccountsFailed(res.error.message)}</p>}
      {res.status === "ok" && res.data.accounts.length === 0 && (
        <p className="px-2 text-sm text-zinc-500">{t.noAccounts}</p>
      )}
      {res.status === "ok" &&
        res.data.accounts.map((a) => (
          <div key={a.name}>
            <button
              type="button"
              className={`${item} font-semibold ${state.account === a.name && !state.folder ? active : idle}`}
              onClick={() => select(a.name, "")}
            >
              <span className="truncate">{a.name}</span>
              {!a.enabled && <span className="text-xs opacity-70">{t.disabled}</span>}
            </button>
            <ul className="mt-1 ml-2 flex flex-col gap-0.5">
              {a.folders.map((f) => {
                const on = state.account === a.name && state.folder === f.name;
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
    </nav>
  );
}
