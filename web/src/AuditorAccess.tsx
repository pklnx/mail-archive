import { useCallback, useEffect, useId, useRef, useState, type FormEvent } from "react";
import {
  Button,
  Checkbox,
  MenuItem,
  MenuItemDivider,
  PasswordInput,
  Select,
  SelectItem,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableHeader,
  TableRow,
  Tag,
  TextInput,
} from "@carbon/react";
import { Add } from "@carbon/icons-react";
import { ApiError, retryAfter, type AccountsResponse } from "./api";
import { auditorApi, type Grant, type GrantInput } from "./auditorApi";
import { auditorText, lastDay } from "./auditorText";
import { DangerModal, AppModal } from "./DangerModal";
import { DateRange } from "./DateRange";
import { dateOfDay, day, dayOfDate, relativeTime, shiftDay } from "./format";
import { t } from "./i18n";
import { FormError, Success } from "./notices";
import { RowMenu, useMenuButtons } from "./settingsParts";
import { TableSkeleton } from "./tableSkeleton";

// The owner's Auditor access page (#393 part 3): the grants with their
// state and the auditor's last access, Grant access, Extend and Revoke. The
// server decides every rule (group, accounts, 365 days, step-up); this page
// only sends the owner's choice and shows the answer.

/** The longest window the server allows, in days (store.MaxGrantDays). */
const maxDays = 365;
/** The default window: 28 days (decision 6). */
const defaultDays = 28;

const stateTag: Record<Grant["state"], "green" | "blue" | "gray" | "red" | "warm-gray"> = {
  active: "green",
  scheduled: "blue",
  expired: "gray",
  revoked: "red",
  suspended: "warm-gray",
};

/** The first instant of a local day ("YYYY-MM-DD") as RFC 3339 (decision 5: the owner's time zone). */
const startOf = (ymd: string) => dateOfDay(ymd).toISOString();

/** The message of a failed grant request, in words for the refusals the owner can act on. */
export function grantError(err: unknown): string {
  const a = auditorText();
  const wait = retryAfter(err);
  if (wait !== null) return t.tooManyAttempts(wait);
  if (err instanceof ApiError) {
    if (err.status === 401) return err.message.includes("password") ? t.wrongCurrentPassword : t.invalidTwoFactor;
    if (err.status === 404 && err.message.includes("auditor")) return a.noAuditor;
    if (err.status === 404 && err.message.includes("account")) return a.noAccount;
    if (err.status === 409 && err.message.includes("revoked")) return a.revokedAlready;
    if (err.status === 409 && err.message.includes("extended")) return a.cannotExtend;
    if (err.status === 409) return a.smtpAccount;
    if (err.status === 400 && err.message.includes("at most")) return a.tooLong;
  }
  return t.failed(err instanceof Error ? err.message : String(err));
}

/** The accounts of a grant in words: "info (all folders)", "team: INBOX, Sent". */
export function accountsText(g: Grant): string {
  const a = auditorText();
  return g.accounts.map((x) => (x.folders === null ? `${x.name} (${a.allFolders})` : `${x.name}: ${x.folders.join(", ")}`)).join("; ");
}

/** The message period in words: "1/1/2021 – 12/31/2023", or all dates. */
export function periodRange(g: Grant): string {
  const a = auditorText();
  if (!g.periodFrom && !g.periodUntil) return a.allDates;
  return a.validRange(g.periodFrom ? day(g.periodFrom) : "…", g.periodUntil ? lastDay(g.periodUntil) : "…");
}

type Activity = Record<number, string | null | "failed">;

interface Props {
  /** The owner has 2FA on: Grant access and Extend ask for a code. */
  twoFactor: boolean;
  /** The owner's accounts, to choose from. */
  accounts: AccountsResponse | null;
}

export function AuditorAccess({ twoFactor, accounts }: Props) {
  const a = auditorText();
  const [grants, setGrants] = useState<Grant[] | null>(null);
  const [activity, setActivity] = useState<Activity>({});
  const [error, setError] = useState("");
  const [done, setDone] = useState("");
  const [adding, setAdding] = useState(false);
  const [extending, setExtending] = useState<Grant | null>(null);
  const [revoking, setRevoking] = useState<Grant | null>(null);
  const menus = useMenuButtons();
  const heading = useRef<HTMLHeadingElement>(null);

  const reload = useCallback(async () => {
    try {
      const list = await auditorApi.grants();
      setGrants(list);
      setError("");
      // The last access per grant, from the owner's Log (Q7); one request each.
      for (const g of list) {
        auditorApi.lastActivity(g.id).then(
          (at) => setActivity((m) => ({ ...m, [g.id]: at })),
          () => setActivity((m) => ({ ...m, [g.id]: "failed" })),
        );
      }
    } catch (err) {
      setError(t.failed(err instanceof Error ? err.message : String(err)));
    }
  }, []);
  useEffect(() => {
    void reload();
  }, [reload]);

  const revoke = async (g: Grant) => {
    setError("");
    setDone("");
    try {
      await auditorApi.revoke(g.id);
      setDone(a.revoked(g.label));
    } catch (err) {
      setError(grantError(err));
    }
    await reload();
    heading.current?.focus();
  };

  const last = (g: Grant) => {
    const at = activity[g.id];
    if (at === undefined) return "";
    if (at === "failed") return a.activityFailed;
    return at === null ? a.neverAccessed : relativeTime(at);
  };

  return (
    <div className="accounts-page">
      <div className="page page--wide">
        <header className="page-header">
          <div className="page-header__row">
            <h1 ref={heading} tabIndex={-1} className="page-header__title type-heading-04 app-page-title">
              {a.title}
            </h1>
            <div className="page-header__actions">
              <Button kind="primary" renderIcon={Add} onClick={() => setAdding(true)}>
                {a.grant}
              </Button>
            </div>
          </div>
          <p className="page-header__intro app-text-secondary">{a.intro}</p>
          <p className="page-header__intro app-text-secondary">{a.holdHint}</p>
        </header>
        {done && <Success text={done} />}
        <FormError text={error} />
        {!grants && !error && (
          <TableSkeleton
            columns={[
              { header: a.colCase },
              { header: a.colAuditor },
              { header: a.colAccounts, wide: true },
              { header: a.colValid, wide: true },
              { header: a.colState },
              { header: a.colLastActivity, wide: true },
              { header: "" },
            ]}
          />
        )}
        {grants && grants.length === 0 && <p className="app-text-secondary">{a.none}</p>}
        {grants && grants.length > 0 && (
          <TableContainer className="users-table app-table">
            <Table aria-label={a.title} size="lg" overflowMenuOnHover={false}>
              <TableHead>
                <TableRow>
                  <TableHeader>{a.colCase}</TableHeader>
                  <TableHeader>{a.colAuditor}</TableHeader>
                  <TableHeader className="app-wide-only">{a.colAccounts}</TableHeader>
                  <TableHeader className="app-wide-only">{a.colMessages}</TableHeader>
                  <TableHeader className="app-wide-only">{a.colValid}</TableHeader>
                  <TableHeader>{a.colState}</TableHeader>
                  <TableHeader className="app-wide-only">{a.colLastActivity}</TableHeader>
                  <TableHeader>
                    <span className="cds--visually-hidden">{t.colActions}</span>
                  </TableHeader>
                </TableRow>
              </TableHead>
              <TableBody>
                {grants.map((g) => (
                  <TableRow key={g.id}>
                    <TableHeader scope="row">
                      <span className="users-table__lines">
                        <span className="users-table__name">{g.label}</span>
                        {g.state === "revoked" && <span className="app-text-secondary app-text-small">{a.reason(g.revokeReason)}</span>}
                        {g.state === "suspended" && <span className="app-text-secondary app-text-small">{a.suspendedHint}</span>}
                        <span className="users-table__more app-narrow-only app-text-secondary app-text-small">
                          <span>{accountsText(g)}</span>
                          <span>{a.validRange(day(g.startsAt), lastDay(g.endsAt))}</span>
                        </span>
                      </span>
                    </TableHeader>
                    <TableCell>{g.auditor ?? <span className="app-text-secondary">{a.auditorGone(g.revokeReason)}</span>}</TableCell>
                    <TableCell className="app-wide-only">
                      <span className="app-break-words">{accountsText(g)}</span>
                    </TableCell>
                    <TableCell className="app-wide-only">{periodRange(g)}</TableCell>
                    <TableCell className="app-wide-only">{a.validRange(day(g.startsAt), lastDay(g.endsAt))}</TableCell>
                    <TableCell>
                      <Tag size="sm" type={stateTag[g.state] ?? "gray"}>
                        {a.state(g.state)}
                      </Tag>
                    </TableCell>
                    <TableCell className="app-wide-only">{last(g)}</TableCell>
                    <TableCell className="cds--table-column-menu">
                      {g.state !== "revoked" && (
                        <RowMenu label={a.actions(g.label)} register={menus.register(String(g.id))}>
                          <MenuItem label={a.extend} onClick={() => setExtending(g)} />
                          <MenuItemDivider />
                          <MenuItem label={a.revoke} kind="danger" onClick={() => setRevoking(g)} />
                        </RowMenu>
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </TableContainer>
        )}
      </div>

      {adding && (
        <GrantModal
          twoFactor={twoFactor}
          accounts={accounts}
          cancel={() => setAdding(false)}
          created={(g) => {
            setAdding(false);
            setError("");
            setDone(a.created(g.label, g.auditor ?? ""));
            void reload();
          }}
        />
      )}
      {extending && (
        <ExtendModal
          grant={extending}
          twoFactor={twoFactor}
          returnFocus={() => menus.button(String(extending.id)) ?? heading.current}
          cancel={() => setExtending(null)}
          extended={(g) => {
            setExtending(null);
            setError("");
            setDone(a.extended(g.label, lastDay(g.endsAt)));
            void reload();
          }}
        />
      )}
      {revoking && (
        <DangerModal
          size="xs"
          modalHeading={a.revokeTitle(revoking.label)}
          primaryButtonText={a.revoke}
          secondaryButtonText={t.cancel}
          returnFocus={() => heading.current}
          onRequestClose={() => setRevoking(null)}
          onRequestSubmit={() => {
            const g = revoking;
            setRevoking(null);
            void revoke(g);
          }}
        >
          <p>{a.revokeText(revoking.auditor ?? a.colAuditor)}</p>
        </DangerModal>
      )}
    </div>
  );
}

/** The password and, with 2FA, the code: the step-up of Grant access and Extend. */
function StepUp({ id, twoFactor, password, code, setPassword, setCode }: {
  id: string;
  twoFactor: boolean;
  password: string;
  code: string;
  setPassword: (v: string) => void;
  setCode: (v: string) => void;
}) {
  const a = auditorText();
  return (
    <fieldset className="grant-section">
      <legend className="cds--label">{a.stepUp}</legend>
      <PasswordInput
        id={`${id}-password`}
        labelText={a.currentPassword}
        showPasswordLabel={t.carbon.showPassword}
        hidePasswordLabel={t.carbon.hidePassword}
        autoComplete="current-password"
        required
        value={password}
        onChange={(e) => setPassword(e.target.value)}
      />
      {twoFactor && (
        <TextInput
          id={`${id}-code`}
          labelText={a.code}
          autoComplete="one-time-code"
          autoCapitalize="none"
          spellCheck={false}
          required
          value={code}
          onChange={(e) => setCode(e.target.value)}
        />
      )}
    </fieldset>
  );
}

/** One account's choice in the grant dialog: not granted, all folders (null) or the named ones. */
type Choice = Record<string, string[] | null>;

/**
 * Grant access: the auditor's exact login name, the case, the accounts with
 * all or some folders, the optional message period and the validity
 * window, confirmed with the owner's password (and code).
 */
function GrantModal({
  twoFactor,
  accounts,
  cancel,
  created,
}: {
  twoFactor: boolean;
  accounts: AccountsResponse | null;
  cancel: () => void;
  created: (g: Grant) => void;
}) {
  const a = auditorText();
  const id = useId();
  const today = dayOfDate(new Date());
  // Journal accounts cannot be granted (M1); removed ones read nothing new.
  const own = (accounts?.accounts ?? []).filter((x) => x.kind !== "smtp" && !x.removed);
  const [auditor, setAuditor] = useState("");
  const [label, setLabel] = useState("");
  const [chosen, setChosen] = useState<Choice>({});
  const [period, setPeriod] = useState({ start: "", end: "" });
  const [valid, setValid] = useState({ start: today, end: shiftDay(today, defaultDays - 1) });
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const auditorField = useRef<HTMLInputElement>(null);

  const names = Object.keys(chosen);
  const folderless = names.filter((n) => chosen[n]?.length === 0);
  const toggleAccount = (name: string, on: boolean) =>
    setChosen((c) => {
      const next = { ...c };
      if (on) next[name] = null;
      else delete next[name];
      return next;
    });
  const toggleFolder = (account: string, folder: string, on: boolean) =>
    setChosen((c) => {
      const list = c[account] ?? [];
      return { ...c, [account]: on ? [...list, folder] : list.filter((f) => f !== folder) };
    });

  const submit = async (e?: FormEvent) => {
    e?.preventDefault();
    if (busy) return;
    setTried(true);
    if (!auditor.trim() || !label.trim() || names.length === 0 || folderless.length > 0 || !valid.end) {
      auditorField.current?.focus();
      return;
    }
    const input: GrantInput = {
      auditor: auditor.trim(),
      label: label.trim(),
      // In the order of the account list.
      accounts: own.filter((x) => x.name in chosen).map((x) => ({ name: x.name, folders: chosen[x.name] ?? null })),
      periodFrom: period.start ? startOf(period.start) : null,
      // The server's ends are exclusive: the start of the day after the last one.
      periodUntil: period.end ? startOf(shiftDay(period.end, 1)) : null,
      // A start today or earlier is now.
      startsAt: valid.start && valid.start > today ? startOf(valid.start) : null,
      endsAt: startOf(shiftDay(valid.end, 1)),
      currentPassword: password,
      code,
    };
    setBusy(true);
    setError("");
    try {
      created(await auditorApi.create(input));
    } catch (err) {
      setError(grantError(err));
      setPassword("");
      setCode("");
      setBusy(false);
    }
  };

  return (
    <AppModal
      size="md"
      modalHeading={a.grant}
      primaryButtonText={a.grant}
      secondaryButtonText={t.cancel}
      loadingStatus={busy ? "active" : "inactive"}
      loadingDescription={a.grant}
      loadingIconDescription={t.loading}
      preventCloseOnClickOutside
      onRequestClose={() => {
        if (!busy) cancel();
      }}
      onRequestSubmit={() => void submit()}
    >
      <form className="modal-stack" onSubmit={(e) => void submit(e)} noValidate>
        <TextInput
          id={`${id}-auditor`}
          ref={auditorField}
          data-modal-primary-focus=""
          labelText={a.auditorName}
          helperText={a.auditorNameHelp}
          autoCapitalize="none"
          autoComplete="off"
          spellCheck={false}
          required
          invalid={tried && !auditor.trim()}
          invalidText={a.auditorRequired}
          value={auditor}
          onChange={(e) => setAuditor(e.target.value)}
        />
        <TextInput
          id={`${id}-label`}
          labelText={a.label}
          helperText={a.labelHelp}
          autoComplete="off"
          required
          maxLength={200}
          invalid={tried && !label.trim()}
          invalidText={a.labelRequired}
          value={label}
          onChange={(e) => setLabel(e.target.value)}
        />
        <fieldset className="grant-section">
          <legend className="cds--label">{a.accounts}</legend>
          {own.length === 0 && <p className="app-text-secondary">{a.noAccounts}</p>}
          {own.map((x) => {
            const choice = chosen[x.name];
            const on = choice !== undefined;
            return (
              <div key={x.name} className="checkbox-list">
                <Checkbox id={`${id}-account-${x.name}`} labelText={x.name} checked={on} onChange={(_, { checked }) => toggleAccount(x.name, checked)} />
                {on && (
                  <div className="grant-folders">
                    <Checkbox
                      id={`${id}-all-${x.name}`}
                      labelText={a.allFoldersLater}
                      checked={choice === null}
                      onChange={(_, { checked }) => setChosen((c) => ({ ...c, [x.name]: checked ? null : [] }))}
                    />
                    {choice !== null && (
                      <fieldset className="checkbox-list">
                        <legend className="cds--visually-hidden">{a.folders(x.name)}</legend>
                        {x.folders.map((f) => (
                          <Checkbox
                            key={f.name}
                            id={`${id}-folder-${x.name}-${f.name}`}
                            labelText={f.name}
                            checked={choice.includes(f.name)}
                            onChange={(_, { checked }) => toggleFolder(x.name, f.name, checked)}
                          />
                        ))}
                        {tried && choice.length === 0 && <p className="app-text-error">{a.foldersRequired}</p>}
                      </fieldset>
                    )}
                  </div>
                )}
              </div>
            );
          })}
          {tried && names.length === 0 && own.length > 0 && <p className="app-text-error">{a.accountsRequired}</p>}
        </fieldset>
        <div className="grant-section">
          <DateRange
            ids={{ start: `${id}-period-from`, end: `${id}-period-until` }}
            labels={{ start: a.periodFrom, end: a.periodUntil }}
            start={period.start}
            end={period.end}
            onChange={(start, end) => setPeriod({ start, end })}
          />
          <p className="app-text-secondary app-text-small">{a.periodHelp}</p>
        </div>
        <div className="grant-section">
          <DateRange
            ids={{ start: `${id}-valid-from`, end: `${id}-valid-until` }}
            labels={{ start: a.validFrom, end: a.validUntil }}
            start={valid.start}
            end={valid.end}
            onChange={(start, end) => setValid({ start, end })}
          />
          <p className={tried && !valid.end ? "app-text-error" : "app-text-secondary app-text-small"}>
            {tried && !valid.end ? a.validRequired : a.validHelp}
          </p>
        </div>
        <StepUp id={id} twoFactor={twoFactor} password={password} code={code} setPassword={setPassword} setCode={setCode} />
        <FormError text={error} />
      </form>
    </AppModal>
  );
}

/**
 * The last days an extension offers: four weeks, three and six months after
 * the current end, and the latest the server allows (365 days from today,
 * decision 6); only days after the current end.
 */
export function extensionDays(endsAt: string, now = new Date()): string[] {
  const today = dayOfDate(now);
  const current = dayOfDate(new Date(Date.parse(endsAt) - 1));
  const base = current > today ? current : today;
  const latest = shiftDay(today, maxDays - 1);
  const days = [shiftDay(base, 28), shiftDay(base, 91), shiftDay(base, 182), latest].map((d) => (d > latest ? latest : d));
  return [...new Set(days)].filter((d) => d > current).sort();
}

/** Extend: a later last day, confirmed with the owner's password (and code). */
function ExtendModal({
  grant,
  twoFactor,
  returnFocus,
  cancel,
  extended,
}: {
  grant: Grant;
  twoFactor: boolean;
  returnFocus: () => HTMLElement | null | undefined;
  cancel: () => void;
  extended: (g: Grant) => void;
}) {
  const a = auditorText();
  const id = useId();
  const days = extensionDays(grant.endsAt);
  const [chosen, setChosen] = useState(days[0] ?? "");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const submit = async (e?: FormEvent) => {
    e?.preventDefault();
    if (busy || !chosen) return;
    setBusy(true);
    setError("");
    try {
      extended(await auditorApi.extend(grant.id, startOf(shiftDay(chosen, 1)), password, code));
    } catch (err) {
      setError(grantError(err));
      setPassword("");
      setCode("");
      setBusy(false);
    }
  };

  return (
    <AppModal
      size="sm"
      modalHeading={a.extendTitle(grant.label)}
      primaryButtonText={a.extend}
      secondaryButtonText={t.cancel}
      primaryButtonDisabled={!chosen}
      loadingStatus={busy ? "active" : "inactive"}
      loadingDescription={a.extend}
      loadingIconDescription={t.loading}
      preventCloseOnClickOutside
      returnFocus={returnFocus}
      onRequestClose={() => {
        if (!busy) cancel();
      }}
      onRequestSubmit={() => void submit()}
    >
      <form className="modal-stack" onSubmit={(e) => void submit(e)} noValidate>
        <p>{a.extendText(lastDay(grant.endsAt))}</p>
        <Select id={`${id}-day`} data-modal-primary-focus="" labelText={a.newLastDay} value={chosen} onChange={(e) => setChosen(e.target.value)}>
          {days.map((d) => (
            <SelectItem key={d} value={d} text={day(startOf(d))} />
          ))}
        </Select>
        <StepUp id={id} twoFactor={twoFactor} password={password} code={code} setPassword={setPassword} setCode={setCode} />
        <FormError text={error} />
      </form>
    </AppModal>
  );
}
