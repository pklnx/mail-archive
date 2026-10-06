import { useCallback, useEffect, useState, type FormEvent } from "react";
import { usersApi, type GeneratedPassword, type ManagedUser } from "./api";
import { relativeTime } from "./format";
import { t } from "./i18n";

const button =
  "rounded-md border border-zinc-300 px-2.5 py-1 text-sm hover:bg-zinc-100 disabled:opacity-50 dark:border-zinc-700 dark:hover:bg-zinc-800";
const primary = "rounded-md bg-blue-600 px-3 py-1 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50";
const input =
  "w-full rounded-md border border-zinc-300 bg-white px-2.5 py-1.5 text-sm outline-none focus:border-blue-500 dark:border-zinc-700 dark:bg-zinc-900";

const message = (err: unknown) => t.failed(err instanceof Error ? err.message : String(err));

/** User management for admins: logins only, never other users' mail. */
export function UsersPage({ close }: { close: () => void }) {
  const [users, setUsers] = useState<ManagedUser[] | null>(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [generated, setGenerated] = useState<GeneratedPassword | null>(null);

  const reload = useCallback(() => {
    usersApi.list().then(
      (u) => {
        setUsers(u);
        setError("");
      },
      (err: unknown) => setError(message(err)),
    );
  }, []);
  useEffect(reload, [reload]);

  const run = async (action: () => Promise<unknown>) => {
    setError("");
    try {
      await action();
    } catch (err) {
      setError(message(err));
    }
    reload();
  };

  return (
    <div className="mx-auto flex h-full max-w-3xl flex-col gap-4 overflow-y-auto p-4">
      <button type="button" className="self-start text-sm text-blue-600 lg:hidden" onClick={close}>
        {t.backToMail}
      </button>
      <div className="flex flex-wrap items-center gap-2">
        <h1 className="mr-auto text-xl font-semibold">{t.usersTitle}</h1>
        {!adding && (
          <button type="button" className={primary} onClick={() => setAdding(true)}>
            {t.addUser}
          </button>
        )}
      </div>
      <p className="text-sm text-zinc-500">{t.adminsSeeNoMail}</p>

      {generated && <GeneratedPanel value={generated} done={() => setGenerated(null)} />}
      {adding && (
        <AddUserForm
          cancel={() => setAdding(false)}
          added={(g) => {
            setAdding(false);
            setGenerated(g);
            reload();
          }}
        />
      )}
      {error && (
        <p role="alert" className="text-sm text-red-600">
          {error}
        </p>
      )}
      {!users && !error && <p className="text-sm text-zinc-500">{t.loading}</p>}

      <ul className="flex flex-col gap-3">
        {users?.map((u) => (
          <li key={u.name} className="flex flex-col gap-2 rounded-lg border border-zinc-200 p-3 dark:border-zinc-800">
            <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
              <span className="font-semibold">{u.name}</span>
              {u.self && <span className="text-sm text-zinc-500">({t.you})</span>}
              <span className="text-sm text-zinc-500">{u.admin ? t.roleAdmin : t.roleUser}</span>
              <span className={`text-sm ${u.locked ? "text-red-600" : "text-zinc-500"}`}>
                {u.locked ? t.stateLocked : u.mustChangePassword ? t.stateMustChange : t.stateActive}
              </span>
              <span className="ml-auto text-sm text-zinc-500">
                {t.colAccounts}: {u.accounts} · {t.colLastLogin}: {u.lastLoginAt ? relativeTime(u.lastLoginAt) : t.never}
              </span>
            </div>
            {u.self ? (
              <p className="text-sm text-zinc-500">{t.selfHint}</p>
            ) : (
              <div className="flex flex-wrap gap-2">
                <button
                  type="button"
                  className={button}
                  onClick={() => window.confirm(t.confirmResetPassword(u.name)) && run(async () => setGenerated(await usersApi.resetPassword(u.name)))}
                >
                  {t.resetPassword}
                </button>
                <button
                  type="button"
                  className={button}
                  onClick={() => (u.locked || window.confirm(t.confirmLock(u.name))) && run(() => usersApi.setLocked(u.name, !u.locked))}
                >
                  {u.locked ? t.unlock : t.lock}
                </button>
                <button type="button" className={button} onClick={() => run(() => usersApi.setAdmin(u.name, !u.admin))}>
                  {u.admin ? t.removeAdmin : t.makeAdmin}
                </button>
                <button
                  type="button"
                  className={`${button} text-red-600`}
                  onClick={() => window.confirm(t.confirmRemoveUser(u.name)) && run(() => usersApi.remove(u.name))}
                >
                  {t.removeUser}
                </button>
              </div>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}

function AddUserForm({ cancel, added }: { cancel: () => void; added: (g: GeneratedPassword) => void }) {
  const [name, setName] = useState("");
  const [admin, setAdmin] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      added(await usersApi.create(name, admin));
    } catch (err) {
      setError(message(err));
      setBusy(false);
    }
  };

  return (
    <form className="flex flex-col gap-3 rounded-lg border border-zinc-200 p-3 text-sm dark:border-zinc-800" onSubmit={submit}>
      <label className="flex flex-col gap-1">
        {t.userName}
        <input className={input} required autoFocus autoCapitalize="none" spellCheck={false} value={name} onChange={(e) => setName(e.target.value)} />
      </label>
      <label className="flex items-center gap-2">
        <input type="checkbox" checked={admin} onChange={(e) => setAdmin(e.target.checked)} />
        {t.userIsAdmin}
      </label>
      {error && (
        <p role="alert" className="text-red-600">
          {error}
        </p>
      )}
      <div className="flex gap-2">
        <button type="submit" className={primary} disabled={busy}>
          {t.addUser}
        </button>
        <button type="button" className={button} onClick={cancel}>
          {t.cancel}
        </button>
      </div>
    </form>
  );
}

/** Shows a generated password once, with a copy button. */
function GeneratedPanel({ value, done }: { value: GeneratedPassword; done: () => void }) {
  const [copied, setCopied] = useState(false);
  return (
    <div role="status" className="flex flex-col gap-2 rounded-lg border border-amber-300 bg-amber-50 p-3 text-sm dark:border-amber-700 dark:bg-amber-950">
      <p className="font-semibold">{t.generatedFor(value.name)}</p>
      <code className="w-fit rounded bg-white px-2 py-1 font-mono text-base select-all dark:bg-zinc-900">{value.password}</code>
      <p>{t.generatedHint}</p>
      <div className="flex gap-2">
        <button
          type="button"
          className={button}
          onClick={() => navigator.clipboard?.writeText(value.password).then(() => setCopied(true), () => setCopied(false))}
        >
          {copied ? t.copied : t.copy}
        </button>
        <button type="button" className={primary} onClick={done}>
          {t.done}
        </button>
      </div>
    </div>
  );
}
