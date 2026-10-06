import { useId, useState, type FormEvent, type ReactNode } from "react";
import { accountsApi, folderQuestion, type Account, type AccountInput, type ServerFolder, type TLSMode } from "./api";
import { suggestName } from "./accountName";
import { formatCount, formatInterval, relativeTime } from "./format";
import { t } from "./i18n";
import type { AccountsState } from "./useAccounts";

const button =
  "rounded-md border border-zinc-300 px-2.5 py-1 text-sm hover:bg-zinc-100 disabled:opacity-50 dark:border-zinc-700 dark:hover:bg-zinc-800";
const primary = "rounded-md bg-blue-600 px-3 py-1 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50";
const input =
  "w-full rounded-md border border-zinc-300 bg-white px-2.5 py-1.5 text-sm outline-none focus:border-blue-500 dark:border-zinc-700 dark:bg-zinc-900";

type Editing = { mode: "new" } | { mode: "edit"; name: string } | null;

interface Props {
  accounts: AccountsState;
  close: () => void;
  /** Called after an account got a new name, to update links to it. */
  renamed: (from: string, to: string) => void;
}

export function AccountsPage({ accounts, close, renamed }: Props) {
  const { data, error, reload } = accounts;
  const [editing, setEditing] = useState<Editing>(null);
  const [actionError, setActionError] = useState("");

  const run = async (action: () => Promise<unknown>) => {
    setActionError("");
    try {
      await action();
    } catch (err) {
      setActionError(t.failed(err instanceof Error ? err.message : String(err)));
    }
    reload();
  };

  const manage = data?.manage ?? false;
  const active = data?.accounts.filter((a) => !a.removed) ?? [];
  const removed = data?.accounts.filter((a) => a.removed) ?? [];
  const editAccount = editing?.mode === "edit" ? active.find((a) => a.name === editing.name) : undefined;

  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto flex max-w-3xl flex-col gap-4 p-4">
        <button type="button" className="self-start text-sm text-blue-600 lg:hidden" onClick={close}>
          {t.backToMail}
        </button>
        <header className="flex flex-wrap items-center gap-2">
          <h1 className="mr-auto text-xl font-semibold">{t.accountsTitle}</h1>
          {manage && (
            <>
              <button type="button" className={button} disabled={active.length === 0} onClick={() => run(accountsApi.syncAll)}>
                {t.syncAll}
              </button>
              <button type="button" className={primary} onClick={() => setEditing({ mode: "new" })}>
                {t.addAccount}
              </button>
            </>
          )}
        </header>
        {data && (
          <p className="text-sm text-zinc-500">
            {!manage ? t.manageOff : data.syncInterval ? t.schedule(formatInterval(data.syncInterval)) : t.scheduleOff}
          </p>
        )}
        {error && !data && <p className="text-sm text-red-600">{t.loadAccountsFailed(error)}</p>}
        {actionError && (
          <p role="alert" className="text-sm text-red-600">
            {actionError}
          </p>
        )}

        {editing?.mode === "new" && (
          <AccountForm
            onCancel={() => setEditing(null)}
            onSaved={() => {
              setEditing(null);
              reload();
            }}
          />
        )}

        {data && active.length === 0 && editing === null && <p className="text-sm text-zinc-500">{t.noAccounts}</p>}

        {active.map((a) =>
          editAccount?.name === a.name ? (
            <Card key={a.name}>
              <AccountForm
                account={a}
                onCancel={() => setEditing(null)}
                onSaved={(name) => {
                  setEditing(null);
                  if (name !== a.name) renamed(a.name, name);
                  reload();
                }}
              />
              <FolderPicker account={a} onSaved={reload} />
            </Card>
          ) : (
            <AccountCard
              key={a.name}
              account={a}
              manage={manage}
              onSync={() => run(() => accountsApi.sync(a.name))}
              onEdit={() => setEditing({ mode: "edit", name: a.name })}
              onToggle={() => run(() => accountsApi.update(a.name, { enabled: !a.enabled }))}
              onRemove={() => {
                if (window.confirm(t.confirmRemove(a.name))) void run(() => accountsApi.remove(a.name));
              }}
            />
          ),
        )}

        {removed.length > 0 && (
          <section className="flex flex-col gap-2">
            <h2 className="mt-2 text-sm font-semibold text-zinc-500">{t.removedAccounts}</h2>
            {removed.map((a) => (
              <Card key={a.name}>
                <div className="flex flex-wrap items-baseline gap-x-3">
                  <span className="font-semibold">{a.name}</span>
                  <span className="text-sm text-zinc-500">{t.messageCount(messageCount(a), formatCount(messageCount(a)))}</span>
                </div>
                <p className="text-sm text-zinc-500">{t.removedNote}</p>
              </Card>
            ))}
          </section>
        )}
      </div>
    </div>
  );
}

function Card({ children }: { children: ReactNode }) {
  return (
    <div className="flex flex-col gap-2 rounded-lg border border-zinc-200 p-4 dark:border-zinc-800">{children}</div>
  );
}

function messageCount(a: Account): number {
  return a.folders.reduce((n, f) => n + f.messages, 0);
}

export function syncStatus(a: Account): { text: string; error?: string } {
  const run = a.sync.lastRun;
  if (a.sync.state === "queued") return { text: t.stateQueued };
  if (a.sync.state === "running") return { text: t.stateRunning(run?.fetched ?? 0, run?.new ?? 0) };
  if (!run) return { text: t.neverSynced };
  const when = relativeTime(run.finishedAt ?? run.startedAt);
  switch (run.status) {
    case "ok":
      return { text: t.lastOk(when, run.new) };
    case "partial":
      return { text: t.lastPartial(when), error: run.error };
    default:
      return { text: t.lastFailed(when), error: run.error };
  }
}

interface CardProps {
  account: Account;
  manage: boolean;
  onSync: () => void;
  onEdit: () => void;
  onToggle: () => void;
  onRemove: () => void;
}

function AccountCard({ account: a, manage, onSync, onEdit, onToggle, onRemove }: CardProps) {
  const status = syncStatus(a);
  const busy = a.sync.state !== "idle";
  return (
    <Card>
      <div className="flex flex-wrap items-baseline gap-x-3">
        <span className="font-semibold">{a.name}</span>
        <span className="min-w-0 truncate text-sm text-zinc-500">
          {a.username} · {a.host}
        </span>
        <span className="ml-auto text-sm text-zinc-500 tabular-nums">{t.messageCount(messageCount(a), formatCount(messageCount(a)))}</span>
      </div>
      <p className={`text-sm ${status.error ? "text-red-600" : "text-zinc-600 dark:text-zinc-400"}`} aria-live="polite">
        {busy && <span aria-hidden className="mr-1 inline-block animate-spin">↻</span>}
        {status.text}
      </p>
      {status.error && <p className="text-xs break-words text-red-600">{status.error}</p>}
      {!a.enabled && <p className="text-sm text-amber-700 dark:text-amber-500">{t.disabledNote}</p>}
      {manage && (
        <div className="flex flex-wrap gap-2">
          <button type="button" className={button} disabled={busy} onClick={onSync}>
            {t.syncNow}
          </button>
          <button type="button" className={button} onClick={onEdit}>
            {t.edit}
          </button>
          <button type="button" className={button} onClick={onToggle}>
            {a.enabled ? t.disable : t.enable}
          </button>
          <button type="button" className={`${button} text-red-600`} disabled={busy} onClick={onRemove}>
            {t.remove}
          </button>
        </div>
      )}
    </Card>
  );
}

const defaultPort: Record<TLSMode, number> = { tls: 993, starttls: 143, none: 143 };

interface FormProps {
  /** Editing an existing account; otherwise a new one. */
  account?: Account;
  onCancel: () => void;
  /** Called with the saved account's name. */
  onSaved: (name: string) => void;
}

function AccountForm({ account, onCancel, onSaved }: FormProps) {
  const [name, setName] = useState(account?.name ?? "");
  // Until the user types a name, a new account gets one suggested from the
  // login or the server.
  const [nameTouched, setNameTouched] = useState(!!account);
  const [host, setHost] = useState(account?.host ?? "");
  const [tls, setTLS] = useState<TLSMode>(account?.tls ?? "tls");
  const [port, setPort] = useState(account && account.port !== defaultPort[account.tls] ? String(account.port) : "");
  const [username, setUsername] = useState(account?.username ?? "");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const hintId = useId();

  const shownName = nameTouched ? name : suggestName(username, host);

  // Trash and spam folders the server reported; the user decides whether
  // to archive them before the account is saved.
  const [question, setQuestion] = useState<string[] | null>(null);

  const save = async (extra: AccountInput = {}) => {
    setBusy(true);
    setError("");
    setQuestion(null);
    const body: AccountInput = { host: host.trim(), tls, port: port ? Number(port) : defaultPort[tls], username: username.trim() };
    if (password) body.password = password;
    const newName = shownName.trim();
    try {
      if (account) await accountsApi.update(account.name, newName !== account.name ? { ...body, name: newName } : body);
      else await accountsApi.create({ ...body, name: newName, ...extra });
      onSaved(newName);
    } catch (err) {
      const folders = folderQuestion(err);
      if (folders) setQuestion(folders);
      else setError(err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    void save();
  };

  const field = "flex flex-col gap-1 text-sm";
  return (
    <form onSubmit={submit} className={account ? "flex flex-col gap-3" : "flex flex-col gap-3 rounded-lg border border-zinc-200 p-4 dark:border-zinc-800"}>
      <h2 className="font-semibold">{account ? account.name : t.addAccount}</h2>
      <div className={field}>
        <label className={field}>
          {t.fieldName}
          <input
            className={input}
            required
            maxLength={64}
            value={shownName}
            onChange={(e) => {
              setNameTouched(true);
              setName(e.target.value);
            }}
            placeholder="private"
            aria-describedby={`${hintId}-name`}
          />
        </label>
        <span id={`${hintId}-name`} className="text-xs text-zinc-500">
          {t.nameHint}
        </span>
      </div>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-[1fr_9rem_7rem]">
        <label className={field}>
          {t.fieldHost}
          <input className={input} required value={host} onChange={(e) => setHost(e.target.value)} placeholder="imap.mail.de" autoComplete="off" />
        </label>
        <label className={field}>
          {t.fieldTLS}
          <select className={input} value={tls} onChange={(e) => setTLS(e.target.value as TLSMode)}>
            <option value="tls">{t.tlsTLS}</option>
            <option value="starttls">{t.tlsSTARTTLS}</option>
            <option value="none">{t.tlsNone}</option>
          </select>
        </label>
        <label className={field}>
          {t.fieldPort}
          <input className={input} inputMode="numeric" pattern="[0-9]*" value={port} onChange={(e) => setPort(e.target.value)} placeholder={String(defaultPort[tls])} />
        </label>
      </div>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <label className={field}>
          {t.fieldUsername}
          <input className={input} required value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" />
        </label>
        <div className={field}>
          <label className={field}>
            {t.fieldPassword}
            <input
              className={input}
              type="password"
              required={!account}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder={account ? t.passwordKeep : ""}
              autoComplete="new-password"
              aria-describedby={`${hintId}-password`}
            />
          </label>
          <span id={`${hintId}-password`} className="text-xs text-zinc-500">
            {t.passwordHint}
          </span>
        </div>
      </div>
      {error && (
        <p role="alert" className="text-sm break-words text-red-600">
          {error}
        </p>
      )}
      {question && (
        <div role="alert" className="flex flex-col gap-2 rounded-md border border-amber-300 bg-amber-50 p-3 text-sm dark:border-amber-700 dark:bg-amber-950/40">
          <p>{t.trashSpamQuestion(question.join(", "))}</p>
          <div className="flex flex-wrap gap-2">
            <button type="button" className={primary} disabled={busy} onClick={() => save({ confirmFolders: true, excludedFolders: question })}>
              {t.saveWithout}
            </button>
            <button type="button" className={button} disabled={busy} onClick={() => save({ confirmFolders: true })}>
              {t.saveWithAll}
            </button>
          </div>
        </div>
      )}
      <div className="flex items-center gap-2">
        <button type="submit" className={primary} disabled={busy}>
          {t.save}
        </button>
        <button type="button" className={button} onClick={onCancel} disabled={busy}>
          {t.cancel}
        </button>
        {busy && <span className="text-sm text-zinc-500">{t.checkingLogin}</span>}
      </div>
    </form>
  );
}

function FolderPicker({ account, onSaved }: { account: Account; onSaved: () => void }) {
  const [folders, setFolders] = useState<ServerFolder[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");

  const load = async () => {
    setBusy(true);
    setMessage("");
    try {
      setFolders(await accountsApi.serverFolders(account.name));
    } catch (err) {
      setMessage(t.failed(err instanceof Error ? err.message : String(err)));
    }
    setBusy(false);
  };

  const save = async () => {
    if (!folders) return;
    setBusy(true);
    setMessage("");
    try {
      await accountsApi.update(account.name, { excludedFolders: folders.filter((f) => !f.selected).map((f) => f.name) });
      setMessage(t.saved);
      onSaved();
    } catch (err) {
      setMessage(t.failed(err instanceof Error ? err.message : String(err)));
    }
    setBusy(false);
  };

  return (
    <section className="flex flex-col gap-2 border-t border-zinc-200 pt-3 dark:border-zinc-800">
      <h3 className="text-sm font-semibold">{t.folders}</h3>
      {!folders ? (
        <button type="button" className={`${button} self-start`} disabled={busy} onClick={load}>
          {busy ? t.loading : t.loadFolders}
        </button>
      ) : (
        <>
          <p className="text-xs text-zinc-500">{t.foldersHint}</p>
          <ul className="flex flex-col gap-1">
            {folders.map((f, i) => (
              <li key={f.name}>
                <label className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={f.selected}
                    onChange={(e) => setFolders(folders.map((g, j) => (i === j ? { ...g, selected: e.target.checked } : g)))}
                  />
                  <span>{f.name}</span>
                  {f.specialUse && <span className="text-xs text-zinc-500">({t.role(f.specialUse)})</span>}
                </label>
              </li>
            ))}
          </ul>
          <button type="button" className={`${button} self-start`} disabled={busy} onClick={save}>
            {t.saveFolders}
          </button>
        </>
      )}
      {message && <p className="text-sm">{message}</p>}
    </section>
  );
}
