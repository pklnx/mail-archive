import { useEffect, useId, useRef, useState, type FormEvent, type ReactNode } from "react";
import {
  Button,
  Checkbox,
  CodeSnippet,
  Form,
  FormGroup,
  IconButton,
  InlineLoading,
  InlineNotification,
  Link,
  OperationalTag,
  PasswordInput,
  RadioButton,
  RadioButtonGroup,
  Select,
  SelectItem,
  Tag,
  TextInput,
  Tooltip,
} from "@carbon/react";
import { Add, Close } from "@carbon/icons-react";
import { accountsApi, folderQuestion, type Account, type AccountInput, type AccountsResponse, type ServerFolder, type TLSMode } from "./api";
import { suggestName } from "./accountName";
import { durationMinutes, formatCount, formatEvery, formatInterval, relativeTime, shortDate } from "./format";
import { t } from "./i18n";
import { postfixSetup } from "./journalSetup";

// The parts of the Accounts pages (#285 part 2) that the list and the
// account's detail page share: the status texts and tags, and the forms.
// Only AccountsPage imports them, so they are in its lazy chunk.

/** A short error: the plain text, so screen readers read it as it is. */
export function ErrorText({ text, alert }: { text: string; alert?: boolean }) {
  return (
    <p role={alert ? "alert" : undefined} className="app-text-error app-break-words">
      {text}
    </p>
  );
}

/** A notification inside a page or form: no close button, takes no focus. */
export function Notice({ kind, text, alert }: { kind: "error" | "warning" | "info" | "success"; text: string; alert?: boolean }) {
  return (
    <InlineNotification
      kind={kind}
      lowContrast
      hideCloseButton
      role={alert ? "alert" : "status"}
      className="notification--full"
      statusIconDescription={t.carbon.status[kind]}
      subtitle={text}
    />
  );
}

/** A source tag with its explanation in a tooltip; a button, so the keyboard reaches the tooltip too. */
function SourceTag({ text, hint }: { text: string; hint: string }) {
  return (
    <Tooltip description={hint} align="bottom">
      <OperationalTag size="sm" type="cool-gray" text={text} />
    </Tooltip>
  );
}

/** Where an account's mail comes from: an IMAP snapshot, SMTP journaling or files. */
export function TypeTag({ account: a }: { account: Account }) {
  if (a.kind === "imap") return <SourceTag text={t.sourceSnapshot} hint={t.sourceSnapshotHint} />;
  if (a.kind === "smtp") return <SourceTag text={t.sourceComplete} hint={t.sourceCompleteHint} />;
  return (
    <Tag size="sm" type="cool-gray">
      {t.imported}
    </Tag>
  );
}

/** The state tags of an account: removed, failing, receiving, disabled. Nothing for an account that is just fine. */
export function StateTags({ account: a, smtpOn }: { account: Account; smtpOn: boolean }) {
  if (a.removed) {
    return (
      <Tag size="sm" type="gray">
        {t.removed}
      </Tag>
    );
  }
  return (
    <>
      {a.kind === "imap" && a.sync.health === "failing" && (
        <Tag size="sm" type="red">
          {t.tagFailing}
        </Tag>
      )}
      {a.kind === "smtp" && a.enabled && smtpOn && (
        <Tag size="sm" type="green">
          {t.receiving}
        </Tag>
      )}
      {!a.enabled && a.kind !== "import" && (
        <Tag size="sm" type="gray">
          {t.disabled}
        </Tag>
      )}
    </>
  );
}

/** Accounts "Sync all" can sync: import and smtp accounts never are. */
export function syncableCount(accounts: Account[]): number {
  return accounts.filter((a) => !a.removed && a.kind === "imap").length;
}

/** When an smtp account last received a message. */
export function receiveStatus(a: Account): string {
  return a.lastReceivedAt ? t.lastReceived(relativeTime(a.lastReceivedAt)) : t.neverReceived;
}

export function messageCount(a: Account): number {
  return a.folders.reduce((n, f) => n + f.messages, 0);
}

/** A sync, an import or a fill is queued or running: some actions wait. */
export const isBusy = (a: Account) => a.sync.state !== "idle";

/** What the account did last: its last sync or import, or for smtp its last message received. */
export function lastActivity(a: Account): { text: string; error?: string } {
  return a.kind === "smtp" ? { text: receiveStatus(a) } : syncStatus(a);
}

/** The failure streak and staleness of an IMAP account. */
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
 * How often the schedule syncs an IMAP account and when next:
 * "Every 30 minutes · next in 12 minutes". Null when it is disabled or the
 * schedule is off (the list's helper line says so).
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

/** What comparing the account with its server found. */
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

/**
 * Whether the complete deletion of a removed account was cancelled: it was
 * being deleted, and the account is still listed after it ended. A new
 * purge clears it; so does the setter, when a new attempt starts.
 */
export function usePurgeCancelled(a: Account) {
  const [cancelled, setCancelled] = useState(false);
  const wasPurging = useRef(a.purging);
  useEffect(() => {
    if (wasPurging.current && !a.purging) setCancelled(true);
    if (a.purging) setCancelled(false);
    wasPurging.current = a.purging;
  }, [a.purging]);
  return [cancelled, setCancelled] as const;
}

/**
 * The buttons of a form. On the Add account page Save is the page's
 * primary; in a section of an account's page, where Sync is the one
 * primary, Save is tertiary and Cancel ghost (#285).
 */
function FormButtons({ section, busy, locked, question, onCancel, children }: {
  section: boolean;
  busy: boolean;
  /** Save waits (a rename is not in the list yet). */
  locked?: boolean;
  /** The trash/spam question is open: its answer is the primary action. */
  question?: boolean;
  onCancel: () => void;
  children?: ReactNode;
}) {
  return (
    <div className="form-actions">
      <Button kind={section ? "ghost" : "secondary"} onClick={onCancel} disabled={busy}>
        {t.cancel}
      </Button>
      <Button type="submit" kind={section || question ? "tertiary" : "primary"} disabled={busy || locked}>
        {t.save}
      </Button>
      {children}
    </div>
  );
}

const defaultPort: Record<TLSMode, number> = { tls: 993, starttls: 143, none: 143 };

/** The name of a connection security setting. */
export const tlsLabel = (tls: TLSMode) => ({ tls: t.tlsTLS, starttls: t.tlsSTARTTLS, none: t.tlsNone })[tls];

interface FormProps {
  /** Editing an existing account; otherwise a new one. */
  account?: Account;
  /** In a section of the account's page (#285): no primary button. */
  section?: boolean;
  /** Save waits: an earlier rename is not in the account list yet. */
  locked?: boolean;
  onCancel: () => void;
  /** Called with the saved account's name. */
  onSaved: (name: string) => void;
}

export function AccountForm({ account, section = false, locked, onCancel, onSaved }: FormProps) {
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
  const id = useId();

  const shownName = nameTouched ? name : suggestName(username, host);

  // Trash and spam folders the server reported; the user decides whether
  // to archive them before the account is saved.
  const [question, setQuestion] = useState<string[] | null>(null);

  const save = async (extra: AccountInput = {}) => {
    if (locked) return;
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

  return (
    <Form onSubmit={submit} className="page-form">
      <TextInput
        id={`${id}-name`}
        labelText={t.fieldName}
        helperText={t.nameHint}
        required
        maxLength={64}
        value={shownName}
        onChange={(e) => {
          setNameTouched(true);
          setName(e.target.value);
        }}
        placeholder="private"
      />
      <div className="form-row form-row--server">
        <TextInput id={`${id}-host`} labelText={t.fieldHost} required value={host} onChange={(e) => setHost(e.target.value)} placeholder="imap.mail.de" autoComplete="off" />
        <Select id={`${id}-tls`} labelText={t.fieldTLS} value={tls} onChange={(e) => setTLS(e.target.value as TLSMode)}>
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
          onChange={(e) => setPort(e.target.value)}
          placeholder={String(defaultPort[tls])}
        />
      </div>
      <div className="form-row form-row--two">
        <TextInput id={`${id}-username`} labelText={t.fieldUsername} required value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" />
        <PasswordInput
          id={`${id}-password`}
          labelText={t.fieldPassword}
          helperText={t.passwordHint}
          showPasswordLabel={t.carbon.showPassword}
          hidePasswordLabel={t.carbon.hidePassword}
          required={!account}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          placeholder={account ? t.passwordKeep : ""}
          autoComplete="new-password"
        />
      </div>
      {error && <ErrorText text={error} alert />}
      {question && (
        <div className="question">
          <Notice kind="warning" alert text={t.trashSpamQuestion(question.join(", "))} />
          <div className="form-actions">
            <Button kind="secondary" disabled={busy || locked} onClick={() => void save({ confirmFolders: true })}>
              {t.saveWithAll}
            </Button>
            {/* One primary per view: on the Add account page the answer is
                the primary while the question is open; on an account's
                page Sync stays the one primary. */}
            <Button kind={section ? "tertiary" : "primary"} disabled={busy || locked} onClick={() => void save({ confirmFolders: true, excludedFolders: question })}>
              {t.saveWithout}
            </Button>
          </div>
        </div>
      )}
      <FormButtons section={section} busy={busy} locked={locked} question={!!question} onCancel={onCancel}>
        {busy && <InlineLoading className="inline-loading--auto" description={t.checkingLogin} iconDescription={t.loading} />}
      </FormButtons>
    </Form>
  );
}

const guideURL = "https://pklnx.github.io/mail-archive/guide/journaling";

/**
 * The "Add account" form: admins first choose where the mail comes from,
 * an IMAP mailbox or SMTP journaling; everyone else adds an IMAP account.
 */
export function NewAccount({ admin, smtpOn, onCancel, onCreated, onSaved, onFill }: {
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
    <div className="page-form">
      {admin && !created && (
        <RadioButtonGroup
          name={name}
          legendText={t.sourceChoice}
          orientation="vertical"
          valueSelected={source}
          onChange={(v) => setSource(v === "smtp" ? "smtp" : "imap")}
        >
          {(["imap", "smtp"] as const).map((k) => (
            <RadioButton
              key={k}
              id={`${name}-${k}`}
              value={k}
              labelText={
                <span className="radio-label">
                  {k === "imap" ? t.sourceIMAP : t.sourceSMTP}
                  <span className="radio-label__hint">{k === "imap" ? t.sourceSnapshotHint : t.sourceCompleteHint}</span>
                </span>
              }
            />
          ))}
        </RadioButtonGroup>
      )}
      {source === "imap" ? (
        <AccountForm onCancel={onCancel} onSaved={onSaved} />
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
    </div>
  );
}

/** Addresses without empty entries, as typed. */
export function addressList(values: string[]): string[] {
  return values.map((v) => v.trim()).filter((v) => v !== "");
}

/**
 * Creates an smtp account (admins), or renames one and changes its
 * addresses (addresses: admins only). A new account shows the mail server
 * setup before the page closes.
 */
export function SMTPAccountForm({ account, section = false, locked, admin, smtpOn, onCancel, onCreated, onSaved, onFill }: {
  account?: Account;
  /** In a section of the account's page (#285): no primary button. */
  section?: boolean;
  /** Save waits: an earlier rename is not in the account list yet. */
  locked?: boolean;
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
  const id = useId();
  const canAddresses = admin;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (locked) return;
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
      <div className="page-form">
        <Notice kind="success" text={t.smtpAccountAdded(name.trim())} />
        <PostfixPanel addresses={created} />
        <div className="form-actions">
          {onFill && (
            <Button kind="secondary" onClick={() => onFill(name.trim())}>
              {t.importFromIMAP}
            </Button>
          )}
          <Button kind="primary" onClick={() => onSaved(name.trim())}>
            {t.done}
          </Button>
        </div>
      </div>
    );
  }

  return (
    <Form onSubmit={(e) => void submit(e)} className="page-form">
      {!smtpOn && (
        // A notification takes no interactive content: the link follows it.
        <div className="notice-with-link">
          <Notice kind="warning" text={t.smtpServerOff} />
          <Link href={guideURL} target="_blank" rel="noreferrer">
            {t.journalingGuide}
          </Link>
        </div>
      )}
      <TextInput id={`${id}-name`} labelText={t.fieldName} required maxLength={64} value={name} onChange={(e) => setName(e.target.value)} placeholder="journal" />
      <FormGroup legendText={t.fieldAddresses} aria-describedby={`${id}-addresses`} className="address-list">
        {addresses.map((addr, i) => (
          <div key={i} className="address-list__row">
            <TextInput
              id={`${id}-address-${i}`}
              labelText={`${t.fieldAddresses} ${i + 1}`}
              hideLabel
              type="email"
              required={i === 0}
              disabled={!canAddresses}
              value={addr}
              onChange={(e) => setAddresses(addresses.map((a, j) => (i === j ? e.target.value : a)))}
              placeholder="archive@archive.local"
              autoComplete="off"
            />
            {canAddresses && addresses.length > 1 && (
              <IconButton kind="ghost" label={t.removeAddress(addr)} align="left" onClick={() => setAddresses(addresses.filter((_, j) => j !== i))}>
                <Close />
              </IconButton>
            )}
          </div>
        ))}
        {canAddresses && (
          <div>
            <Button kind="tertiary" size="sm" renderIcon={Add} onClick={() => setAddresses([...addresses, ""])}>
              {t.addAddress}
            </Button>
          </div>
        )}
        <p id={`${id}-addresses`} className="cds--form__helper-text">
          {canAddresses ? t.addressHint : t.addressesAdminOnly}
        </p>
      </FormGroup>
      {error && <ErrorText text={error} alert />}
      <FormButtons section={section} busy={busy} locked={locked} onCancel={onCancel} />
    </Form>
  );
}

/**
 * What Postfix needs to send an smtp account its copies, with Carbon's copy
 * button. Inside a section named for it, it is no region of its own.
 */
export function PostfixPanel({ addresses, inSection = false }: { addresses: string[]; inSection?: boolean }) {
  const setup = postfixSetup(addresses);
  const content = (
    <>
      {!setup.dedicated && <Notice kind="warning" text={t.setupNotDedicated(setup.address)} />}
      <p>{t.setupIntro}</p>
      <CodeSnippet
        type="multi"
        className="postfix__code"
        aria-label={t.carbon.copy}
        copyButtonDescription={t.carbon.copy}
        feedback={t.carbon.copied}
        showMoreText={t.carbon.showMore}
        showLessText={t.carbon.showLess}
        maxCollapsedNumberOfRows={20}
      >
        {setup.text}
      </CodeSnippet>
      <p className="app-text-secondary app-text-small">
        {t.setupTarget}{" "}
        <Link href={guideURL} target="_blank" rel="noreferrer" inline size="sm">
          {t.journalingGuide}
        </Link>
      </p>
    </>
  );
  return inSection ? (
    <div className="postfix">{content}</div>
  ) : (
    <section className="postfix" aria-label={t.mailServerSetup}>
      {content}
    </section>
  );
}

/** The result of a section's save, for its status line: an explicit state, never a comparison of texts (#310). */
export type SaveResult = { ok: true } | { ok: false; error: string };

const failure = (err: unknown): SaveResult => ({ ok: false, error: t.failed(err instanceof Error ? err.message : String(err)) });

/**
 * Sets how often the schedule syncs the account. It sends only a changed
 * choice, so a value stored before the server's floor was raised is kept.
 */
export function ScheduleForm({ account, data, locked, onCancel, onSaved }: {
  account: Account;
  data: AccountsResponse;
  locked?: boolean;
  onCancel: () => void;
  onSaved: () => void;
}) {
  const [choice, setChoice] = useState(account.syncInterval);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<SaveResult | null>(null);
  const id = useId();
  const off = !data.syncInterval;
  const unchanged = choice === account.syncInterval;

  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (unchanged || locked) return;
    setBusy(true);
    setResult(null);
    try {
      await accountsApi.update(account.name, { syncInterval: choice });
      onSaved();
      return;
    } catch (err) {
      setResult(failure(err));
    }
    setBusy(false);
  };

  return (
    <Form onSubmit={(e) => void save(e)} className="page-form">
      <Select
        id={id}
        className="field--narrow"
        labelText={t.scheduleTitle}
        value={choice}
        disabled={off || busy}
        onChange={(e) => setChoice(e.target.value)}
        helperText={off ? t.scheduleOffHint : choice === "manual" ? t.intervalManualHint : undefined}
      >
        {scheduleOptions(account, data).map((o) => (
          <SelectItem key={o.value} value={o.value} text={o.label} />
        ))}
      </Select>
      {result && !result.ok && <ErrorText text={result.error} alert />}
      <div className="form-actions">
        <Button kind="ghost" onClick={onCancel} disabled={busy}>
          {t.cancel}
        </Button>
        <Button type="submit" kind="tertiary" disabled={off || busy || unchanged || locked}>
          {t.saveSchedule}
        </Button>
      </div>
    </Form>
  );
}

/** Chooses the folders to archive: the list comes live from the server, checked folders are archived. */
export function FolderForm({ account, locked, onCancel, onSaved }: { account: Account; locked?: boolean; onCancel: () => void; onSaved: () => void }) {
  const [folders, setFolders] = useState<ServerFolder[] | null>(null);
  const [busy, setBusy] = useState(true);
  const [result, setResult] = useState<SaveResult | null>(null);
  const id = useId();
  const name = account.name;

  useEffect(() => {
    let live = true;
    accountsApi.serverFolders(name).then(
      (f) => {
        if (!live) return;
        setFolders(f);
        setBusy(false);
      },
      (err: unknown) => {
        if (!live) return;
        setResult(failure(err));
        setBusy(false);
      },
    );
    return () => {
      live = false;
    };
    // Loaded once when the section opens: a rename of the account must not
    // load the list again over the choices made in it.
  }, []);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (!folders || locked) return;
    setBusy(true);
    setResult(null);
    try {
      await accountsApi.update(account.name, { excludedFolders: folders.filter((f) => !f.selected).map((f) => f.name) });
      onSaved();
      return;
    } catch (err) {
      setResult(failure(err));
    }
    setBusy(false);
  };

  return (
    <Form onSubmit={(e) => void save(e)} className="page-form">
      {!folders && busy && <p className="app-text-secondary">{t.loading}</p>}
      {folders && <p className="app-text-secondary">{t.foldersHint}</p>}
      {folders && (
        <div className="checkbox-list">
          {folders.map((f, i) => (
            <Checkbox
              key={f.name}
              id={`${id}-folder-${i}`}
              labelText={f.specialUse ? `${f.name} (${t.role(f.specialUse)})` : f.name}
              checked={f.selected}
              disabled={busy}
              onChange={(_, { checked }) => setFolders(folders.map((g, j) => (i === j ? { ...g, selected: checked } : g)))}
            />
          ))}
        </div>
      )}
      {result && !result.ok && <ErrorText text={result.error} alert />}
      <div className="form-actions">
        <Button kind="ghost" onClick={onCancel}>
          {t.cancel}
        </Button>
        {folders && (
          <Button type="submit" kind="tertiary" disabled={busy || locked}>
            {t.saveFolders}
          </Button>
        )}
      </div>
    </Form>
  );
}
