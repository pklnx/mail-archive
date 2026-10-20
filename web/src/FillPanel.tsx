import { useId, useState, type FormEvent } from "react";
import { accountsApi, folderQuestion, type Account, type AccountsResponse, type Fill, type ServerFolder, type TLSMode } from "./api";
import { canKeepRunning, cutoffFromInput, fillIntervalOptions, fillStatus, resumable, stoppable } from "./fill";
import { button, input, primary } from "./formStyles";
import { formatCount, longDate } from "./format";
import { t } from "./i18n";

const errorText = (err: unknown) => (err instanceof Error ? err.message : String(err));

/**
 * The fill part of an smtp account's card: its recurring fills and its
 * latest fill, each with its progress or result, its folders, and stop,
 * resume, keep running or the interval.
 */
export function FillSection({ account: a, data, manage, onChanged }: { account: Account; data: AccountsResponse; manage: boolean; onChanged: () => void }) {
  const fills = a.fills;
  if (fills.length === 0) return null;
  return (
    <>
      {fills.map((f) => (
        <FillBlock key={f.id} account={a} fill={f} data={data} manage={manage} onChanged={onChanged} />
      ))}
    </>
  );
}

function FillBlock({ account: a, fill: f, data, manage, onChanged }: { account: Account; fill: Fill; data: AccountsResponse; manage: boolean; onChanged: () => void }) {
  const status = fillStatus(a, f);
  const [password, setPassword] = useState("");
  const [interval, setIntervalChoice] = useState(f.interval ?? "default");
  // Trash and spam folders that switching on would copy, once asked.
  const [confirm, setConfirm] = useState<string[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const id = useId();
  if (!status) return null;

  const act = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setError("");
    try {
      await action();
      setPassword("");
      setConfirm(null);
    } catch (err) {
      const question = folderQuestion(err);
      if (question) setConfirm(question);
      else setError(t.failed(errorText(err)));
    }
    setBusy(false);
    onChanged();
  };

  const resume = (e: FormEvent) => {
    e.preventDefault();
    void act(() => accountsApi.resumeFill(a.name, f.id, password));
  };
  const keepRunning = (e: FormEvent) => {
    e.preventDefault();
    void act(() =>
      accountsApi.changeFill(a.name, f.id, { keepRunning: true, password, interval, ...(confirm ? { confirmFolders: true } : {}) }),
    );
  };
  const health = f.health && f.health.state !== "ok" ? f.health : null;
  // Older fills write into a folder per IMAP folder until they keep running; newer ones all into JOURNAL.
  const prefixed = f.folderPrefix && !f.recurringSince;

  return (
    <section className="flex flex-col gap-1 border-t border-line pt-2" aria-label={t.importFromIMAP}>
      <p className={`text-sm ${status.error || health ? "text-danger" : "text-ink-muted"}`} aria-live="polite">
        {f.status === "running" && <span aria-hidden className="mr-1 inline-block animate-spin">↻</span>}
        {status.text}
      </p>
      <p className="truncate text-xs text-ink-muted">{t.fillSource(f.username, f.host, prefixed ? `${f.folderPrefix}/…` : "JOURNAL")}</p>
      {health && (
        <p className={`text-xs ${health.state === "failing" ? "text-danger" : "text-warning"}`}>
          {health.state === "failing" ? t.fillFailing(health.failureStreak) : t.fillStale}
        </p>
      )}
      {status.error && <p className="text-xs break-words text-danger">{status.error}</p>}
      {f.folders.length > 0 && (
        <ul className="flex flex-wrap gap-x-3 text-xs text-ink-muted" title={f.recurringSince ? t.recurringCountsHint : undefined}>
          {f.folders.map((ff) => (
            <li key={ff.source} title={ff.error || undefined} className={ff.error ? "text-danger" : undefined}>
              {prefixed ? ff.folder : ff.source} <span className="tabular-nums">({formatCount(ff.imported)})</span>
              {!ff.done && !ff.error && f.status === "running" && " …"}
            </li>
          ))}
        </ul>
      )}
      {f.unconfirmedFolders.length > 0 && <p className="text-xs text-warning">{t.fillUnconfirmed(f.unconfirmedFolders.join(", "))}</p>}
      {manage && stoppable(f) && (
        <div className="mt-1 flex flex-wrap items-center gap-2">
          <button
            type="button"
            className={button}
            disabled={busy}
            onClick={() => {
              if (window.confirm(f.keepRunning ? t.confirmStopFill : t.confirmCancelFill)) void act(() => accountsApi.cancelFill(a.name, f.id));
            }}
          >
            {f.keepRunning ? t.stopFill : t.cancelFill}
          </button>
          {f.keepRunning && (
            <>
              <select
                className={`${input} w-auto`}
                value={interval}
                disabled={busy}
                onChange={(e) => setIntervalChoice(e.target.value)}
                aria-label={`${t.fillInterval} (${f.username})`}
              >
                {fillIntervalOptions(f.interval, data).map((o) => (
                  <option key={o.value} value={o.value}>
                    {o.label}
                  </option>
                ))}
              </select>
              <button
                type="button"
                className={button}
                disabled={busy || interval === (f.interval ?? "default")}
                onClick={() => void act(() => accountsApi.changeFill(a.name, f.id, { interval }))}
              >
                {t.saveFillInterval}
              </button>
            </>
          )}
        </div>
      )}
      {manage && (resumable(f) || canKeepRunning(f)) && (
        <form onSubmit={resumable(f) ? resume : keepRunning} className="mt-1 flex flex-col gap-1">
          <div className="flex flex-wrap items-center gap-2">
            <input
              className={`${input} w-56`}
              type="password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              aria-label={`${t.fieldPassword} (${f.username})`}
              placeholder={t.fieldPassword}
              autoComplete="new-password"
              aria-describedby={`${id}-password`}
            />
            {resumable(f) && (
              <button type="submit" className={button} disabled={busy || password === ""}>
                {t.resumeFill}
              </button>
            )}
            {canKeepRunning(f) && (
              <>
                <select
                  className={`${input} w-auto`}
                  value={interval}
                  disabled={busy}
                  onChange={(e) => setIntervalChoice(e.target.value)}
                  aria-label={`${t.fillInterval} (${f.username})`}
                >
                  {fillIntervalOptions(null, data).map((o) => (
                    <option key={o.value} value={o.value}>
                      {o.label}
                    </option>
                  ))}
                </select>
                <button type="button" className={button} disabled={busy || password === ""} onClick={keepRunning}>
                  {confirm ? t.keepRunningCopy : t.keepRunningFill}
                </button>
              </>
            )}
            {busy && <span className="text-sm text-ink-muted">{t.checkingLogin}</span>}
          </div>
          {confirm && <p className="text-sm text-warning">{t.keepRunningTrashSpam(confirm.join(", "))}</p>}
          <span id={`${id}-password`} className="text-xs text-ink-muted">
            {f.recurringSince ? t.resumeRecurringHint : resumable(f) ? `${t.resumeFillHint} ${t.keepRunningHint}` : t.keepRunningHint}
          </span>
        </form>
      )}
      {error && (
        <p role="alert" className="text-sm break-words text-danger">
          {error}
        </p>
      )}
    </section>
  );
}

const defaultPort: Record<TLSMode, number> = { tls: 993, starttls: 143, none: 143 };

/**
 * Starts a fill of an smtp account: the IMAP server, its folders after a
 * login check (trash and spam unticked), and the cut-off, or whether it
 * keeps importing new mail and how often. The mail goes into the account's
 * folder JOURNAL.
 */
export function FillForm({ account, data, onCancel, onStarted }: { account: Account; data: AccountsResponse; onCancel: () => void; onStarted: () => void }) {
  const [host, setHost] = useState("");
  const [tls, setTLS] = useState<TLSMode>("tls");
  const [port, setPort] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [cutoff, setCutoff] = useState("");
  const [keep, setKeep] = useState(false);
  const [interval, setIntervalChoice] = useState("default");
  // The server's folders after the login check; changing the server or the
  // login loads them again.
  const [folders, setFolders] = useState<ServerFolder[] | null>(null);
  const [suggested, setSuggested] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const id = useId();

  const server = () => ({ host: host.trim(), tls, port: port ? Number(port) : defaultPort[tls], username: username.trim(), password });
  const changed = <T,>(set: (v: T) => void) => (v: T) => {
    set(v);
    setFolders(null);
  };

  const loadFolders = async () => {
    setBusy(true);
    setError("");
    try {
      const list = await accountsApi.fillFolders(account.name, server());
      setSuggested(list.suggestedExclusions);
      setFolders(list.folders.map((f) => ({ ...f, selected: !list.suggestedExclusions.includes(f.name) })));
    } catch (err) {
      setError(errorText(err));
    }
    setBusy(false);
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!folders) {
      void loadFolders();
      return;
    }
    setBusy(true);
    setError("");
    try {
      await accountsApi.startFill(account.name, {
        ...server(),
        excludedFolders: folders.filter((f) => !f.selected).map((f) => f.name),
        ...(keep ? { keepRunning: true, ...(interval !== "default" ? { interval } : {}) } : { before: cutoffFromInput(cutoff) }),
        // The user saw the trash and spam folders unticked in the list.
        confirmFolders: true,
      });
      onStarted();
    } catch (err) {
      const question = folderQuestion(err);
      setError(question ? t.fillTrashSpam(question.join(", ")) : errorText(err));
      setBusy(false);
    }
  };

  const field = "flex flex-col gap-1 text-sm";
  return (
    <form onSubmit={submit} className="flex flex-col gap-3">
      <h2 className="font-semibold">{t.importFromIMAP}</h2>
      <p className="text-sm text-ink-muted">{t.fillIntro}</p>
      <p className="text-sm text-ink-muted">
        {account.firstReceived ? t.firstReceived(longDate(account.firstReceived.at), account.firstReceived.subject) : t.noneReceived}
      </p>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-[1fr_11rem_7rem]">
        <label className={field}>
          {t.fieldHost}
          <input className={input} required value={host} onChange={(e) => changed(setHost)(e.target.value)} placeholder="imap.example.com" autoComplete="off" />
        </label>
        <label className={field}>
          {t.fieldTLS}
          <select className={input} value={tls} onChange={(e) => changed(setTLS)(e.target.value as TLSMode)}>
            <option value="tls">{t.tlsTLS}</option>
            <option value="starttls">{t.tlsSTARTTLS}</option>
            <option value="none">{t.tlsNone}</option>
          </select>
        </label>
        <label className={field}>
          {t.fieldPort}
          <input
            className={input}
            inputMode="numeric"
            pattern="[0-9]*"
            value={port}
            onChange={(e) => changed(setPort)(e.target.value)}
            placeholder={String(defaultPort[tls])}
          />
        </label>
      </div>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <label className={field}>
          {t.fieldUsername}
          <input className={input} required value={username} onChange={(e) => changed(setUsername)(e.target.value)} autoComplete="off" />
        </label>
        <label className={field}>
          {t.fieldPassword}
          <input
            className={input}
            type="password"
            required
            value={password}
            onChange={(e) => changed(setPassword)(e.target.value)}
            autoComplete="new-password"
          />
        </label>
      </div>
      {folders && (
        <fieldset className="flex flex-col gap-1 text-sm" aria-describedby={`${id}-folders`}>
          <legend className="mb-1 font-medium">{t.folders}</legend>
          {suggested.length > 0 && <p className="text-warning">{t.fillTrashSpam(suggested.join(", "))}</p>}
          <ul className="flex flex-col gap-1">
            {folders.map((f, i) => (
              <li key={f.name}>
                <label className="flex items-center gap-2">
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
          <span id={`${id}-folders`} className="text-xs text-ink-muted">
            {t.fillFoldersHint}
          </span>
        </fieldset>
      )}
      <div className="flex flex-col gap-1 text-sm">
        <label className="flex items-center gap-2">
          <input type="checkbox" checked={keep} onChange={(e) => setKeep(e.target.checked)} aria-describedby={`${id}-keep`} />
          <span className="font-medium">{t.keepImporting}</span>
        </label>
        <span id={`${id}-keep`} className="text-xs text-ink-muted">
          {t.keepImportingHint}
        </span>
      </div>
      {keep ? (
        <label className={`${field} sm:w-64`}>
          {t.fillInterval}
          <select className={input} value={interval} onChange={(e) => setIntervalChoice(e.target.value)}>
            {fillIntervalOptions(null, data).map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </select>
        </label>
      ) : (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <div className={field}>
            <label className={field}>
              {t.fieldCutoff}
              <input
                className={input}
                type="datetime-local"
                value={cutoff}
                onChange={(e) => setCutoff(e.target.value)}
                aria-describedby={`${id}-cutoff`}
              />
            </label>
            <span id={`${id}-cutoff`} className="text-xs text-ink-muted">
              {t.cutoffHint}
              {cutoff && <span className="block text-warning">{t.cutoffGap}</span>}
            </span>
          </div>
        </div>
      )}
      {error && (
        <p role="alert" className="text-sm break-words text-danger">
          {error}
        </p>
      )}
      <div className="flex items-center gap-2">
        <button type="submit" className={primary} disabled={busy}>
          {folders ? t.startFill : t.loadFillFolders}
        </button>
        <button type="button" className={button} onClick={onCancel} disabled={busy}>
          {t.cancel}
        </button>
        {busy && <span className="text-sm text-ink-muted">{t.checkingLogin}</span>}
      </div>
    </form>
  );
}
