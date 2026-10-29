import { useEffect, useLayoutEffect, useRef, useState, type MouseEvent, type ReactNode, type RefObject } from "react";
import {
  Accordion,
  AccordionItem,
  Breadcrumb,
  BreadcrumbItem,
  Button,
  InlineLoading,
  MenuItem,
  MenuItemDivider,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableHeader,
  TableRow,
} from "@carbon/react";
import { Add } from "@carbon/icons-react";
import { accountsApi, type Account, type AccountsResponse } from "./api";
import { AccountDetail } from "./AccountDetail";
import { PurgeDialog, PurgeProgress } from "./AccountPurge";
import { isPlainClick } from "./clicks";
import { DangerModal } from "./DangerModal";
import { FillForm } from "./FillPanel";
import { formatCount, formatEvery, formatInterval } from "./format";
import { t } from "./i18n";
import { ViewFallback } from "./lazyView";
import { ErrorText, isBusy, lastActivity, messageCount, NewAccount, StateTags, syncableCount, TypeTag, usePurgeCancelled } from "./accountParts";
import { AppLink, RowMenu, useMenuButtons, usePageFocus } from "./settingsParts";
import { settingsText } from "./settingsText";
import { TableSkeleton } from "./tableSkeleton";
import type { AccountsState } from "./useAccounts";
import { formatState, parseState, useViewState, type ViewState } from "./urlState";

interface Props {
  accounts: AccountsState;
  /** Admins can add smtp accounts and change their addresses. */
  admin: boolean;
  /** Called after an account got a new name, to update links to it. */
  renamed: (from: string, to: string) => void;
  /** Lists an account's messages that are no longer on its server. */
  showGone: (account: string) => void;
}

/** A confirmation the page asks for in a danger Modal. */
type Asking = { kind: "remove" | "disable"; account: Account } | null;

/** The pages under Accounts, from the URL (?view=accounts&add=1, &id=NAME, &import=NAME). */
type Page = { kind: "list" } | { kind: "add" } | { kind: "detail"; name: string } | { kind: "import"; name: string };

function pageOf(s: ViewState): Page {
  if (s.add === "1") return { kind: "add" };
  if (s.id) return { kind: "detail", name: s.id };
  if (s.import) return { kind: "import", name: s.import };
  return { kind: "list" };
}

/** The URL of a page under Accounts. */
const hrefOf = (patch: Partial<ViewState> = {}) => formatState({ ...parseState(""), view: "accounts", ...patch });

/**
 * Account management: the accounts as a table, each with its own page
 * (?view=accounts&id=NAME), and the Add and Import from IMAP pages. The page
 * is in the URL, so Back and reload work; a page for an account that does
 * not exist (any more) returns to the list.
 */
export function AccountsPage({ accounts, admin, renamed, showGone }: Props) {
  const { data, error, reload } = accounts;
  const [state, update] = useViewState();
  const page = pageOf(state);
  // A page for an account just created, until the next account list has it.
  const [awaiting, setAwaiting] = useState<{ name: string; data: AccountsResponse | null } | null>(null);
  // The last rename saved on an account's page. A ref, set before the URL
  // changes: the render for the new id must already know it.
  const renamedTo = useRef<{ from: string; to: string } | null>(null);
  // The page of a renamed account keeps its identity (and the drafts of its
  // sections): a new name maps to the name its page was opened with.
  const aliases = useRef(new Map<string, string>());
  const [asking, setAsking] = useState<Asking>(null);
  // The error of the last action, for the page it was started on.
  const [failure, setFailure] = useState<{ page: string; text: string } | null>(null);
  // Removed accounts are folded away; open while one is being deleted, or
  // right after removing one, so it shows where it went.
  const [removedOpen, setRemovedOpen] = useState(false);
  // The list's Mail server setup opened this account's page: its page moves
  // the focus to that section once (no URL key).
  const [setupFor, setSetupFor] = useState<string | null>(null);
  const menus = useMenuButtons();
  const heading = useRef<HTMLHeadingElement>(null);

  // Opening a page is a history entry; finishing one replaces it, so Back
  // does not lead into a form that is done.
  const go = (p: Partial<ViewState>, replace = false) => update({ view: "accounts", add: "", id: "", import: "", ...p }, { replace });
  const toList = () => go({}, true);

  const manage = data?.manage ?? false;
  const all = data?.accounts ?? [];
  const active = all.filter((a) => !a.removed);
  const named = (name: string) => all.find((a) => a.name === name);

  let target: Account | undefined;
  if (page.kind === "detail") {
    target = named(page.name);
    // Renamed on its page: until the list has the new name, the page shows
    // the account under its old one.
    if (!target && renamedTo.current?.to === page.name) target = named(renamedTo.current.from);
  } else if (page.kind === "import" && manage) {
    target = active.find((a) => a.name === page.name && a.kind === "smtp");
  }
  // Saving on the page waits until the list has the new name.
  // Only the renamed account's page waits, and only until the list has the
  // new name.
  const renaming = page.kind === "detail" && renamedTo.current?.to === page.name && data !== null && !all.some((a) => a.name === page.name);
  const waiting = awaiting !== null && page.kind === "import" && awaiting.name === page.name && awaiting.data === data;
  const unknown = data !== null && !waiting && (page.kind === "add" ? !manage : page.kind !== "list" && !target);
  const shown = unknown ? "list" : page.kind === "detail" ? `detail:${aliases.current.get(page.name) ?? page.name}` : page.kind === "list" || page.kind === "add" ? page.kind : `import:${page.name}`;
  usePageFocus(data === null || waiting ? null : shown, heading);

  useEffect(() => {
    if (unknown) update({ add: "", id: "", import: "" }, { replace: true });
  }, [unknown, update]);
  // An old link with edit=NAME reads as id=NAME (urlState); the URL then says id too.
  useEffect(() => {
    if (new URLSearchParams(window.location.search).has("edit")) update({}, { replace: true });
  }, [update]);
  useEffect(() => {
    if (awaiting && awaiting.data !== data) setAwaiting(null);
  }, [awaiting, data]);
  // The list has the new name: the rename is done, and the old name means
  // nothing for the new one any more.
  useEffect(() => {
    const to = renamedTo.current?.to;
    if (to !== undefined && data?.accounts.some((a) => a.name === to)) renamedTo.current = null;
  }, [data]);
  // A purge that shows up later (the list loaded after the page, or the
  // purge was confirmed elsewhere) opens the removed accounts too (#310).
  const purging = all.some((a) => a.removed && a.purging);
  useEffect(() => {
    if (purging) setRemovedOpen(true);
  }, [purging]);

  const run = async (action: () => Promise<unknown>) => {
    setFailure(null);
    let ok = true;
    try {
      await action();
    } catch (err) {
      ok = false;
      setFailure({ page: shown, text: t.failed(err instanceof Error ? err.message : String(err)) });
    }
    reload();
    return ok;
  };
  const notices = failure?.page === shown && <ErrorText text={failure.text} alert />;
  const ask = (kind: "remove" | "disable", account: Account) => setAsking({ kind, account });
  const toggle = (a: Account) => {
    // Disabling asks first; enabling does not.
    if (a.enabled) ask("disable", a);
    else void run(() => accountsApi.update(a.name, { enabled: true }));
  };

  const crumb = (current: string) => (
    <Breadcrumb aria-label={t.carbon.breadcrumb} noTrailingSlash className="page__breadcrumb">
      <BreadcrumbItem
        href={hrefOf()}
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
  const title = (text: string) => (
    <h1 ref={heading} tabIndex={-1} className="page__title type-heading-04 app-page-title">
      {text}
    </h1>
  );

  let content: ReactNode;
  if (page.kind === "list" || unknown) {
    content = (
      <AccountList
        data={data}
        error={error}
        reload={reload}
        admin={admin}
        heading={heading}
        menus={menus}
        notices={notices}
        removedOpen={removedOpen}
        setRemovedOpen={setRemovedOpen}
        open={(name) => go({ id: name })}
        openSetup={(name) => {
          setSetupFor(name);
          go({ id: name });
        }}
        openImport={(name) => go({ import: name })}
        add={() => go({ add: "1" })}
        run={run}
        ask={ask}
        toggle={toggle}
      />
    );
  } else if (!data && error) {
    // The list never loaded: say so instead of loading for ever (#310).
    content = <ErrorText text={t.loadAccountsFailed(error)} alert />;
  } else if (!data || waiting) {
    content = <ViewFallback inline />;
  } else if (page.kind === "add") {
    content = (
      <>
        {crumb(t.addAccount)}
        {title(t.addAccount)}
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
  } else if (page.kind === "detail" && target) {
    const a = target;
    content = (
      <AccountDetail
        // The page of a renamed account stays the same page.
        key={aliases.current.get(page.name) ?? page.name}
        account={a}
        data={data}
        admin={admin}
        heading={heading}
        renaming={renaming}
        notices={notices}
        register={menus.register(a.name)}
        menuButton={() => menus.button(a.name)}
        list={{ href: hrefOf(), go: () => go({}) }}
        gone={{ href: `?account=${encodeURIComponent(a.name)}&gone=1`, go: () => showGone(a.name) }}
        fill={{ href: hrefOf({ import: a.name }), go: () => go({ import: a.name }) }}
        run={run}
        ask={(kind) => ask(kind, a)}
        reload={reload}
        renamed={(to) => {
          const from = a.name;
          aliases.current.set(to, aliases.current.get(from) ?? from);
          renamedTo.current = { from, to };
          renamed(from, to);
          go({ id: to }, true);
        }}
        focusSetup={setupFor === a.name}
        setupFocused={() => setSetupFor(null)}
      />
    );
  } else if (page.kind === "import" && target) {
    content = (
      <>
        {crumb(t.importFromIMAP)}
        {title(t.importFromIMAP)}
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

  const live = asking ? named(asking.account.name) : undefined;
  return (
    <div className="accounts-page">
      <div className={page.kind === "list" || unknown ? "page page--wide" : "page"}>{content}</div>
      {asking && (
        <ConfirmAccount
          asking={asking}
          // Remove waits while the account syncs, as its menu item does (#310).
          blocked={asking.kind === "remove" && live !== undefined && isBusy(live)}
          returnFocus={() => menus.button(asking.account.name) ?? heading.current}
          cancel={() => setAsking(null)}
          confirm={() => {
            const { kind, account: a } = asking;
            setAsking(null);
            if (kind === "disable") {
              void run(() => accountsApi.update(a.name, { enabled: false }));
              return;
            }
            // Removed from its page: the list replaces the page. Removed
            // from its row: the row goes, and the focus goes to the list's
            // h1 instead of falling to the body (#311).
            const fromDetail = page.kind === "detail";
            void run(() => accountsApi.remove(a.name)).then((ok) => {
              if (!ok) return;
              setRemovedOpen(true);
              if (fromDetail) toList();
              else heading.current?.focus();
            });
          }}
        />
      )}
    </div>
  );
}

/** Asks before removing or disabling an account. */
function ConfirmAccount({ asking, blocked, returnFocus, cancel, confirm }: {
  asking: NonNullable<Asking>;
  /** The account became busy since the dialog opened: nothing is sent. */
  blocked: boolean;
  returnFocus: () => HTMLElement | null;
  cancel: () => void;
  confirm: () => void;
}) {
  const { kind, account: a } = asking;
  return (
    <DangerModal
      size="xs"
      modalHeading={kind === "remove" ? t.confirmRemoveTitle(a.name) : t.confirmDisableTitle(a.name)}
      primaryButtonText={kind === "remove" ? t.remove : t.disable}
      primaryButtonDisabled={blocked}
      secondaryButtonText={t.cancel}
      preventCloseOnClickOutside
      returnFocus={returnFocus}
      onRequestClose={cancel}
      onRequestSubmit={() => {
        if (!blocked) confirm();
      }}
    >
      <p>{kind === "remove" ? t.confirmRemoveText : a.kind === "smtp" ? t.confirmDisableSMTPText : t.confirmDisableText}</p>
    </DangerModal>
  );
}

interface ListProps {
  data: AccountsResponse | null;
  error: string;
  reload: () => void;
  admin: boolean;
  heading: RefObject<HTMLHeadingElement | null>;
  menus: ReturnType<typeof useMenuButtons>;
  notices: ReactNode;
  removedOpen: boolean;
  setRemovedOpen: (open: boolean) => void;
  open: (name: string) => void;
  openSetup: (name: string) => void;
  openImport: (name: string) => void;
  add: () => void;
  run: (action: () => Promise<unknown>) => Promise<boolean>;
  ask: (kind: "remove" | "disable", account: Account) => void;
  toggle: (account: Account) => void;
}

/** The list page: the header with its actions, the accounts table, and the removed accounts in an accordion. */
function AccountList(p: ListProps) {
  const { data, heading } = p;
  const manage = data?.manage ?? false;
  const active = data?.accounts.filter((a) => !a.removed) ?? [];
  const removed = data?.accounts.filter((a) => a.removed) ?? [];
  const removedBox = useRef<HTMLDivElement>(null);
  // A removed row that had the focus went away (its purge finished): the
  // focus goes to the accordion's button, or to the h1 if that went too.
  const lostFocus = useRef(false);
  useLayoutEffect(() => {
    if (!lostFocus.current) return;
    lostFocus.current = false;
    (removedBox.current?.querySelector<HTMLElement>(".cds--accordion__heading") ?? heading.current)?.focus();
  });

  return (
    <>
      <header className="page-header">
        <div className="page-header__row">
          <h1 ref={heading} tabIndex={-1} className="page-header__title type-heading-04 app-page-title">
            {t.accountsTitle}
          </h1>
          {manage && (
            <div className="page-header__actions">
              <Button kind="tertiary" disabled={syncableCount(active) === 0} onClick={() => void p.run(accountsApi.syncAll)}>
                {t.syncAll}
              </Button>
              <Button kind="primary" renderIcon={Add} onClick={p.add}>
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
      {p.error && !data && <ErrorText text={t.loadAccountsFailed(p.error)} />}
      {p.notices}
      {!data && !p.error && <AccountsSkeleton />}

      {data && active.length === 0 && <p className="app-text-secondary">{t.noAccounts}</p>}
      {data && active.length > 0 && <AccountTable {...p} data={data} accounts={active} />}

      {data && removed.length > 0 && (
        <div ref={removedBox}>
          <Accordion className="removed-accounts">
            <AccordionItem title={t.removedAccountsCount(removed.length)} open={p.removedOpen} onHeadingClick={({ isOpen }) => p.setRemovedOpen(isOpen)}>
              <p className="app-text-secondary">{t.removedNote}</p>
              <RemovedTable
                {...p}
                accounts={removed}
                lost={() => {
                  lostFocus.current = true;
                }}
              />
            </AccordionItem>
          </Accordion>
        </div>
      )}
    </>
  );
}

/** The accounts table while the list loads, with the table's columns. */
function AccountsSkeleton() {
  const s = settingsText();
  return (
    <TableSkeleton
      columns={[
        { header: t.accountSection },
        { header: s.colType, wide: true },
        { header: s.colStatus },
        { header: t.messages, wide: true },
        { header: s.colLastActivity, wide: true },
        { header: "" },
      ]}
    />
  );
}

/**
 * The active accounts. The name is the row header and links to the
 * account's page; Sync stays visible on IMAP rows, the rest is in the row's
 * menu with today's disabled states. Below 42rem the secondary columns give
 * way to a second line under the name.
 */
function AccountTable({ data, accounts, ...p }: ListProps & { data: AccountsResponse; accounts: Account[] }) {
  const s = settingsText();
  const manage = data.manage;
  return (
    <TableContainer className="accounts-table app-table">
      <Table aria-label={t.accountsTitle} size="lg" overflowMenuOnHover={false}>
        <TableHead>
          <TableRow>
            <TableHeader>{t.accountSection}</TableHeader>
            <TableHeader className="app-wide-only">{s.colType}</TableHeader>
            <TableHeader>{s.colStatus}</TableHeader>
            <TableHeader className="app-wide-only">{t.messages}</TableHeader>
            <TableHeader className="app-wide-only">{s.colLastActivity}</TableHeader>
            <TableHeader>
              <span className="cds--visually-hidden">{t.colActions}</span>
            </TableHeader>
          </TableRow>
        </TableHead>
        <TableBody>
          {accounts.map((a) => {
            const busy = isBusy(a);
            const activity = lastActivity(a);
            const count = messageCount(a);
            // smtp's Mail server setup is there without management too.
            const menu = (manage || a.kind === "smtp") && (
              <RowMenu label={t.userActions(a.name)} register={p.menus.register(a.name)}>
                {a.kind === "smtp" && <MenuItem label={t.mailServerSetup} onClick={() => p.openSetup(a.name)} />}
                {manage && a.kind === "smtp" && (
                  <MenuItem label={t.importFromIMAP} disabled={busy || a.fills.some((f) => f.status === "running")} onClick={() => p.openImport(a.name)} />
                )}
                {manage && a.kind === "imap" && (
                  <MenuItem label={t.checkServer} disabled={busy} onClick={() => void p.run(() => accountsApi.reconcile(a.name))} />
                )}
                {manage && a.kind !== "import" && <MenuItem label={t.edit} onClick={() => p.open(a.name)} />}
                {manage && a.kind !== "import" && <MenuItem label={a.enabled ? t.disable : t.enable} onClick={() => p.toggle(a)} />}
                {manage && a.kind !== "import" && <MenuItemDivider />}
                {manage && <MenuItem label={t.remove} kind="danger" disabled={busy} onClick={() => p.ask("remove", a)} />}
              </RowMenu>
            );
            return (
              <TableRow key={a.name}>
                <TableHeader scope="row">
                  <AppLink href={hrefOf({ id: a.name })} go={() => p.open(a.name)} className="accounts-table__name">
                    {a.name}
                  </AppLink>
                  <span className="accounts-table__more app-narrow-only app-text-secondary app-text-small">
                    {t.messageCount(count, formatCount(count))} · {activity.text}
                  </span>
                </TableHeader>
                <TableCell className="app-wide-only">
                  <TypeTag account={a} />
                </TableCell>
                <TableCell>
                  <span className="accounts-table__tags">
                    {busy && (
                      <InlineLoading
                        className="inline-loading--auto"
                        status="active"
                        description={a.kind === "import" ? t.importing : t.syncing}
                        iconDescription={t.loading}
                        aria-live="polite"
                      />
                    )}
                    <StateTags account={a} smtpOn={data.smtp != null} />
                  </span>
                </TableCell>
                <TableCell className="app-wide-only app-tabular">{formatCount(count)}</TableCell>
                <TableCell className={activity.error ? "app-wide-only app-text-error" : "app-wide-only"}>{activity.text}</TableCell>
                <TableCell className="cds--table-column-menu">
                  <span className="accounts-table__actions">
                    {manage && a.kind === "imap" && (
                      <Button kind="ghost" size="sm" disabled={busy} onClick={() => void p.run(() => accountsApi.sync(a.name))}>
                        {t.syncNow}
                      </Button>
                    )}
                    {menu}
                  </span>
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </TableContainer>
  );
}

/** The removed accounts: their messages and the state of a complete deletion; admins delete them completely from the menu. */
function RemovedTable({ accounts, admin, menus, open, reload, lost }: ListProps & { accounts: Account[]; lost: () => void }) {
  const s = settingsText();
  return (
    <TableContainer className="accounts-table app-table">
      <Table aria-label={t.removedAccounts} size="lg" overflowMenuOnHover={false}>
        <TableHead>
          <TableRow>
            <TableHeader>{t.accountSection}</TableHeader>
            <TableHeader className="app-wide-only">{t.messages}</TableHeader>
            <TableHeader>{s.colStatus}</TableHeader>
            <TableHeader>
              <span className="cds--visually-hidden">{t.colActions}</span>
            </TableHeader>
          </TableRow>
        </TableHead>
        <TableBody>
          {accounts.map((a) => (
            <RemovedRow key={a.name} account={a} admin={admin} menus={menus} open={open} reload={reload} lost={lost} />
          ))}
        </TableBody>
      </Table>
    </TableContainer>
  );
}

/**
 * A removed account's row. A purge open elsewhere (another tab, the command
 * line) shows as progress, never as the dialog. After confirming, the row
 * stays with the progress and no menu item, so the focus goes to its name
 * link; when the row goes because the purge finished, lost hands the focus
 * on (#285).
 */
function RemovedRow({ account: a, admin, menus, open, reload, lost }: {
  account: Account;
  admin: boolean;
  menus: ReturnType<typeof useMenuButtons>;
  open: (name: string) => void;
  reload: () => void;
  lost: () => void;
}) {
  const [cancelled, setCancelled] = usePurgeCancelled(a);
  const [reviewing, setReviewing] = useState(false);
  const left = useRef<"confirmed" | "closed" | null>(null);
  const name = useRef<HTMLSpanElement>(null);
  const onLost = useRef(lost);
  useLayoutEffect(() => {
    onLost.current = lost;
  });
  useEffect(() => {
    if (a.purging) setReviewing(false);
  }, [a.purging]);
  useLayoutEffect(() => {
    if (reviewing || !left.current) return;
    (left.current === "confirmed" ? name.current?.querySelector("a") : menus.button(a.name))?.focus();
    left.current = null;
  }, [reviewing, menus, a.name]);
  // Checked while the row is still there: a layout effect's cleanup runs
  // before React removes the row from the document.
  useLayoutEffect(() => {
    const row = name.current?.closest("tr");
    return () => {
      if (row?.contains(document.activeElement)) onLost.current();
    };
  }, []);
  const count = messageCount(a);
  return (
    <TableRow>
      <TableHeader scope="row">
        <span ref={name}>
          <AppLink href={hrefOf({ id: a.name })} go={() => open(a.name)} className="accounts-table__name">
            {a.name}
          </AppLink>
        </span>
        <span className="accounts-table__more app-narrow-only app-text-secondary app-text-small">{t.messageCount(count, formatCount(count))}</span>
      </TableHeader>
      <TableCell className="app-wide-only app-tabular">{formatCount(count)}</TableCell>
      <TableCell>
        {a.purging && <PurgeProgress purge={a.purge} />}
        {!a.purging && cancelled && (
          <p role="status" className="app-text-warning">
            {t.purgeCancelled}
          </p>
        )}
      </TableCell>
      <TableCell className="cds--table-column-menu">
        {admin && !a.purging && (
          <RowMenu label={t.userActions(a.name)} register={menus.register(a.name)}>
            <MenuItem
              label={t.purgeStart}
              kind="danger"
              onClick={() => {
                setCancelled(false);
                setReviewing(true);
              }}
            />
          </RowMenu>
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
      </TableCell>
    </TableRow>
  );
}
