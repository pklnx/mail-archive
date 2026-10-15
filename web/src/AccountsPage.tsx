import { useId, useState, type FormEvent, type ReactNode } from "react";
import { accountsApi, folderQuestion, type Account, type AccountInput, type AccountsResponse, type ServerFolder, type TLSMode } from "./api";
import { suggestName } from "./accountName";
import { durationMinutes, formatCount, formatEvery, formatInterval, relativeTime, shortDate } from "./format";
import { t } from "./i18n";
import { PageHeader } from "./PageHeader";
import { button, input, primary } from "./formStyles";
import { FillForm, FillSection } from "./FillPanel";
import { postfixSetup } from "./journalSetup";
import type { AccountsState } from "./useAccounts";

/** "fill": the form that imports existing mail from IMAP into an smtp account. */
type Editing = { mode: "new" } | { mode: "edit" | "fill"; name: string } | null;

interface Props {
  accounts: AccountsState;
  /** Admins can add smtp accounts and change their addresses. */
  admin: boolean;
  close: () => void;
  /** Called after an account got a new name, to update links to it. */
  renamed: (from: string, to: string) => void;
  /** Lists an account's messages that are no longer on its server. */
  showGone: (account: string) => void;
}

export function AccountsPage({ accounts, admin, close, renamed, showGone }: Props) {
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
  const fillAccount = editing?.mode === "fill" ? active.find((a) => a.name === editing.name && a.kind === "smtp") : undefined;
  const done = () => {
    setEditing(null);
    reload();
  };

  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto flex max-w-3xl flex-col gap-4 p-4">
        <PageHeader title={t.accountsTitle} close={close}>
          {manage && (
            <>
              <button type="button" className={button} disabled={syncableCount(active) === 0} onClick={() => run(accountsApi.syncAll)}>
                {t.syncAll}
              </button>
              <button type="button" className={primary} onClick={() => setEditing({ mode: "new" })}>
                {t.addAccount}
              </button>
            </>
          )}
        </PageHeader>
        {data && (
          <p className="text-sm text-ink-muted">
            {!manage ? t.manageOff : data.syncInterval ? t.schedule(formatEvery(data.syncInterval)) : t.scheduleOff}
            {manage && " " + (data.reconcileInterval ? t.reconcileSchedule(formatInterval(data.reconcileInterval)) : t.reconcileOff)}
          </p>
        )}
        {error && !data && <p className="text-sm text-danger">{t.loadAccountsFailed(error)}</p>}
        {actionError && (
          <p role="alert" className="text-sm text-danger">
            {actionError}
          </p>
        )}

        {editing?.mode === "new" && (
          <NewAccount
            admin={admin}
            smtpOn={data?.smtp != null}
            onCancel={() => setEditing(null)}
            onCreated={reload}
            onSaved={done}
            onFill={(name) => {
              setEditing({ mode: "fill", name });
              reload();
            }}
          />
        )}

        {data && active.length === 0 && editing === null && <p className="text-sm text-ink-muted">{t.noAccounts}</p>}

        {active.map((a) =>
          fillAccount?.name === a.name ? (
            <Card key={a.name}>
              <FillForm account={a} onCancel={() => setEditing(null)} onStarted={done} />
            </Card>
          ) : editAccount?.name === a.name && a.kind === "smtp" ? (
            <Card key={a.name}>
              <SMTPAccountForm
                account={a}
                admin={admin}
                smtpOn={data?.smtp != null}
                onCancel={() => setEditing(null)}
                onSaved={(name) => {
                  setEditing(null);
                  if (name !== a.name) renamed(a.name, name);
                  reload();
                }}
              />
            </Card>
          ) : editAccount?.name === a.name ? (
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
              {data && <SchedulePicker account={a} data={data} onSaved={reload} />}
              <FolderPicker account={a} onSaved={reload} />
            </Card>
          ) : (
            <AccountCard
              key={a.name}
              account={a}
              data={data!}
              manage={manage}
              onSync={() => run(() => accountsApi.sync(a.name))}
              onReconcile={() => run(() => accountsApi.reconcile(a.name))}
              onShowGone={() => showGone(a.name)}
              onEdit={() => setEditing({ mode: "edit", name: a.name })}
              onFill={() => setEditing({ mode: "fill", name: a.name })}
              onChanged={reload}
              onToggle={() => run(() => accountsApi.update(a.name, { enabled: !a.enabled }))}
              onRemove={() => {
                if (window.confirm(t.confirmRemove(a.name))) void run(() => accountsApi.remove(a.name));
              }}
            />
          ),
        )}

        {removed.length > 0 && (
          <section className="flex flex-col gap-2">
            <h2 className="mt-2 text-sm font-semibold text-ink-muted">{t.removedAccounts}</h2>
            {removed.map((a) => (
              <Card key={a.name}>
                <div className="flex flex-wrap items-baseline gap-x-3">
                  <span className="font-semibold">{a.name}</span>
                  <span className="text-sm text-ink-muted">{t.messageCount(messageCount(a), formatCount(messageCount(a)))}</span>
                </div>
                <p className="text-sm text-ink-muted">{t.removedNote}</p>
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
    <div className="flex flex-col gap-2 rounded-lg border border-line p-4">{children}</div>
  );
}

/** Accounts "Sync all" can sync: import and smtp accounts never are. */
export function syncableCount(accounts: Account[]): number {
  return accounts.filter((a) => !a.removed && a.kind === "imap").length;
}

/** When an smtp account last received a message, for its card. */
export function receiveStatus(a: Account): string {
  return a.lastReceivedAt ? t.lastReceived(relativeTime(a.lastReceivedAt)) : t.neverReceived;
}

function messageCount(a: Account): number {
  return a.folders.reduce((n, f) => n + f.messages, 0);
}

/** The failure streak and staleness of an IMAP account, for its card. */
export function syncHealthNote(a: Account): { text: string; failing: boolean } | null {
  if (a.kind !== "imap" || a.removed) return null;
  const s = a.sync;
  if (s.failureStreak >= 2 && s.failingSince) {
    return { text: t.failureStreak(s.failureStreak, shortDate(s.failingSince)), failing: s.health === "failing" };
  }
  if (s.health === "stale") return { text: t.staleNote, failing: false };
  return null;
}

function capitalize(s: string): string {
  return s.charAt(0).toLocaleUpperCase() + s.slice(1);
}

/**
 * How often the schedule syncs an IMAP account and when next, for its card:
 * "Every 30 minutes · next in 12 minutes". Null when it is disabled or the
 * schedule is off (the page header says so).
 */
export function scheduleNote(a: Account, data: Pick<AccountsResponse, "syncInterval">, now = new Date()): string | null {
  if (a.kind !== "imap" || a.removed || !a.enabled || !data.syncInterval) return null;
  if (a.syncInterval === "manual") return t.intervalManual;
  const every = a.sync.syncEvery;
  if (!every) return null;
  let text = capitalize(formatEvery(every));
  const own = durationMinutes(a.syncInterval);
  const effective = durationMinutes(every);
  if (a.syncInterval === "default") text += " " + t.defaultMark;
  else if (own !== null && effective !== null && own < effective) text += " " + t.raisedTo(formatInterval(a.syncInterval));
  if (a.sync.nextAt && a.sync.state === "idle") {
    text += " · " + (Date.parse(a.sync.nextAt) <= now.getTime() ? t.dueNow : t.nextSync(relativeTime(a.sync.nextAt, now)));
  }
  return text;
}

/** Presets of the schedule picker, as Go durations. */
const intervalPresets = ["15m0s", "30m0s", "1h0m0s", "6h0m0s", "12h0m0s", "24h0m0s"];

/**
 * The choices of the schedule picker: the default, the presets at or above
 * the server's floor, a stored value that is no preset, and manual.
 */
export function scheduleOptions(a: Account, data: Pick<AccountsResponse, "syncInterval" | "syncIntervalMin">): { value: string; label: string }[] {
  const floor = durationMinutes(data.syncIntervalMin) ?? 0;
  const options = [{ value: "default", label: t.intervalDefault(data.syncInterval ? formatEvery(data.syncInterval) : t.intervalManual) }];
  for (const p of intervalPresets) {
    if ((durationMinutes(p) ?? 0) >= floor) options.push({ value: p, label: capitalize(formatEvery(p)) });
  }
  if (a.syncInterval !== "default" && a.syncInterval !== "manual" && !options.some((o) => o.value === a.syncInterval)) {
    options.push({ value: a.syncInterval, label: t.intervalOther(formatEvery(a.syncInterval)) });
  }
  options.push({ value: "manual", label: t.intervalManual });
  return options;
}

/** What comparing the account with its server found, for its card. */
export function reconcileNote(a: Account): { gone: string | null; check: string } | null {
  if (a.kind !== "imap" || a.removed) return null;
  const gone = a.goneMessages > 0 ? t.goneMessages(a.goneMessages, formatCount(a.goneMessages)) : null;
  if (!a.lastReconciledAt) return { gone, check: t.neverChecked };
  let check = t.lastCheck(relativeTime(a.lastReconciledAt));
  const run = a.sync.lastRun;
  if (run && run.reconciledFolders > 0) check += ` · ${t.lastCheckCounts(run.gone, run.back, run.flagsChanged)}`;
  return { gone, check };
}

export function syncStatus(a: Account): { text: string; error?: string } {
  const run = a.sync.lastRun;
  if (a.kind === "import") {
    if (a.sync.state === "running") return { text: t.importRunning(run?.fetched ?? 0, run?.new ?? 0) };
    if (!run) return { text: t.importedNote };
    const when = relativeTime(run.finishedAt ?? run.startedAt);
    if (run.status === "ok") return { text: t.lastImportOk(when, run.new) };
    if (run.status === "partial") return { text: t.lastImportPartial(when), error: run.error };
    return { text: t.lastImportFailed(when), error: run.error };
  }
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
  data: AccountsResponse;
  manage: boolean;
  onSync: () => void;
  onReconcile: () => void;
  onShowGone: () => void;
  onEdit: () => void;
  /** smtp accounts: open the form that imports existing mail from IMAP. */
  onFill: () => void;
  /** After a fill was cancelled or resumed. */
  onChanged: () => void;
  onToggle: () => void;
  onRemove: () => void;
}

function AccountCard({ account: a, data, manage, onSync, onReconcile, onShowGone, onEdit, onFill, onChanged, onToggle, onRemove }: CardProps) {
  const status = syncStatus(a);
  const schedule = scheduleNote(a, data);
  const reconcile = reconcileNote(a);
  const health = syncHealthNote(a);
  const busy = a.sync.state !== "idle";
  const [showSetup, setShowSetup] = useState(false);
  if (a.kind === "smtp") {
    return (
      <Card>
        <div className="flex flex-wrap items-baseline gap-x-3">
          <span className="font-semibold">{a.name}</span>
          <span className="text-sm text-ink-muted" title={t.sourceCompleteHint}>
            {t.sourceComplete}
          </span>
          <span className="ml-auto text-sm text-ink-muted tabular-nums">{t.messageCount(messageCount(a), formatCount(messageCount(a)))}</span>
        </div>
        <p className="text-sm break-words text-ink-muted">{t.receivesFor(a.addresses.join(", "))}</p>
        <p className="text-sm text-ink-muted">{receiveStatus(a)}</p>
        {!a.enabled && <p className="text-sm text-warning">{t.smtpDisabledNote}</p>}
        {!data.smtp && <p className="text-sm text-warning">{t.smtpServerOffCard}</p>}
        <div className="flex flex-wrap gap-2">
          <button type="button" className={button} aria-expanded={showSetup} onClick={() => setShowSetup(!showSetup)}>
            {showSetup ? t.hideMailServerSetup : t.mailServerSetup}
          </button>
          {manage && (
            <>
              <button type="button" className={button} disabled={busy || a.fill?.status === "running"} onClick={onFill}>
                {t.importFromIMAP}
              </button>
              <button type="button" className={button} onClick={onEdit}>
                {t.edit}
              </button>
              <button type="button" className={button} onClick={onToggle}>
                {a.enabled ? t.disable : t.enable}
              </button>
              <button type="button" className={`${button} text-danger`} disabled={busy} onClick={onRemove}>
                {t.remove}
              </button>
            </>
          )}
        </div>
        {showSetup && <PostfixPanel addresses={a.addresses} />}
        <FillSection account={a} manage={manage} onChanged={onChanged} />
      </Card>
    );
  }
  if (a.kind === "import") {
    return (
      <Card>
        <div className="flex flex-wrap items-baseline gap-x-3">
          <span className="font-semibold">{a.name}</span>
          <span className="text-sm text-ink-muted">{t.imported}</span>
          <span className="ml-auto text-sm text-ink-muted tabular-nums">{t.messageCount(messageCount(a), formatCount(messageCount(a)))}</span>
        </div>
        <p className={`text-sm ${status.error ? "text-danger" : "text-ink-muted"}`} aria-live="polite">
          {busy && <span aria-hidden className="mr-1 inline-block animate-spin">↻</span>}
          {status.text}
        </p>
        {status.error && <p className="text-xs break-words text-danger">{status.error}</p>}
        {a.folders.length > 0 && (
          <ul className="flex flex-wrap gap-x-3 text-xs text-ink-muted">
            {a.folders.map((f) => (
              <li key={f.name}>
                {f.name} <span className="tabular-nums">({formatCount(f.messages)})</span>
              </li>
            ))}
          </ul>
        )}
        {manage && (
          <div className="flex flex-wrap gap-2">
            <button type="button" className={`${button} text-danger`} disabled={busy} onClick={onRemove}>
              {t.remove}
            </button>
          </div>
        )}
      </Card>
    );
  }
  return (
    <Card>
      <div className="flex flex-wrap items-baseline gap-x-3">
        <span className="font-semibold">{a.name}</span>
        <span className="min-w-0 truncate text-sm text-ink-muted">
          {a.username} · {a.host} ·{" "}
          <span title={t.sourceSnapshotHint}>{t.sourceSnapshot}</span>
        </span>
        <span className="ml-auto text-sm text-ink-muted tabular-nums">{t.messageCount(messageCount(a), formatCount(messageCount(a)))}</span>
      </div>
      <p className={`text-sm ${status.error ? "text-danger" : "text-ink-muted"}`} aria-live="polite">
        {busy && <span aria-hidden className="mr-1 inline-block animate-spin">↻</span>}
        {status.text}
      </p>
      {status.error && <p className="text-xs break-words text-danger">{status.error}</p>}
      {health && (
        <p className={`text-sm ${health.failing ? "font-medium text-danger" : "text-warning"}`}>{health.text}</p>
      )}
      {!a.enabled && <p className="text-sm text-warning">{t.disabledNote}</p>}
      {schedule && <p className="text-sm text-ink-muted">{schedule}</p>}
      {reconcile && (
        <p className="text-sm text-ink-muted">
          {reconcile.gone && (
            <>
              <button type="button" className="font-medium text-warning hover:underline" onClick={onShowGone}>
                {reconcile.gone}
              </button>
              {" · "}
            </>
          )}
          {reconcile.check}
        </p>
      )}
      {manage && (
        <div className="flex flex-wrap gap-2">
          <button type="button" className={button} disabled={busy} onClick={onSync}>
            {t.syncNow}
          </button>
          <button type="button" className={button} disabled={busy} title={t.checkServerHint} onClick={onReconcile}>
            {t.checkServer}
          </button>
          <button type="button" className={button} onClick={onEdit}>
            {t.edit}
          </button>
          <button type="button" className={button} onClick={onToggle}>
            {a.enabled ? t.disable : t.enable}
          </button>
          <button type="button" className={`${button} text-danger`} disabled={busy} onClick={onRemove}>
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
  /** Inside NewAccount, which draws the frame and the heading. */
  bare?: boolean;
  onCancel: () => void;
  /** Called with the saved account's name. */
  onSaved: (name: string) => void;
}

function AccountForm({ account, bare, onCancel, onSaved }: FormProps) {
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
    <form onSubmit={submit} className={account || bare ? "flex flex-col gap-3" : "flex flex-col gap-3 rounded-lg border border-line p-4"}>
      {!bare && <h2 className="font-semibold">{account ? account.name : t.addAccount}</h2>}
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
        <span id={`${hintId}-name`} className="text-xs text-ink-muted">
          {t.nameHint}
        </span>
      </div>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-[1fr_11rem_7rem]">
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
          <span id={`${hintId}-password`} className="text-xs text-ink-muted">
            {t.passwordHint}
          </span>
        </div>
      </div>
      {error && (
        <p role="alert" className="text-sm break-words text-danger">
          {error}
        </p>
      )}
      {question && (
        <div role="alert" className="flex flex-col gap-2 rounded-md border border-warning-line bg-warning-soft p-3 text-sm">
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
        {busy && <span className="text-sm text-ink-muted">{t.checkingLogin}</span>}
      </div>
    </form>
  );
}

const guideURL = "https://pklnx.github.io/mail-archive/guide/journaling";

/**
 * The "Add account" form: admins first choose where the mail comes from,
 * an IMAP mailbox or SMTP journaling; everyone else adds an IMAP account.
 */
function NewAccount({ admin, smtpOn, onCancel, onCreated, onSaved, onFill }: {
  admin: boolean;
  smtpOn: boolean;
  onCancel: () => void;
  /** An smtp account exists; its form still shows the mail server setup. */
  onCreated: () => void;
  onSaved: () => void;
  /** Opens the import from IMAP for the new smtp account. */
  onFill: (name: string) => void;
}) {
  const [source, setSource] = useState<"imap" | "smtp">("imap");
  const [created, setCreated] = useState(false);
  const name = useId();
  return (
    <section className="flex flex-col gap-3 rounded-lg border border-line p-4">
      <h2 className="font-semibold">{t.addAccount}</h2>
      {admin && !created && (
        <fieldset className="flex flex-col gap-1 text-sm">
          <legend className="mb-1">{t.sourceChoice}</legend>
          {(["imap", "smtp"] as const).map((k) => (
            <label key={k} className="flex items-start gap-2">
              <input type="radio" name={name} className="mt-1" checked={source === k} onChange={() => setSource(k)} />
              <span>
                {k === "imap" ? t.sourceIMAP : t.sourceSMTP}
                <span className="block text-xs text-ink-muted">{k === "imap" ? t.sourceSnapshotHint : t.sourceCompleteHint}</span>
              </span>
            </label>
          ))}
        </fieldset>
      )}
      {source === "imap" ? (
        <AccountForm bare onCancel={onCancel} onSaved={onSaved} />
      ) : (
        <SMTPAccountForm
          admin={admin}
          smtpOn={smtpOn}
          onCancel={onCancel}
          onCreated={() => {
            setCreated(true);
            onCreated();
          }}
          onSaved={onSaved}
          onFill={onFill}
        />
      )}
    </section>
  );
}

/** Addresses without empty entries, as typed. */
export function addressList(values: string[]): string[] {
  return values.map((v) => v.trim()).filter((v) => v !== "");
}

/**
 * Creates an smtp account (admins), or renames one and changes its
 * addresses (addresses: admins only). A new account shows the mail server
 * setup before the form closes.
 */
export function SMTPAccountForm({ account, admin, smtpOn, onCancel, onCreated, onSaved, onFill }: {
  account?: Account;
  admin: boolean;
  smtpOn: boolean;
  onCancel: () => void;
  onCreated?: () => void;
  /** Called with the saved account's name. */
  onSaved: (name: string) => void;
  /** After creating: offers the import of existing mail from IMAP. */
  onFill?: (name: string) => void;
}) {
  const [name, setName] = useState(account?.name ?? "");
  const [addresses, setAddresses] = useState<string[]>(account ? account.addresses : [""]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [created, setCreated] = useState<string[] | null>(null);
  const hintId = useId();
  const canAddresses = admin;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    const list = addressList(addresses);
    const newName = name.trim();
    try {
      if (account) {
        const body: AccountInput = {};
        if (newName !== account.name) body.name = newName;
        if (canAddresses && list.join("\n") !== account.addresses.join("\n")) body.addresses = list;
        if (Object.keys(body).length > 0) await accountsApi.update(account.name, body);
        onSaved(newName);
      } else {
        await accountsApi.create({ kind: "smtp", name: newName, addresses: list });
        setCreated(list.map((a) => a.toLowerCase()));
        onCreated?.();
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
    setBusy(false);
  };

  if (created) {
    return (
      <div className="flex flex-col gap-3">
        <p role="status" className="text-sm">
          {t.smtpAccountAdded(name.trim())}
        </p>
        <PostfixPanel addresses={created} />
        <div className="flex flex-wrap gap-2">
          <button type="button" className={primary} onClick={() => onSaved(name.trim())}>
            {t.done}
          </button>
          {onFill && (
            <button type="button" className={button} onClick={() => onFill(name.trim())}>
              {t.importFromIMAP}
            </button>
          )}
        </div>
      </div>
    );
  }

  const field = "flex flex-col gap-1 text-sm";
  return (
    <form onSubmit={submit} className="flex flex-col gap-3">
      {account && <h2 className="font-semibold">{account.name}</h2>}
      {!smtpOn && (
        <p className="text-sm text-warning">
          {t.smtpServerOff}{" "}
          <a className="text-accent-ink hover:underline" href={guideURL} target="_blank" rel="noreferrer">
            {t.journalingGuide}
          </a>
        </p>
      )}
      <label className={field}>
        {t.fieldName}
        <input className={input} required maxLength={64} value={name} onChange={(e) => setName(e.target.value)} placeholder="journal" />
      </label>
      <fieldset className={field} aria-describedby={`${hintId}-addresses`}>
        <legend className="mb-1">{t.fieldAddresses}</legend>
        {addresses.map((addr, i) => (
          <div key={i} className="flex gap-2">
            <input
              className={input}
              type="email"
              required={i === 0}
              disabled={!canAddresses}
              value={addr}
              onChange={(e) => setAddresses(addresses.map((a, j) => (i === j ? e.target.value : a)))}
              placeholder="archive@archive.local"
              aria-label={`${t.fieldAddresses} ${i + 1}`}
              autoComplete="off"
            />
            {canAddresses && addresses.length > 1 && (
              <button type="button" className={button} aria-label={t.removeAddress(addr)} onClick={() => setAddresses(addresses.filter((_, j) => j !== i))}>
                ×
              </button>
            )}
          </div>
        ))}
        {canAddresses && (
          <button type="button" className={`${button} self-start`} onClick={() => setAddresses([...addresses, ""])}>
            {t.addAddress}
          </button>
        )}
        <span id={`${hintId}-addresses`} className="text-xs text-ink-muted">
          {canAddresses ? t.addressHint : t.addressesAdminOnly}
        </span>
      </fieldset>
      {error && (
        <p role="alert" className="text-sm break-words text-danger">
          {error}
        </p>
      )}
      <div className="flex items-center gap-2">
        <button type="submit" className={primary} disabled={busy}>
          {t.save}
        </button>
        <button type="button" className={button} onClick={onCancel} disabled={busy}>
          {t.cancel}
        </button>
      </div>
    </form>
  );
}

/** What Postfix needs to send an smtp account its copies, with a copy button. */
export function PostfixPanel({ addresses }: { addresses: string[] }) {
  const setup = postfixSetup(addresses);
  const [copied, setCopied] = useState(false);
  return (
    <section className="flex flex-col gap-2 rounded-md border border-line p-3 text-sm">
      {!setup.dedicated && <p className="text-warning">{t.setupNotDedicated(setup.address)}</p>}
      <p>{t.setupIntro}</p>
      <pre className="overflow-x-auto rounded bg-surface-hover p-2 text-xs">{setup.text}</pre>
      <p className="text-xs text-ink-muted">
        {t.setupTarget}{" "}
        <a className="text-accent-ink hover:underline" href={guideURL} target="_blank" rel="noreferrer">
          {t.journalingGuide}
        </a>
      </p>
      <button
        type="button"
        className={`${button} self-start`}
        onClick={() => navigator.clipboard?.writeText(setup.text).then(() => setCopied(true), () => setCopied(false))}
      >
        {copied ? t.copied : t.copy}
      </button>
    </section>
  );
}

/**
 * Sets how often the schedule syncs the account. It sends only a changed
 * choice, so a value stored before the server's floor was raised is kept.
 */
export function SchedulePicker({ account, data, onSaved }: { account: Account; data: AccountsResponse; onSaved: () => void }) {
  const [choice, setChoice] = useState(account.syncInterval);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const id = useId();
  const off = !data.syncInterval;

  const save = async () => {
    if (choice === account.syncInterval) return;
    setBusy(true);
    setMessage("");
    try {
      await accountsApi.update(account.name, { syncInterval: choice });
      setMessage(t.saved);
      onSaved();
    } catch (err) {
      setMessage(t.failed(err instanceof Error ? err.message : String(err)));
    }
    setBusy(false);
  };

  return (
    <section className="flex flex-col gap-2 border-t border-line pt-3">
      <label htmlFor={id} className="text-sm font-semibold">
        {t.scheduleTitle}
      </label>
      <div className="flex flex-wrap items-center gap-2">
        <select id={id} className={`${input} w-auto`} value={choice} disabled={off || busy} onChange={(e) => setChoice(e.target.value)}>
          {scheduleOptions(account, data).map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
        <button type="button" className={button} disabled={off || busy || choice === account.syncInterval} onClick={save}>
          {t.saveSchedule}
        </button>
      </div>
      {off && <p className="text-xs text-ink-muted">{t.scheduleOffHint}</p>}
      {!off && choice === "manual" && <p className="text-xs text-ink-muted">{t.intervalManualHint}</p>}
      {message && <p className="text-sm">{message}</p>}
    </section>
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
    <section className="flex flex-col gap-2 border-t border-line pt-3">
      <h3 className="text-sm font-semibold">{t.folders}</h3>
      {!folders ? (
        <button type="button" className={`${button} self-start`} disabled={busy} onClick={load}>
          {busy ? t.loading : t.loadFolders}
        </button>
      ) : (
        <>
          <p className="text-xs text-ink-muted">{t.foldersHint}</p>
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
                  {f.specialUse && <span className="text-xs text-ink-muted">({t.role(f.specialUse)})</span>}
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
