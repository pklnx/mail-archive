import { useEffect, useId, useLayoutEffect, useRef, useState, type FormEvent, type MouseEvent, type ReactNode } from "react";
import {
  Accordion,
  AccordionItem,
  Breadcrumb,
  BreadcrumbItem,
  Button,
  Checkbox,
  CodeSnippet,
  Form,
  FormGroup,
  IconButton,
  InlineLoading,
  InlineNotification,
  Layer,
  Link,
  OperationalTag,
  PasswordInput,
  RadioButton,
  RadioButtonGroup,
  Select,
  SelectItem,
  Tag,
  TextInput,
  Tile,
  Tooltip,
} from "@carbon/react";
import { Add, Close } from "@carbon/icons-react";
import { accountsApi, folderQuestion, type Account, type AccountInput, type AccountsResponse, type ServerFolder, type TLSMode } from "./api";
import { suggestName } from "./accountName";
import { isPlainClick } from "./clicks";
import { DangerModal } from "./DangerModal";
import { durationMinutes, formatCount, formatEvery, formatInterval, relativeTime, shortDate } from "./format";
import { t } from "./i18n";
import { FillForm, FillSection } from "./FillPanel";
import { PurgeDialog, PurgeProgress } from "./AccountPurge";
import { postfixSetup } from "./journalSetup";
import type { AccountsState } from "./useAccounts";
import { useViewState, type ViewState } from "./urlState";

interface Props {
  accounts: AccountsState;
  /** Admins can add smtp accounts and change their addresses. */
  admin: boolean;
  /** Called after an account got a new name, to update links to it. */
  renamed: (from: string, to: string) => void;
  /** Lists an account's messages that are no longer on its server. */
  showGone: (account: string) => void;
}

/** A confirmation the list asks for in a danger Modal. */
type Asking = { kind: "remove" | "disable"; account: Account } | null;

/** The pages under Accounts, from the URL (?view=accounts&add=1, &edit=NAME, &import=NAME). */
type Page = { kind: "list" } | { kind: "add" } | { kind: "edit"; name: string } | { kind: "import"; name: string };

function pageOf(s: ViewState): Page {
  if (s.add === "1") return { kind: "add" };
  if (s.edit) return { kind: "edit", name: s.edit };
  if (s.import) return { kind: "import", name: s.import };
  return { kind: "list" };
}

/**
 * Account management: the list of accounts, and the Add, Edit and Import
 * from IMAP pages. The page is in the URL, so Back and reload work; a page
 * for an account that does not exist (any more) returns to the list.
 */
export function AccountsPage({ accounts, admin, renamed, showGone }: Props) {
  const { data, error, reload } = accounts;
  const [state, update] = useViewState();
  const page = pageOf(state);
  // A page for an account just created, until the next account list has it.
  const [awaiting, setAwaiting] = useState<{ name: string; data: AccountsResponse | null } | null>(null);

  // Opening a page is a history entry; finishing one replaces it, so Back
  // does not lead into a form that is done.
  const go = (p: Partial<ViewState>, replace = false) => update({ view: "accounts", add: "", edit: "", import: "", ...p }, { replace });
  const toList = () => go({}, true);

  const manage = data?.manage ?? false;
  const active = data?.accounts.filter((a) => !a.removed) ?? [];
  const target =
    page.kind === "edit"
      ? // Imported accounts have nothing to edit.
        active.find((a) => a.name === page.name && a.kind !== "import")
      : page.kind === "import"
        ? active.find((a) => a.name === page.name && a.kind === "smtp")
        : undefined;
  const waiting = awaiting !== null && page.kind !== "list" && page.kind !== "add" && awaiting.name === page.name && awaiting.data === data;
  // Only pages that need management, and only accounts that exist.
  const unknown = data !== null && page.kind !== "list" && !waiting && (!manage || (page.kind !== "add" && !target));

  useEffect(() => {
    if (unknown) update({ add: "", edit: "", import: "" }, { replace: true });
  }, [unknown, update]);
  useEffect(() => {
    if (awaiting && awaiting.data !== data) setAwaiting(null);
  }, [awaiting, data]);

  const crumb = (current: string) => (
    <Breadcrumb aria-label={t.carbon.breadcrumb} noTrailingSlash className="page__breadcrumb">
      <BreadcrumbItem
        href="?view=accounts"
        onClick={(e: MouseEvent) => {
          if (!isPlainClick(e)) return;
          e.preventDefault();
          go({});
        }}
      >
        {t.accountsTitle}
      </BreadcrumbItem>
      <BreadcrumbItem isCurrentPage>{current}</BreadcrumbItem>
    </Breadcrumb>
  );

  let content: ReactNode;
  if (page.kind === "list" || unknown) {
    content = <AccountList data={data} error={error} reload={reload} admin={admin} manage={manage} active={active} go={go} showGone={showGone} />;
  } else if (!data || waiting) {
    content = <p className="app-text-secondary">{t.loading}</p>;
  } else if (page.kind === "add") {
    content = (
      <>
        {crumb(t.addAccount)}
        <h1 className="page__title type-heading-04">{t.addAccount}</h1>
        <NewAccount
          admin={admin}
          smtpOn={data.smtp != null}
          onCancel={toList}
          onCreated={reload}
          onSaved={() => {
            reload();
            toList();
          }}
          onFill={(name) => {
            setAwaiting({ name, data });
            reload();
            go({ import: name }, true);
          }}
        />
      </>
    );
  } else if (page.kind === "edit" && target) {
    const a = target;
    const onSaved = (name: string) => {
      if (name !== a.name) renamed(a.name, name);
      reload();
      toList();
    };
    content = (
      <>
        {crumb(a.name)}
        <h1 className="page__title type-heading-04">{t.editAccountTitle(a.name)}</h1>
        {a.kind === "smtp" ? (
          <SMTPAccountForm account={a} admin={admin} smtpOn={data.smtp != null} onCancel={toList} onSaved={onSaved} />
        ) : (
          <>
            <section className="page__section">
              <h2 className="type-heading-03">
                {t.accountSection}
              </h2>
              <AccountForm account={a} onCancel={toList} onSaved={onSaved} />
            </section>
            {a.kind === "imap" && <SchedulePicker account={a} data={data} onSaved={reload} />}
            {a.kind === "imap" && <FolderPicker account={a} onSaved={reload} />}
          </>
        )}
      </>
    );
  } else if (page.kind === "import" && target) {
    content = (
      <>
        {crumb(t.importFromIMAP)}
        <h1 className="page__title type-heading-04">{t.importFromIMAP}</h1>
        <p className="page__subtitle app-text-secondary">{target.name}</p>
        <FillForm
          account={target}
          data={data}
          onCancel={toList}
          onStarted={() => {
            reload();
            toList();
          }}
        />
      </>
    );
  }

  return (
    <div className="accounts-page">
      <div className="page">{content}</div>
    </div>
  );
}

/** The list page: the header with its actions, one tile per account, the removed accounts. */
function AccountList({ data, error, reload, admin, manage, active, go, showGone }: {
  data: AccountsResponse | null;
  error: string;
  reload: () => void;
  admin: boolean;
  manage: boolean;
  active: Account[];
  go: (p: Partial<ViewState>) => void;
  showGone: (account: string) => void;
}) {
  const [actionError, setActionError] = useState("");
  const [asking, setAsking] = useState<Asking>(null);
  const removed = data?.accounts.filter((a) => a.removed) ?? [];
  // Removed accounts are folded away; open while one is being deleted, or
  // right after removing one, so it shows where it went.
  const [removedOpen, setRemovedOpen] = useState(() => removed.some((a) => a.purging));

  const run = async (action: () => Promise<unknown>) => {
    setActionError("");
    let ok = true;
    try {
      await action();
    } catch (err) {
      ok = false;
      setActionError(t.failed(err instanceof Error ? err.message : String(err)));
    }
    reload();
    return ok;
  };

  return (
    <>
      <header className="page-header">
        <div className="page-header__row">
          <h1 className="page-header__title type-heading-04">{t.accountsTitle}</h1>
          {manage && (
            <div className="page-header__actions">
              <Button kind="tertiary" disabled={syncableCount(active) === 0} onClick={() => void run(accountsApi.syncAll)}>
                {t.syncAll}
              </Button>
              <Button kind="primary" renderIcon={Add} onClick={() => go({ add: "1" })}>
                {t.addAccount}
              </Button>
            </div>
          )}
        </div>
        {data && (
          <p className="page-header__intro app-text-secondary">
            {!manage ? t.manageOff : data.syncInterval ? t.schedule(formatEvery(data.syncInterval)) : t.scheduleOff}
            {manage && " " + (data.reconcileInterval ? t.reconcileSchedule(formatInterval(data.reconcileInterval)) : t.reconcileOff)}
          </p>
        )}
      </header>
      {error && !data && <ErrorText text={t.loadAccountsFailed(error)} />}
      {actionError && <ErrorText text={actionError} alert />}

      {data && active.length === 0 && <p className="app-text-secondary">{t.noAccounts}</p>}

      <div className="account-list">
        {active.map((a) => (
          <AccountCard
            key={a.name}
            account={a}
            data={data!}
            manage={manage}
            onSync={() => void run(() => accountsApi.sync(a.name))}
            onReconcile={() => void run(() => accountsApi.reconcile(a.name))}
            onShowGone={() => showGone(a.name)}
            onEdit={() => go({ edit: a.name })}
            onFill={() => go({ import: a.name })}
            onChanged={reload}
            onToggle={() => {
              // Disabling asks first; enabling does not.
              if (a.enabled) setAsking({ kind: "disable", account: a });
              else void run(() => accountsApi.update(a.name, { enabled: true }));
            }}
            onRemove={() => setAsking({ kind: "remove", account: a })}
          />
        ))}
      </div>

      {asking && (
        <DangerModal
          size="xs"
          modalHeading={asking.kind === "remove" ? t.confirmRemoveTitle(asking.account.name) : t.confirmDisableTitle(asking.account.name)}
          primaryButtonText={asking.kind === "remove" ? t.remove : t.disable}
          secondaryButtonText={t.cancel}
          preventCloseOnClickOutside
          onRequestClose={() => setAsking(null)}
          onRequestSubmit={() => {
            const { kind, account: a } = asking;
            setAsking(null);
            if (kind === "remove") {
              void run(() => accountsApi.remove(a.name)).then((ok) => ok && setRemovedOpen(true));
            } else {
              void run(() => accountsApi.update(a.name, { enabled: false }));
            }
          }}
        >
          <p>
            {asking.kind === "remove"
              ? t.confirmRemoveText
              : asking.account.kind === "smtp"
                ? t.confirmDisableSMTPText
                : t.confirmDisableText}
          </p>
        </DangerModal>
      )}

      {removed.length > 0 && (
        <Accordion className="removed-accounts">
          <AccordionItem
            title={t.removedAccountsCount(removed.length)}
            open={removedOpen}
            onHeadingClick={({ isOpen }) => setRemovedOpen(isOpen)}
          >
            <div className="account-list">
              {removed.map((a) => (
                <RemovedCard key={a.name} account={a} admin={admin} onChanged={reload} />
              ))}
            </div>
          </AccordionItem>
        </Accordion>
      )}
    </>
  );
}

/** A short error: the plain text, so screen readers read it as it is. */
function ErrorText({ text, alert }: { text: string; alert?: boolean }) {
  return (
    <p role={alert ? "alert" : undefined} className="app-text-error app-break-words">
      {text}
    </p>
  );
}

/**
 * A removed account. Admins can delete their own removed accounts
 * completely; the server then deletes in the background. A purge open
 * elsewhere (another tab, the command line) shows as progress, never as
 * the form.
 */
export function RemovedCard({ account: a, admin, onChanged }: { account: Account; admin: boolean; onChanged: () => void }) {
  const [reviewing, setReviewing] = useState(false);
  const [cancelled, setCancelled] = useState(false);
  const start = useRef<HTMLButtonElement>(null);
  const wasReviewing = useRef(false);
  const wasPurging = useRef(a.purging);

  useEffect(() => {
    // Still listed after its purge ended: it was cancelled.
    if (wasPurging.current && !a.purging) setCancelled(true);
    if (a.purging) {
      setReviewing(false);
      setCancelled(false);
    }
    wasPurging.current = a.purging;
  }, [a.purging]);

  useLayoutEffect(() => {
    // Closing the dialog returns the focus to the button that opened it,
    // in the same commit that closes the dialog.
    if (wasReviewing.current && !reviewing) start.current?.focus();
    wasReviewing.current = reviewing;
  }, [reviewing]);

  return (
    <Card name={a.name} level={3} count={messageCount(a)}>
      {a.purging ? (
        <PurgeProgress purge={a.purge} />
      ) : (
        <>
          <p className="app-text-secondary">{t.removedNote}</p>
          {cancelled && (
            <p role="status" className="app-text-warning">
              {t.purgeCancelled}
            </p>
          )}
          {admin && reviewing && (
            <PurgeDialog
              name={a.name}
              onClose={() => setReviewing(false)}
              onConflict={onChanged}
              onConfirmed={() => {
                setReviewing(false);
                onChanged();
              }}
            />
          )}
          {admin && (
            <div className="account-tile__actions">
              <Button
                ref={start}
                kind="danger--tertiary"
                size="sm"
                onClick={() => {
                  setCancelled(false);
                  setReviewing(true);
                }}
              >
                {t.purgeStart}
              </Button>
            </div>
          )}
        </>
      )}
    </Card>
  );
}

/** One account's tile: its name, tags and message count, then the rest. Named by the account, so it is a landmark region. */
function Card({ name, level = 2, tags, count, children }: { name: string; level?: 2 | 3; tags?: ReactNode; count: number; children: ReactNode }) {
  const id = useId();
  const Heading = level === 2 ? "h2" : "h3";
  return (
    <Tile className="account-tile" role="region" aria-labelledby={id}>
      <div className="account-tile__head">
        <Heading id={id} className="account-tile__name">
          {name}
        </Heading>
        {tags && <div className="account-tile__tags">{tags}</div>}
        <span className="account-tile__count">{t.messageCount(count, formatCount(count))}</span>
      </div>
      {/* One layer up from the page, so fields in the tile contrast with it. */}
      <Layer className="account-tile__body">{children}</Layer>
    </Tile>
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

/** The sync or import status line; a spinner while it runs. */
function StatusLine({ status, busy }: { status: { text: string; error?: string }; busy: boolean }) {
  if (busy) {
    return <InlineLoading className="inline-loading--auto" status="active" description={status.text} iconDescription={t.loading} aria-live="polite" />;
  }
  return (
    <p className={status.error ? "app-text-error" : "app-text-secondary"} aria-live="polite">
      {status.text}
    </p>
  );
}

interface CardProps {
  account: Account;
  data: AccountsResponse;
  manage: boolean;
  onSync: () => void;
  onReconcile: () => void;
  onShowGone: () => void;
  onEdit: () => void;
  /** smtp accounts: open the page that imports existing mail from IMAP. */
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
  const setupId = useId();
  const count = messageCount(a);
  const disabledTag = !a.enabled && (
    <Tag size="sm" type="gray">
      {t.disabled}
    </Tag>
  );
  const toggle = (
    <Button kind="tertiary" size="sm" onClick={onToggle}>
      {a.enabled ? t.disable : t.enable}
    </Button>
  );
  const remove = (
    <Button kind="danger--tertiary" size="sm" disabled={busy} onClick={onRemove}>
      {t.remove}
    </Button>
  );

  if (a.kind === "smtp") {
    return (
      <Card
        name={a.name}
        count={count}
        tags={
          <>
            <SourceTag text={t.sourceComplete} hint={t.sourceCompleteHint} />
            {a.enabled && data.smtp && (
              <Tag size="sm" type="green">
                {t.receiving}
              </Tag>
            )}
            {disabledTag}
          </>
        }
      >
        <p className="app-text-secondary app-break-words">{t.receivesFor(a.addresses.join(", "))}</p>
        <p className="app-text-secondary">{receiveStatus(a)}</p>
        {!a.enabled && <p className="app-text-warning">{t.smtpDisabledNote}</p>}
        {!data.smtp && <Notice kind="warning" text={t.smtpServerOffCard} />}
        <div className="account-tile__actions">
          <Button kind="tertiary" size="sm" aria-expanded={showSetup} aria-controls={setupId} onClick={() => setShowSetup(!showSetup)}>
            {showSetup ? t.hideMailServerSetup : t.mailServerSetup}
          </Button>
          {manage && (
            <>
              <Button kind="tertiary" size="sm" disabled={busy || a.fills.some((f) => f.status === "running")} onClick={onFill}>
                {t.importFromIMAP}
              </Button>
              <Button kind="tertiary" size="sm" onClick={onEdit}>
                {t.edit}
              </Button>
              {toggle}
              {remove}
            </>
          )}
        </div>
        <div id={setupId}>{showSetup && <PostfixPanel addresses={a.addresses} />}</div>
        <FillSection account={a} data={data} manage={manage} onChanged={onChanged} />
      </Card>
    );
  }
  if (a.kind === "import") {
    return (
      <Card
        name={a.name}
        count={count}
        tags={
          <Tag size="sm" type="cool-gray">
            {t.imported}
          </Tag>
        }
      >
        <StatusLine status={status} busy={busy} />
        {status.error && <p className="app-text-error app-text-small app-break-words">{status.error}</p>}
        {a.folders.length > 0 && (
          <ul className="app-inline-list app-text-small app-text-secondary">
            {a.folders.map((f) => (
              <li key={f.name}>
                {f.name} <span className="app-tabular">({formatCount(f.messages)})</span>
              </li>
            ))}
          </ul>
        )}
        {manage && <div className="account-tile__actions">{remove}</div>}
      </Card>
    );
  }
  return (
    <Card
      name={a.name}
      count={count}
      tags={
        <>
          <SourceTag text={t.sourceSnapshot} hint={t.sourceSnapshotHint} />
          {a.sync.health === "failing" && (
            <Tag size="sm" type="red">
              {t.tagFailing}
            </Tag>
          )}
          {disabledTag}
        </>
      }
    >
      <p className="app-text-secondary app-text-truncate">
        {a.username} · {a.host}
      </p>
      <StatusLine status={status} busy={busy} />
      {status.error && <p className="app-text-error app-text-small app-break-words">{status.error}</p>}
      {health && <Notice kind={health.failing ? "error" : "warning"} text={health.text} />}
      {!a.enabled && <p className="app-text-warning">{t.disabledNote}</p>}
      {schedule && <p className="app-text-secondary">{schedule}</p>}
      {reconcile && (
        <p className="app-text-secondary">
          {reconcile.gone && (
            <>
              <Link
                href={`?account=${encodeURIComponent(a.name)}&gone=1`}
                onClick={(e: MouseEvent) => {
                  if (!isPlainClick(e)) return;
                  e.preventDefault();
                  onShowGone();
                }}
              >
                {reconcile.gone}
              </Link>
              {" · "}
            </>
          )}
          {reconcile.check}
        </p>
      )}
      {manage && (
        <div className="account-tile__actions">
          <Button kind="tertiary" size="sm" disabled={busy} onClick={onSync}>
            {t.syncNow}
          </Button>
          <Button kind="tertiary" size="sm" disabled={busy} title={t.checkServerHint} onClick={onReconcile}>
            {t.checkServer}
          </Button>
          <Button kind="tertiary" size="sm" onClick={onEdit}>
            {t.edit}
          </Button>
          {toggle}
          {remove}
        </div>
      )}
    </Card>
  );
}

/** A notification inside a tile or form: no close button, takes no focus. */
function Notice({ kind, text, alert }: { kind: "error" | "warning" | "info" | "success"; text: string; alert?: boolean }) {
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
  const id = useId();

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
            <Button kind="secondary" disabled={busy} onClick={() => void save({ confirmFolders: true })}>
              {t.saveWithAll}
            </Button>
            <Button kind="primary" disabled={busy} onClick={() => void save({ confirmFolders: true, excludedFolders: question })}>
              {t.saveWithout}
            </Button>
          </div>
        </div>
      )}
      <div className="form-actions">
        <Button kind="secondary" onClick={onCancel} disabled={busy}>
          {t.cancel}
        </Button>
        <Button type="submit" kind="primary" disabled={busy}>
          {t.save}
        </Button>
        {busy && <InlineLoading className="inline-loading--auto" description={t.checkingLogin} iconDescription={t.loading} />}
      </div>
    </Form>
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
  const id = useId();
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
    <Form onSubmit={submit} className="page-form">
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
      <div className="form-actions">
        <Button kind="secondary" onClick={onCancel} disabled={busy}>
          {t.cancel}
        </Button>
        <Button type="submit" kind="primary" disabled={busy}>
          {t.save}
        </Button>
      </div>
    </Form>
  );
}

/** What Postfix needs to send an smtp account its copies, with Carbon's copy button. */
export function PostfixPanel({ addresses }: { addresses: string[] }) {
  const setup = postfixSetup(addresses);
  return (
    <section className="postfix" aria-label={t.mailServerSetup}>
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
    <section className="page__section">
      <h2 className="type-heading-03">
        {t.scheduleTitle}
      </h2>
      <div className="inline-form">
        <Select
          id={id}
          className="field--narrow"
          labelText={t.scheduleTitle}
          hideLabel
          value={choice}
          disabled={off || busy}
          onChange={(e) => setChoice(e.target.value)}
          helperText={off ? t.scheduleOffHint : choice === "manual" ? t.intervalManualHint : undefined}
        >
          {scheduleOptions(account, data).map((o) => (
            <SelectItem key={o.value} value={o.value} text={o.label} />
          ))}
        </Select>
        <Button kind="tertiary" size="md" disabled={off || busy || choice === account.syncInterval} onClick={() => void save()}>
          {t.saveSchedule}
        </Button>
      </div>
      {message && (
        // Saved, or the failure in the error colour.
        <p role="status" className={message === t.saved ? "app-text-secondary" : "app-text-error"}>
          {message}
        </p>
      )}
    </section>
  );
}

function FolderPicker({ account, onSaved }: { account: Account; onSaved: () => void }) {
  const [folders, setFolders] = useState<ServerFolder[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const id = useId();

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
    <section className="page__section">
      <h2 className="type-heading-03">
        {t.folders}
      </h2>
      {!folders ? (
        <div>
          <Button kind="tertiary" disabled={busy} onClick={() => void load()}>
            {busy ? t.loading : t.loadFolders}
          </Button>
        </div>
      ) : (
        <>
          <p className="app-text-secondary">{t.foldersHint}</p>
          <div className="checkbox-list">
            {folders.map((f, i) => (
              <Checkbox
                key={f.name}
                id={`${id}-folder-${i}`}
                labelText={f.specialUse ? `${f.name} (${t.role(f.specialUse)})` : f.name}
                checked={f.selected}
                onChange={(_, { checked }) => setFolders(folders.map((g, j) => (i === j ? { ...g, selected: checked } : g)))}
              />
            ))}
          </div>
          <div>
            <Button kind="tertiary" disabled={busy} onClick={() => void save()}>
              {t.saveFolders}
            </Button>
          </div>
        </>
      )}
      {message && (
        // Saved, or the failure in the error colour.
        <p role="status" className={message === t.saved ? "app-text-secondary" : "app-text-error"}>
          {message}
        </p>
      )}
    </section>
  );
}
