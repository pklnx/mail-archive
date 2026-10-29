import { useEffect, useId, useLayoutEffect, useRef, useState, type ReactNode, type RefObject } from "react";
import { Button, InlineLoading, MenuItem, MenuItemDivider } from "@carbon/react";
import { accountsApi, type Account, type AccountsResponse } from "./api";
import { PurgeDialog, PurgeProgress } from "./AccountPurge";
import { FillSection } from "./FillPanel";
import { formatCount, relativeTime } from "./format";
import { t } from "./i18n";
import {
  AccountForm,
  FolderForm,
  isBusy,
  messageCount,
  Notice,
  PostfixPanel,
  reconcileNote,
  scheduleNote,
  ScheduleForm,
  scheduleOptions,
  SMTPAccountForm,
  StateTags,
  syncHealthNote,
  syncStatus,
  tlsLabel,
  TypeTag,
  usePurgeCancelled,
} from "./accountParts";
import { AppLink, DetailHeader, RowMenu, SummaryTiles } from "./settingsParts";
import { settingsText } from "./settingsText";

export interface AccountDetailProps {
  account: Account;
  data: AccountsResponse;
  admin: boolean;
  /** The page's h1: the focus target of a page change and after a purge is confirmed. */
  heading: RefObject<HTMLHeadingElement | null>;
  /** A new name is saved but not in the account list yet: the sections cannot save until it is. */
  renaming: boolean;
  /** The error of the last action, under the title row. */
  notices: ReactNode;
  /** The overflow menu's ref (useMenuButtons), and its button, for the focus after a dialog. */
  register: (el: HTMLElement | null) => void;
  menuButton: () => HTMLElement | null;
  list: { href: string; go: () => void };
  gone: { href: string; go: () => void };
  fill: { href: string; go: () => void };
  /** Runs an action and reloads the list; resolves to whether it worked. */
  run: (action: () => Promise<unknown>) => Promise<boolean>;
  /** Asks before removing or disabling, in the page's danger Modal. */
  ask: (kind: "remove" | "disable") => void;
  reload: () => void;
  /** Connection was saved with a new name. */
  renamed: (to: string) => void;
  /** The list's Mail server setup opened the page: the focus goes to that section, once. */
  focusSetup: boolean;
  setupFocused: () => void;
}

/**
 * An account's page (#285, maintainer decisions of 2026-10-27): the title
 * row with Sync and Check server and the overflow menu, the summary tiles,
 * then the sections, each with its own Edit and Save. A section keeps its
 * draft in its own state, so saving one never drops what is being edited
 * in another, and a reload of the account list does not reset it either.
 * smtp, import and removed accounts get the sections that apply to them;
 * without management (no secret key) the page is read only.
 */
export function AccountDetail(p: AccountDetailProps) {
  const { account: a, data, admin, heading, menuButton, run, reload } = p;
  const s = settingsText();
  const manage = data.manage;
  const busy = isBusy(a);
  const canAct = manage && !a.removed;
  const [cancelled, setCancelled] = usePurgeCancelled(a);
  const [reviewing, setReviewing] = useState(false);
  // How the purge dialog was left: confirmed (the focus goes to the h1, the
  // menu has no item left) or closed (back to the menu's button).
  const left = useRef<"confirmed" | "closed" | null>(null);
  const setupHeading = useRef<HTMLHeadingElement>(null);

  useEffect(() => {
    // A purge confirmed elsewhere drops the dialog.
    if (a.purging) setReviewing(false);
  }, [a.purging]);
  useLayoutEffect(() => {
    if (reviewing || !left.current) return;
    (left.current === "confirmed" ? heading.current : menuButton())?.focus();
    left.current = null;
  }, [reviewing, heading, menuButton]);
  // A passive effect, so it runs after the page's layout effect that moves
  // the focus to the h1 on a page change.
  const { focusSetup, setupFocused } = p;
  useEffect(() => {
    if (!focusSetup) return;
    setupHeading.current?.focus();
    setupFocused();
  }, [focusSetup, setupFocused]);

  const status = syncStatus(a);
  const health = syncHealthNote(a);
  const reconcile = reconcileNote(a);

  let menu: ReactNode = null;
  if (canAct) {
    menu = (
      <RowMenu label={t.userActions(a.name)} register={p.register}>
        {a.kind !== "import" && (
          <MenuItem label={a.enabled ? t.disable : t.enable} onClick={() => (a.enabled ? p.ask("disable") : void run(() => accountsApi.update(a.name, { enabled: true })))} />
        )}
        {a.kind !== "import" && <MenuItemDivider />}
        <MenuItem label={t.remove} kind="danger" disabled={busy} onClick={() => p.ask("remove")} />
      </RowMenu>
    );
  } else if (a.removed && admin && !a.purging) {
    menu = (
      <RowMenu label={t.userActions(a.name)} register={p.register}>
        <MenuItem
          label={t.purgeStart}
          kind="danger"
          onClick={() => {
            setCancelled(false);
            setReviewing(true);
          }}
        />
      </RowMenu>
    );
  }

  const last =
    a.kind === "smtp"
      ? { label: s.lastReceived, value: a.lastReceivedAt ? relativeTime(a.lastReceivedAt) : t.neverReceived }
      : {
          label: a.kind === "import" ? s.lastImport : s.lastSync,
          value: (
            <span className="app-summary__lines">
              {busy ? (
                <InlineLoading className="inline-loading--auto" status="active" description={status.text} iconDescription={t.loading} aria-live="polite" />
              ) : (
                <span className={status.error ? "app-text-error" : undefined}>{status.text}</span>
              )}
              {/* The last error text, under the tile's value. */}
              {status.error && <span className="app-text-error app-text-small app-break-words">{status.error}</span>}
            </span>
          ),
        };
  const tiles = [
    { label: t.messages, value: <span className="app-tabular">{formatCount(messageCount(a))}</span> },
    ...(a.kind === "imap" && !a.removed && a.goneMessages > 0
      ? [
          {
            label: t.onlyInArchive,
            value: (
              <AppLink href={p.gone.href} go={p.gone.go}>
                <span className="app-tabular">{formatCount(a.goneMessages)}</span>
              </AppLink>
            ),
          },
        ]
      : []),
    { label: s.foldersArchived, value: <span className="app-tabular">{formatCount(a.folders.length)}</span> },
    ...(a.removed ? [] : [last]),
  ];

  return (
    <>
      <DetailHeader
        parent={{ label: t.accountsTitle, href: p.list.href, go: p.list.go }}
        title={a.name}
        heading={heading}
        tags={
          <>
            <TypeTag account={a} />
            <StateTags account={a} smtpOn={data.smtp != null} />
          </>
        }
        actions={
          canAct &&
          a.kind === "imap" && (
            <>
              <Button kind="primary" disabled={busy} onClick={() => void run(() => accountsApi.sync(a.name))}>
                {t.syncNow}
              </Button>
              <Button kind="tertiary" disabled={busy} title={t.checkServerHint} onClick={() => void run(() => accountsApi.reconcile(a.name))}>
                {t.checkServer}
              </Button>
            </>
          )
        }
        menu={menu}
      >
        {reconcile && <p className="page-header__intro app-text-secondary">{reconcile.check}</p>}
        {a.removed && <p className="page-header__intro app-text-secondary">{t.removedNote}</p>}
      </DetailHeader>
      {p.notices}
      <SummaryTiles items={tiles} />
      {health && <Notice kind={health.failing ? "error" : "warning"} text={health.text} />}
      {!a.removed && !a.enabled && a.kind !== "import" && <p className="app-text-warning">{a.kind === "smtp" ? t.smtpDisabledNote : t.disabledNote}</p>}
      {!a.removed && a.kind === "smtp" && !data.smtp && <Notice kind="warning" text={t.smtpServerOffCard} />}
      {a.purging && <PurgeProgress purge={a.purge} />}
      {a.removed && cancelled && (
        <p role="status" className="app-text-warning">
          {t.purgeCancelled}
        </p>
      )}
      {reviewing && (
        <PurgeDialog
          name={a.name}
          onClose={() => {
            left.current = "closed";
            setReviewing(false);
          }}
          onConflict={reload}
          onConfirmed={() => {
            left.current = "confirmed";
            setReviewing(false);
            reload();
          }}
        />
      )}

      {!a.removed && a.kind === "imap" && (
        <>
          <EditSection
            title={s.connection}
            canEdit={canAct}
            view={
              <Fields
                items={[
                  [t.fieldName, a.name],
                  [t.fieldHost, a.host],
                  [t.fieldTLS, tlsLabel(a.tls)],
                  [t.fieldPort, String(a.port)],
                  [t.fieldUsername, a.username],
                ]}
              />
            }
            form={(close) => (
              <AccountForm
                account={a}
                section
                locked={p.renaming}
                onCancel={() => close(false)}
                onSaved={(name) => {
                  close(true);
                  if (name !== a.name) p.renamed(name);
                  reload();
                }}
              />
            )}
          />
          <EditSection
            title={t.scheduleTitle}
            canEdit={canAct}
            view={<ScheduleView account={a} data={data} />}
            form={(close) => (
              <ScheduleForm
                account={a}
                data={data}
                locked={p.renaming}
                onCancel={() => close(false)}
                onSaved={() => {
                  close(true);
                  reload();
                }}
              />
            )}
          />
        </>
      )}
      {!a.removed && a.kind === "smtp" && (
        <>
          <EditSection
            title={s.connection}
            canEdit={canAct}
            view={
              <Fields
                items={[
                  [t.fieldName, a.name],
                  [t.fieldAddresses, a.addresses.join(", ")],
                ]}
              />
            }
            form={(close) => (
              <SMTPAccountForm
                account={a}
                section
                locked={p.renaming}
                admin={admin}
                smtpOn={data.smtp != null}
                onCancel={() => close(false)}
                onSaved={(name) => {
                  close(true);
                  if (name !== a.name) p.renamed(name);
                  reload();
                }}
              />
            )}
          />
          {/* Without management too, as on the list before (#285). */}
          <SetupSection account={a} heading={setupHeading} />
          {(manage || a.fills.length > 0) && (
            <PlainSection title={s.imports}>
              <FillSection account={a} data={data} manage={manage} onChanged={reload} />
              {manage && (
                <div>
                  <Button kind="tertiary" size="sm" disabled={busy || a.fills.some((f) => f.status === "running")} onClick={p.fill.go}>
                    {t.importFromIMAP}
                  </Button>
                </div>
              )}
            </PlainSection>
          )}
        </>
      )}
      {!a.removed && a.kind !== "smtp" && (
        <EditSection
          title={t.folders}
          canEdit={canAct && a.kind === "imap"}
          view={<FolderView account={a} />}
          form={(close) => (
            <FolderForm
              account={a}
              locked={p.renaming}
              onCancel={() => close(false)}
              onSaved={() => {
                close(true);
                reload();
              }}
            />
          )}
        />
      )}
    </>
  );
}

/** Label and value pairs, the read view of a section. */
function Fields({ items }: { items: [string, string][] }) {
  return (
    <dl className="app-fields">
      {items.map(([label, value]) => (
        <div key={label} className="app-fields__row">
          <dt className="app-text-secondary">{label}</dt>
          <dd className="app-break-words">{value}</dd>
        </div>
      ))}
    </dl>
  );
}

function ScheduleView({ account: a, data }: { account: Account; data: AccountsResponse }) {
  const choice = scheduleOptions(a, data).find((o) => o.value === a.syncInterval)?.label ?? a.syncInterval;
  const note = scheduleNote(a, data);
  // The note says the choice too, with when next ("Every 6 hours (default) ·
  // next in 3 hours"); without one (disabled, schedule off) the choice shows.
  return (
    <div className="app-lines">
      <p>{note ?? choice}</p>
      {!data.syncInterval && <p className="app-text-secondary">{t.scheduleOffHint}</p>}
    </div>
  );
}

function FolderView({ account: a }: { account: Account }) {
  const s = settingsText();
  return (
    <div className="app-lines">
      {a.folders.length > 0 && (
        <ul className="app-inline-list">
          {a.folders.map((f) => (
            <li key={f.name}>
              {f.name} <span className="app-tabular app-text-secondary">({formatCount(f.messages)})</span>
            </li>
          ))}
        </ul>
      )}
      {a.excludedFolders.length > 0 && <p className="app-text-secondary app-break-words">{s.notArchived(a.excludedFolders.join(", "))}</p>}
    </div>
  );
}

/** A section with a heading and no editing. */
function PlainSection({ title, heading, children }: { title: string; heading?: RefObject<HTMLHeadingElement | null>; children: ReactNode }) {
  const id = useId();
  return (
    <section className="page__section" aria-labelledby={id}>
      <h2 id={id} ref={heading} tabIndex={-1} className="type-heading-03 app-page-title">
        {title}
      </h2>
      {children}
    </section>
  );
}

function SetupSection({ account: a, heading }: { account: Account; heading: RefObject<HTMLHeadingElement | null> }) {
  return (
    <PlainSection title={t.mailServerSetup} heading={heading}>
      <PostfixPanel addresses={a.addresses} inSection />
    </PlainSection>
  );
}

/**
 * A section with its own Edit: the read view, or the form in its place.
 * Opening the form moves the focus to its first field (to the heading
 * while the form has none yet), closing it back to Edit. After a save the
 * read view says so.
 */
function EditSection({ title, canEdit, view, form }: {
  title: string;
  canEdit: boolean;
  view: ReactNode;
  /** The form; close(true) after a save, close(false) on Cancel. */
  form: (close: (saved: boolean) => void) => ReactNode;
}) {
  const id = useId();
  const [editing, setEditing] = useState(false);
  const [saved, setSaved] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const edit = useRef<HTMLButtonElement>(null);
  const wasEditing = useRef(false);
  useLayoutEffect(() => {
    if (editing && !wasEditing.current) {
      const field = box.current?.querySelector<HTMLElement>("input:not(:disabled), select:not(:disabled), textarea:not(:disabled)");
      (field ?? heading.current)?.focus();
    } else if (!editing && wasEditing.current) {
      edit.current?.focus();
    }
    wasEditing.current = editing;
  }, [editing]);
  const close = (ok: boolean) => {
    setEditing(false);
    setSaved(ok);
  };
  return (
    <section className="page__section" aria-labelledby={id}>
      <div className="app-section-head">
        <h2 id={id} ref={heading} tabIndex={-1} className="type-heading-03 app-page-title">
          {title}
        </h2>
        {canEdit && !editing && (
          <Button
            ref={edit}
            kind="ghost"
            size="sm"
            aria-describedby={id}
            onClick={() => {
              setSaved(false);
              setEditing(true);
            }}
          >
            {t.edit}
          </Button>
        )}
      </div>
      {editing ? (
        <div ref={box}>{form(close)}</div>
      ) : (
        <>
          {view}
          {saved && (
            <p role="status" className="app-text-secondary">
              {t.saved}
            </p>
          )}
        </>
      )}
    </section>
  );
}
