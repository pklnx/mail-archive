import { useId, useState, type FormEvent } from "react";
import { Button, Checkbox, Form, FormGroup, InlineLoading, InlineNotification, PasswordInput, ProgressBar, Select, SelectItem, TextInput } from "@carbon/react";
import { accountsApi, folderQuestion, type Account, type AccountsResponse, type Fill, type ServerFolder, type TLSMode } from "./api";
import { DangerModal } from "./DangerModal";
import { canKeepRunning, cutoffFromInput, fillIntervalOptions, fillStatus, resumable, stoppable } from "./fill";
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
  // Cancel or stop asks first, in a danger Modal.
  const [asking, setAsking] = useState(false);
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
  const running = f.status === "running";

  return (
    <section className="fill" aria-label={t.importFromIMAP}>
      <p className={status.error || health ? "text-error" : "text-secondary"} aria-live="polite">
        {status.text}
      </p>
      {running && <ProgressBar size="small" hideLabel label={t.fillProgress} />}
      <p className="text-secondary text-truncate">{t.fillSource(f.username, f.host, prefixed ? `${f.folderPrefix}/…` : "JOURNAL")}</p>
      {health && (
        <p className={health.state === "failing" ? "text-error" : "text-warning"}>
          {health.state === "failing" ? t.fillFailing(health.failureStreak) : t.fillStale}
        </p>
      )}
      {status.error && <p className="text-error text-small break-words">{status.error}</p>}
      {f.folders.length > 0 && (
        <ul className="inline-list text-small text-secondary" title={f.recurringSince ? t.recurringCountsHint : undefined}>
          {f.folders.map((ff) => (
            <li key={ff.source} title={ff.error || undefined} className={ff.error ? "text-error" : undefined}>
              {prefixed ? ff.folder : ff.source} <span className="tabular">({formatCount(ff.imported)})</span>
              {!ff.done && !ff.error && running && " …"}
            </li>
          ))}
        </ul>
      )}
      {f.unconfirmedFolders.length > 0 && <p className="text-warning text-small">{t.fillUnconfirmed(f.unconfirmedFolders.join(", "))}</p>}
      {manage && stoppable(f) && (
        <div className="fill__row">
          <Button kind="danger--tertiary" size="sm" disabled={busy} onClick={() => setAsking(true)}>
            {f.keepRunning ? t.stopFill : t.cancelFill}
          </Button>
          {f.keepRunning && (
            <>
              <Select
                id={`${id}-interval`}
                className="fill__select"
                size="sm"
                labelText={`${t.fillInterval} (${f.username})`}
                hideLabel
                value={interval}
                disabled={busy}
                onChange={(e) => setIntervalChoice(e.target.value)}
              >
                {fillIntervalOptions(f.interval, data).map((o) => (
                  <SelectItem key={o.value} value={o.value} text={o.label} />
                ))}
              </Select>
              <Button
                kind="tertiary"
                size="sm"
                disabled={busy || interval === (f.interval ?? "default")}
                onClick={() => void act(() => accountsApi.changeFill(a.name, f.id, { interval }))}
              >
                {t.saveFillInterval}
              </Button>
            </>
          )}
        </div>
      )}
      {asking && (
        <DangerModal
          size="xs"
          modalHeading={f.keepRunning ? t.stopFillTitle : t.cancelFillTitle}
          primaryButtonText={f.keepRunning ? t.stopFill : t.cancelFill}
          secondaryButtonText={t.fillBack}
          preventCloseOnClickOutside
          onRequestClose={() => setAsking(false)}
          onRequestSubmit={() => {
            setAsking(false);
            void act(() => accountsApi.cancelFill(a.name, f.id));
          }}
        >
          <p>{f.keepRunning ? t.stopFillText : t.cancelFillText}</p>
        </DangerModal>
      )}
      {manage && (resumable(f) || canKeepRunning(f)) && (
        <form onSubmit={resumable(f) ? resume : keepRunning} className="fill__form">
          <div className="fill__password">
            <PasswordInput
              id={`${id}-password`}
              size="sm"
              labelText={`${t.fieldPassword} (${f.username})`}
              showPasswordLabel={t.carbon.showPassword}
              hidePasswordLabel={t.carbon.hidePassword}
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="new-password"
              helperText={f.recurringSince ? t.resumeRecurringHint : resumable(f) ? `${t.resumeFillHint} ${t.keepRunningHint}` : t.keepRunningHint}
            />
          </div>
          <div className="fill__row">
            {resumable(f) && (
              <Button type="submit" kind="tertiary" size="sm" disabled={busy || password === ""}>
                {t.resumeFill}
              </Button>
            )}
            {canKeepRunning(f) && (
              <>
                <Select
                  id={`${id}-keep-interval`}
                  className="fill__select"
                  size="sm"
                  labelText={`${t.fillInterval} (${f.username})`}
                  hideLabel
                  value={interval}
                  disabled={busy}
                  onChange={(e) => setIntervalChoice(e.target.value)}
                >
                  {fillIntervalOptions(null, data).map((o) => (
                    <SelectItem key={o.value} value={o.value} text={o.label} />
                  ))}
                </Select>
                <Button kind="tertiary" size="sm" disabled={busy || password === ""} onClick={keepRunning}>
                  {confirm ? t.keepRunningCopy : t.keepRunningFill}
                </Button>
              </>
            )}
            {busy && <InlineLoading className="inline-loading--auto" description={t.checkingLogin} iconDescription={t.loading} />}
          </div>
          {confirm && <p className="text-warning">{t.keepRunningTrashSpam(confirm.join(", "))}</p>}
        </form>
      )}
      {error && (
        <p role="alert" className="text-error break-words">
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
 * folder JOURNAL. The page around it draws the heading.
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

  return (
    <Form onSubmit={submit} className="page-form">
      <p className="text-secondary">{t.fillIntro}</p>
      <p className="text-secondary">
        {account.firstReceived ? t.firstReceived(longDate(account.firstReceived.at), account.firstReceived.subject) : t.noneReceived}
      </p>
      <div className="form-row form-row--server">
        <TextInput
          id={`${id}-host`}
          labelText={t.fieldHost}
          required
          value={host}
          onChange={(e) => changed(setHost)(e.target.value)}
          placeholder="imap.example.com"
          autoComplete="off"
        />
        <Select id={`${id}-tls`} labelText={t.fieldTLS} value={tls} onChange={(e) => changed(setTLS)(e.target.value as TLSMode)}>
          <SelectItem value="tls" text={t.tlsTLS} />
          <SelectItem value="starttls" text={t.tlsSTARTTLS} />
          <SelectItem value="none" text={t.tlsNone} />
        </Select>
        <TextInput
          id={`${id}-port`}
          labelText={t.fieldPort}
          inputMode="numeric"
          pattern="[0-9]*"
          value={port}
          onChange={(e) => changed(setPort)(e.target.value)}
          placeholder={String(defaultPort[tls])}
        />
      </div>
      <div className="form-row form-row--two">
        <TextInput
          id={`${id}-username`}
          labelText={t.fieldUsername}
          required
          value={username}
          onChange={(e) => changed(setUsername)(e.target.value)}
          autoComplete="off"
        />
        <PasswordInput
          id={`${id}-password`}
          labelText={t.fieldPassword}
          showPasswordLabel={t.carbon.showPassword}
          hidePasswordLabel={t.carbon.hidePassword}
          required
          value={password}
          onChange={(e) => changed(setPassword)(e.target.value)}
          autoComplete="new-password"
        />
      </div>
      {folders && (
        <FormGroup legendText={t.folders} aria-describedby={`${id}-folders`}>
          {suggested.length > 0 && (
            <InlineNotification
              kind="warning"
              lowContrast
              hideCloseButton
              className="notification--full"
              statusIconDescription={t.carbon.status.warning}
              subtitle={t.fillTrashSpam(suggested.join(", "))}
            />
          )}
          {folders.map((f, i) => (
            <Checkbox
              key={f.name}
              id={`${id}-folder-${i}`}
              labelText={f.specialUse ? `${f.name} (${t.role(f.specialUse)})` : f.name}
              checked={f.selected}
              onChange={(_, { checked }) => setFolders(folders.map((g, j) => (i === j ? { ...g, selected: checked } : g)))}
            />
          ))}
          <p id={`${id}-folders`} className="cds--form__helper-text">
            {t.fillFoldersHint}
          </p>
        </FormGroup>
      )}
      <div>
        <Checkbox
          id={`${id}-keep`}
          labelText={t.keepImporting}
          checked={keep}
          onChange={(_, { checked }) => setKeep(checked)}
          aria-describedby={`${id}-keep-hint`}
        />
        <p id={`${id}-keep-hint`} className="cds--form__helper-text checkbox-hint">
          {t.keepImportingHint}
        </p>
      </div>
      {keep ? (
        <Select id={`${id}-interval`} className="field--narrow" labelText={t.fillInterval} value={interval} onChange={(e) => setIntervalChoice(e.target.value)}>
          {fillIntervalOptions(null, data).map((o) => (
            <SelectItem key={o.value} value={o.value} text={o.label} />
          ))}
        </Select>
      ) : (
        <div className="field--half">
          <TextInput
            id={`${id}-cutoff`}
            labelText={t.fieldCutoff}
            type="datetime-local"
            value={cutoff}
            onChange={(e) => setCutoff(e.target.value)}
            helperText={t.cutoffHint}
          />
          {cutoff && <p className="text-warning text-small field-note">{t.cutoffGap}</p>}
        </div>
      )}
      {error && (
        <p role="alert" className="text-error break-words">
          {error}
        </p>
      )}
      <div className="form-actions">
        <Button kind="secondary" onClick={onCancel} disabled={busy}>
          {t.cancel}
        </Button>
        <Button type="submit" kind="primary" disabled={busy}>
          {folders ? t.startFill : t.loadFillFolders}
        </Button>
        {busy && <InlineLoading className="inline-loading--auto" description={t.checkingLogin} iconDescription={t.loading} />}
      </div>
    </Form>
  );
}
