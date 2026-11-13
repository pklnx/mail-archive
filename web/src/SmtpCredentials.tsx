import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState, type FormEvent, type RefObject } from "react";
import {
  Button,
  CodeSnippet,
  InlineNotification,
  Link,
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
import { ApiError, profileApi, retryAfter } from "./api";
import { AppModal, DangerModal } from "./DangerModal";
import { relativeTime, shortDate } from "./format";
import { t } from "./i18n";
import { saslLine } from "./journalSetup";
import { ViewFallback } from "./lazyView";
import { FormError } from "./notices";
import { smtpApi, usable, type AdminSMTPCredential, type NewSMTPCredential, type SMTPCredential, type SMTPCredentialList } from "./smtpApi";
import { smtpText } from "./smtpText";

// SMTP secrets in the web UI (#395 part 3): the section on an smtp
// account's page (list, a new secret with the password, shown once, revoke)
// and the admins' list of every account's secrets on the Users page. Only
// the lazy Accounts and Users chunks import this module.

/** The grace choices for the current secret, in days (0 to 30, #395 decision 4). */
export const graceChoices = [0, 1, 7, 14, 30];
const defaultGrace = 7;

const failed = (err: unknown) => t.failed(err instanceof Error ? err.message : String(err));

/** The message for a refused new secret: the same answers as for a new API key. */
export function newSecretError(err: unknown): string {
  const wait = retryAfter(err);
  if (wait !== null) return t.tooManyAttempts(wait);
  if (err instanceof ApiError && err.status === 401) return err.message.includes("password") ? t.wrongCurrentPassword : t.invalidTwoFactor;
  return failed(err);
}

const stateTag: Record<SMTPCredential["state"], "green" | "warm-gray" | "gray" | "red"> = {
  active: "green",
  expiring: "warm-gray",
  expired: "gray",
  revoked: "red",
};

/** Until when a secret works: no end while it is the current one. */
function validUntil(c: SMTPCredential): string {
  if (c.state === "revoked") return shortDate(c.revokedAt);
  return c.expiresAt ? shortDate(c.expiresAt) : smtpText().noEnd;
}

const lastUsed = (c: SMTPCredential) => (c.lastUsedAt ? relativeTime(c.lastUsedAt) : t.apiKeyNeverUsed);

/** The secrets of an smtp account, loaded while enabled (admins only reach them). */
export function useSmtpCredentials(account: string, enabled: boolean) {
  const [list, setList] = useState<SMTPCredentialList | null>(null);
  const [error, setError] = useState("");
  const [version, setVersion] = useState(0);
  useEffect(() => {
    if (!enabled) return;
    const ctl = new AbortController();
    smtpApi.list(account, ctl.signal).then(
      (l) => {
        setList(l);
        setError("");
      },
      (err: unknown) => {
        if (!ctl.signal.aborted) setError(failed(err));
      },
    );
    return () => ctl.abort();
  }, [account, enabled, version]);
  const reload = useCallback(() => setVersion((v) => v + 1), []);
  return { list: enabled ? list : null, error: enabled ? error : "", reload };
}

export type SmtpCredentialsState = ReturnType<typeof useSmtpCredentials>;

/** Asks before a secret is revoked, in a danger Modal. */
function RevokeModal({ credential, cancel, confirm, returnFocus }: {
  credential: SMTPCredential;
  cancel: () => void;
  confirm: () => void;
  returnFocus: () => HTMLElement | null;
}) {
  const st = smtpText();
  return (
    <DangerModal
      size="xs"
      modalHeading={st.revokeTitle(credential.hint)}
      primaryButtonText={st.revoke}
      secondaryButtonText={t.cancel}
      returnFocus={returnFocus}
      onRequestClose={cancel}
      onRequestSubmit={confirm}
    >
      <p>{st.revokeText}</p>
    </DangerModal>
  );
}

/**
 * Revoking from a list: which secret is asked about, and the message after.
 * After a revoke the secret's button is gone, so the focus goes to the
 * section's heading.
 */
function useRevoke<C extends SMTPCredential>(revoke: (c: C) => Promise<unknown>, heading: RefObject<HTMLHeadingElement | null>) {
  const st = smtpText();
  const [asking, setAsking] = useState<C | null>(null);
  const [done, setDone] = useState("");
  const [error, setError] = useState("");
  const modal = asking && (
    <RevokeModal
      credential={asking}
      returnFocus={() => heading.current}
      cancel={() => setAsking(null)}
      confirm={() => {
        const c = asking;
        setAsking(null);
        setDone("");
        setError("");
        revoke(c).then(
          () => {
            setDone(st.revoked(c.hint));
            heading.current?.focus();
          },
          (err: unknown) => setError(failed(err)),
        );
      }}
    />
  );
  return { ask: setAsking, modal, done, error };
}

/** The Revoke button of a secret that still works. */
function RevokeButton({ credential, ask }: { credential: SMTPCredential; ask: () => void }) {
  const st = smtpText();
  if (!usable(credential)) return null;
  return (
    <Button kind="danger--ghost" size="sm" aria-label={st.revokeLabel(credential.hint)} onClick={ask}>
      {st.revoke}
    </Button>
  );
}

function StateTag({ credential }: { credential: SMTPCredential }) {
  return (
    <Tag size="sm" type={stateTag[credential.state]}>
      {smtpText().state(credential.state)}
    </Tag>
  );
}

/**
 * The SMTP secrets section of an smtp account's page, for its owner, who is
 * an admin: the user name, the secrets with their state, New secret and
 * Revoke. off: the server runs without logins (KEEPIA_ARCHIVE_SMTP_AUTH=off).
 */
export function SmtpCredentialsSection({ account, off, state }: { account: string; off: boolean; state: SmtpCredentialsState }) {
  const st = smtpText();
  const id = useId();
  const heading = useRef<HTMLHeadingElement>(null);
  const [creating, setCreating] = useState(false);
  const { list, error, reload } = state;
  const { ask, modal, done, error: revokeError } = useRevoke<SMTPCredential>(async (c) => {
    try {
      await smtpApi.revoke(account, c.id);
    } finally {
      reload();
    }
  }, heading);

  return (
    <section className="page__section" aria-labelledby={id}>
      <div className="app-section-head">
        <h2 id={id} ref={heading} tabIndex={-1} className="type-heading-03 app-page-title">
          {st.title}
        </h2>
        {list && (
          <Button kind="tertiary" size="sm" onClick={() => setCreating(true)}>
            {st.newSecret}
          </Button>
        )}
      </div>
      <p className="app-text-secondary">{st.intro}</p>
      {off && <p className="app-text-secondary">{st.authOff}</p>}
      {!list && (error ? <FormError text={error} /> : <ViewFallback inline />)}
      {list && (
        <>
          <dl className="app-fields">
            <div className="app-fields__row">
              <dt className="app-text-secondary">{st.username}</dt>
              <dd className="app-break-words">{list.username}</dd>
            </div>
          </dl>
          {list.credentials.length === 0 ? (
            <p>{st.none}</p>
          ) : (
            <TableContainer className="profile-table app-table">
              <Table aria-label={st.title} size="md">
                <TableHead>
                  <TableRow>
                    <TableHeader>{st.colSecret}</TableHeader>
                    <TableHeader>{t.colState}</TableHeader>
                    <TableHeader className="app-wide-only">{st.colCreated}</TableHeader>
                    <TableHeader className="app-wide-only">{st.colValidUntil}</TableHeader>
                    <TableHeader className="app-wide-only">{t.colLastUsed}</TableHeader>
                    <TableHeader>
                      <span className="cds--visually-hidden">{t.colActions}</span>
                    </TableHeader>
                  </TableRow>
                </TableHead>
                <TableBody>
                  {list.credentials.map((c) => (
                    <TableRow key={c.id} className={usable(c) ? undefined : "profile-table__row--off"}>
                      <TableCell>
                        <span className="profile-table__name">mas_…{c.hint}</span>
                      </TableCell>
                      <TableCell>
                        <StateTag credential={c} />
                      </TableCell>
                      <TableCell className="app-wide-only">{shortDate(c.createdAt)}</TableCell>
                      <TableCell className="app-wide-only">{validUntil(c)}</TableCell>
                      <TableCell className="app-wide-only">{lastUsed(c)}</TableCell>
                      <TableCell className="profile-table__action">
                        <RevokeButton credential={c} ask={() => ask(c)} />
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </TableContainer>
          )}
        </>
      )}
      {done && (
        <p role="status" className="app-text-secondary">
          {done}
        </p>
      )}
      <FormError text={revokeError} />
      {modal}
      {creating && list && (
        <NewSecretModal
          account={account}
          current={list.credentials.some((c) => c.state === "active")}
          created={reload}
          close={() => setCreating(false)}
        />
      )}
    </section>
  );
}

/**
 * New secret, in a Modal: how long the current secret stays valid, the
 * password and, with 2FA on, a code. The answer replaces the form: the
 * user name, the secret with a copy button and the password map line. The
 * secret lives only in this Modal's state: closing it drops the secret,
 * and nothing keeps it (no storage, no URL, no log).
 */
export function NewSecretModal({ account, current, created, close }: {
  account: string;
  /** The account has a current secret: the grace choice applies to it. */
  current: boolean;
  /** A secret was created: the list reloads behind the Modal. */
  created: () => void;
  close: () => void;
}) {
  const st = smtpText();
  const id = useId();
  const [grace, setGrace] = useState(defaultGrace);
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [twoFactor, setTwoFactor] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<NewSMTPCredential | null>(null);
  const done = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let live = true;
    profileApi.twoFactor.get().then(
      (s) => live && setTwoFactor(s.enabled),
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, []);
  // The answer replaces the form, whose fields had the focus.
  useLayoutEffect(() => {
    if (result) done.current?.focus();
  }, [result]);

  const leave = () => {
    if (busy) return;
    setResult(null);
    close();
  };

  const submit = async (e?: FormEvent) => {
    e?.preventDefault();
    if (busy) return;
    if (result) {
      leave();
      return;
    }
    setBusy(true);
    setError("");
    try {
      setResult(await smtpApi.create(account, { currentPassword: password, code, graceDays: current ? grace : defaultGrace }));
      created();
    } catch (err) {
      setError(newSecretError(err));
    } finally {
      setPassword("");
      setCode("");
      setBusy(false);
    }
  };

  return (
    <AppModal
      size="sm"
      modalHeading={st.newSecretTitle(account)}
      primaryButtonText={result ? t.done : st.create}
      secondaryButtonText={result ? undefined : t.cancel}
      loadingStatus={busy ? "active" : "inactive"}
      loadingDescription={st.create}
      loadingIconDescription={t.loading}
      preventCloseOnClickOutside
      shouldSubmitOnEnter
      onRequestClose={leave}
      onRequestSubmit={() => void submit()}
    >
      {result ? (
        <div ref={done} tabIndex={-1} role="status" className="modal-stack">
          <p className="generated__title">{st.created}</p>
          <SecretField label={st.username} value={result.username} />
          <SecretField label={st.secret} value={result.secret} />
          <SecretField label={st.mapLine} value={saslLine(result.username, result.secret)} />
          {result.previous.map((p) => (
            <p key={p.id} className="app-text-secondary">
              {p.expiresAt ? st.previousExpires(shortDate(p.expiresAt)) : st.previousRevoked}
            </p>
          ))}
        </div>
      ) : (
        <form className="modal-stack" onSubmit={(e) => void submit(e)} noValidate>
          {current && (
            <Select
              id={`${id}-grace`}
              labelText={st.grace}
              helperText={st.graceHelp}
              value={grace}
              onChange={(e) => setGrace(Number(e.target.value))}
            >
              {graceChoices.map((d) => (
                <SelectItem key={d} value={d} text={st.graceDays(d)} />
              ))}
            </Select>
          )}
          <p className="app-text-secondary">{twoFactor ? t.apiKeyConfirm2FA : t.apiKeyConfirm}</p>
          <PasswordInput
            id={`${id}-password`}
            data-modal-primary-focus=""
            labelText={t.currentPassword}
            showPasswordLabel={t.carbon.showPassword}
            hidePasswordLabel={t.carbon.hidePassword}
            required
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
          {twoFactor && (
            <TextInput
              id={`${id}-code`}
              labelText={t.twoFactorCode}
              required
              autoComplete="one-time-code"
              autoCapitalize="none"
              spellCheck={false}
              value={code}
              onChange={(e) => setCode(e.target.value)}
            />
          )}
          <FormError text={error} />
        </form>
      )}
    </AppModal>
  );
}

/** A labelled value with Carbon's copy button. */
function SecretField({ label, value }: { label: string; value: string }) {
  const id = useId();
  return (
    <div>
      <p id={id} className="cds--label">
        {label}
      </p>
      <CodeSnippet
        type="single"
        className="generated__code"
        aria-labelledby={id}
        copyText={value}
        copyButtonDescription={t.carbon.copy}
        feedback={t.carbon.copied}
      >
        {value}
      </CodeSnippet>
    </div>
  );
}

/**
 * The admins' list of every smtp account's secrets on the Users page
 * (#395 Q2), with Revoke: a locked owner's secrets keep working until an
 * admin revokes them. New secrets come from the account's owner.
 */
export function SmtpCredentialsAdmin({ credentials, heading, revoke }: {
  credentials: AdminSMTPCredential[];
  heading: RefObject<HTMLHeadingElement | null>;
  revoke: (c: AdminSMTPCredential) => Promise<unknown>;
}) {
  const st = smtpText();
  const id = useId();
  const { ask, modal, done, error } = useRevoke(revoke, heading);
  return (
    <section className="page__section" aria-labelledby={id}>
      <h2 id={id} ref={heading} tabIndex={-1} className="type-heading-03 app-page-title">
        {st.title}
      </h2>
      <p className="app-text-secondary">{st.adminIntro}</p>
      <TableContainer className="app-table">
        <Table aria-label={st.title} size="md">
          <TableHead>
            <TableRow>
              <TableHeader>{st.colAccount}</TableHeader>
              <TableHeader className="app-wide-only">{st.colOwner}</TableHeader>
              <TableHeader className="app-wide-only">{st.username}</TableHeader>
              <TableHeader>{st.colSecret}</TableHeader>
              <TableHeader>{t.colState}</TableHeader>
              <TableHeader className="app-wide-only">{st.colValidUntil}</TableHeader>
              <TableHeader className="app-wide-only">{t.colLastUsed}</TableHeader>
              <TableHeader>
                <span className="cds--visually-hidden">{t.colActions}</span>
              </TableHeader>
            </TableRow>
          </TableHead>
          <TableBody>
            {credentials.map((c) => (
              <TableRow key={c.id} className={usable(c) ? undefined : "profile-table__row--off"}>
                <TableHeader scope="row">
                  {c.account}
                  {c.accountRemoved && <span className="app-text-secondary"> ({st.accountRemoved})</span>}
                  <span className="app-narrow-only app-text-secondary app-text-small">
                    {" "}
                    {c.owner || st.noOwner} · {c.username}
                  </span>
                </TableHeader>
                <TableCell className="app-wide-only">{c.owner || st.noOwner}</TableCell>
                <TableCell className="app-wide-only">{c.username}</TableCell>
                <TableCell>mas_…{c.hint}</TableCell>
                <TableCell>
                  <StateTag credential={c} />
                </TableCell>
                <TableCell className="app-wide-only">{validUntil(c)}</TableCell>
                <TableCell className="app-wide-only">{lastUsed(c)}</TableCell>
                <TableCell className="profile-table__action">
                  <RevokeButton credential={c} ask={() => ask(c)} />
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableContainer>
      {done && (
        <p role="status" className="app-text-secondary">
          {done}
        </p>
      )}
      <FormError text={error} />
      {modal}
    </section>
  );
}

/**
 * After a lock: the locked user's smtp accounts still have usable secrets
 * (#395 decision 9), with a link to the admins' list to revoke them.
 */
export function LockHint({ user, count, href, open }: { user: string; count: number; href: string; open: () => void }) {
  const st = smtpText();
  return (
    // A notification takes no interactive content: the link follows it.
    <div className="notice-with-link">
      <InlineNotification
        kind="warning"
        lowContrast
        hideCloseButton
        role="status"
        className="notification--full"
        statusIconDescription={t.carbon.status.warning}
        subtitle={st.lockHint(user, count)}
      />
      <Link
        href={href}
        onClick={(e) => {
          e.preventDefault();
          open();
        }}
      >
        {st.lockHintLink}
      </Link>
    </div>
  );
}
