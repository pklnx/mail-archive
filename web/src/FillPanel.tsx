import { useId, useState, type FormEvent } from "react";
import { accountsApi, folderQuestion, type Account, type ServerFolder, type TLSMode } from "./api";
import { cutoffFromInput, defaultFillPrefix, fillStatus, resumable } from "./fill";
import { button, input, primary } from "./formStyles";
import { formatCount, longDate } from "./format";
import { t } from "./i18n";

const errorText = (err: unknown) => (err instanceof Error ? err.message : String(err));

/**
 * The fill part of an smtp account's card: the latest fill's progress or
 * result, its folders, and cancel or resume.
 */
export function FillSection({ account: a, manage, onChanged }: { account: Account; manage: boolean; onChanged: () => void }) {
  const f = a.fill;
  const status = fillStatus(a);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const id = useId();
  if (!f || !status) return null;

  const act = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setError("");
    try {
      await action();
      setPassword("");
    } catch (err) {
      setError(t.failed(errorText(err)));
    }
    setBusy(false);
    onChanged();
  };

  const resume = (e: FormEvent) => {
    e.preventDefault();
    void act(() => accountsApi.resumeFill(a.name, f.id, password));
  };

  return (
    <section className="flex flex-col gap-1 border-t border-line pt-2" aria-label={t.importFromIMAP}>
      <p className={`text-sm ${status.error ? "text-danger" : "text-ink-muted"}`} aria-live="polite">
        {f.status === "running" && <span aria-hidden className="mr-1 inline-block animate-spin">↻</span>}
        {status.text}
      </p>
      <p className="truncate text-xs text-ink-muted">{t.fillSource(f.username, f.host, f.folderPrefix)}</p>
      {status.error && <p className="text-xs break-words text-danger">{status.error}</p>}
      {f.folders.length > 0 && (
        <ul className="flex flex-wrap gap-x-3 text-xs text-ink-muted">
          {f.folders.map((ff) => (
            <li key={ff.source} title={ff.error || undefined} className={ff.error ? "text-danger" : undefined}>
              {ff.folder} <span className="tabular-nums">({formatCount(ff.imported)})</span>
              {!ff.done && !ff.error && f.status === "running" && " …"}
            </li>
          ))}
        </ul>
      )}
      {manage && f.status === "running" && (
        <button
          type="button"
          className={`${button} mt-1 self-start`}
          disabled={busy}
          onClick={() => {
            if (window.confirm(t.confirmCancelFill)) void act(() => accountsApi.cancelFill(a.name, f.id));
          }}
        >
          {t.cancelFill}
        </button>
      )}
      {manage && resumable(f) && (
        <form onSubmit={resume} className="mt-1 flex flex-col gap-1">
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
              aria-describedby={`${id}-resume`}
            />
            <button type="submit" className={button} disabled={busy || password === ""}>
              {t.resumeFill}
            </button>
            {busy && <span className="text-sm text-ink-muted">{t.checkingLogin}</span>}
          </div>
          <span id={`${id}-resume`} className="text-xs text-ink-muted">
            {t.resumeFillHint}
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
 * login check (trash and spam unticked), the prefix and the cut-off.
 */
export function FillForm({ account, onCancel, onStarted }: { account: Account; onCancel: () => void; onStarted: () => void }) {
  const [host, setHost] = useState("");
  const [tls, setTLS] = useState<TLSMode>("tls");
  const [port, setPort] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [prefix, setPrefix] = useState("");
  const [cutoff, setCutoff] = useState("");
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
        folderPrefix: prefix.trim() || undefined,
        before: cutoffFromInput(cutoff),
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

  const shownPrefix = prefix.trim() || defaultFillPrefix(username) || "alice@example.com";
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
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <div className={field}>
          <label className={field}>
            {t.fieldFillPrefix}
            <input
              className={input}
              value={prefix}
              maxLength={200}
              onChange={(e) => setPrefix(e.target.value)}
              placeholder={defaultFillPrefix(username)}
              autoComplete="off"
              aria-describedby={`${id}-prefix`}
            />
          </label>
          <span id={`${id}-prefix`} className="text-xs text-ink-muted">
            {t.fillPrefixHint(shownPrefix)}
          </span>
        </div>
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
