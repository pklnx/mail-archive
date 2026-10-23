import { Fragment, useCallback, useEffect, useId, useRef, useState, type FormEvent } from "react";
import {
  Button,
  Checkbox,
  CodeSnippet,
  FeatureFlags,
  MenuItem,
  MenuItemDivider,
  OverflowMenu,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableExpandedRow,
  TableExpandHeader,
  TableExpandRow,
  TableHead,
  TableHeader,
  TableRow,
  Tag,
  TextInput,
  Tile,
} from "@carbon/react";
import { Add } from "@carbon/icons-react";
import { ApiKeyItems } from "./ApiKeys";
import { ApiError, apiKeyApi, usersApi, type ApiKey, type GeneratedPassword, type ManagedUser } from "./api";
import { AppModal, DangerModal } from "./DangerModal";
import { relativeTime } from "./format";
import { t } from "./i18n";

/**
 * The text of a failed request. A user named by a legal hold gets a sentence
 * of its own with the holds' IDs (holdIds), not the server's text.
 */
const message = (err: unknown) => {
  if (err instanceof ApiError && err.body.code === "on_hold") {
    const ids = Array.isArray(err.body.holdIds) ? err.body.holdIds.filter((id): id is number => typeof id === "number") : [];
    return t.userOnHold(ids);
  }
  return t.failed(err instanceof Error ? err.message : String(err));
};

/** A confirmation that is open: what it does, and for whom. */
type Asking = { kind: "reset" | "lock" | "twoFactor" | "passkeys" | "remove"; user: ManagedUser } | null;

/** User management for admins: logins only, never other users' mail. */
export function UsersPage() {
  const [users, setUsers] = useState<ManagedUser[] | null>(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [generated, setGenerated] = useState<GeneratedPassword | null>(null);
  const [asking, setAsking] = useState<Asking>(null);
  // The users whose API keys are shown, and every user's keys.
  const [showKeys, setShowKeys] = useState<Set<string>>(new Set());
  const [keys, setKeys] = useState<ApiKey[]>([]);
  const id = useId();
  // The menu buttons by user, so the focus can go back to one after a
  // confirmation (its menu item is gone by then).
  const menus = useRef(new Map<string, HTMLElement>());
  const heading = useRef<HTMLHeadingElement>(null);

  const reload = useCallback(() => {
    const listed = usersApi.list().then(
      (u) => {
        setUsers(u);
        setError("");
      },
      (err: unknown) => setError(message(err)),
    );
    apiKeyApi.listAll().then(setKeys, () => setKeys([]));
    return listed;
  }, []);
  useEffect(() => {
    void reload();
  }, [reload]);

  // The error of a failed action is set after the reload, whose success
  // would clear it otherwise.
  const run = async (action: () => Promise<unknown>) => {
    setError("");
    let failure = "";
    try {
      await action();
    } catch (err) {
      failure = message(err);
    }
    await reload();
    if (failure) setError(failure);
  };

  const toggleKeys = (name: string) =>
    setShowKeys((cur) => {
      const next = new Set(cur);
      if (!next.delete(name)) next.add(name);
      return next;
    });

  const ask = (kind: NonNullable<Asking>["kind"], user: ManagedUser) => setAsking({ kind, user });
  const focusBack = () => (asking && menus.current.get(asking.user.name)?.querySelector<HTMLButtonElement>("button")) ?? heading.current;

  return (
    <div className="accounts-page">
      <div className="page page--wide">
        <header className="page-header">
          <div className="page-header__row">
            <h1 ref={heading} tabIndex={-1} className="page-header__title type-heading-04 users-page__heading">
              {t.usersTitle}
            </h1>
            <div className="page-header__actions">
              <Button kind="primary" renderIcon={Add} onClick={() => setAdding(true)}>
                {t.addUser}
              </Button>
            </div>
          </div>
          <p className="page-header__intro app-text-secondary">{t.adminsSeeNoMail}</p>
        </header>

        {generated && <GeneratedPanel value={generated} done={() => setGenerated(null)} />}
        {error && (
          <p role="alert" className="app-text-error">
            {error}
          </p>
        )}
        {!users && !error && <p className="app-text-secondary">{t.loading}</p>}

        {users && (
          // The OverflowMenu of Carbon v12 (on Menu): the older one adds two
          // tab stops named "Focus sentinel" to every open menu.
          <FeatureFlags enableV12Overflowmenu>
          <TableContainer className="users-table">
            <Table aria-label={t.usersTitle} size="lg" overflowMenuOnHover={false}>
              <TableHead>
                <TableRow>
                  <TableExpandHeader id={`${id}-expand`}>
                    <span className="cds--visually-hidden">{t.showApiKeys}</span>
                  </TableExpandHeader>
                  <TableHeader>{t.colUser}</TableHeader>
                  <TableHeader>{t.colRole}</TableHeader>
                  <TableHeader>{t.colState}</TableHeader>
                  <TableHeader>{t.colSignIn}</TableHeader>
                  <TableHeader>{t.colAccounts}</TableHeader>
                  <TableHeader>{t.colLastLogin}</TableHeader>
                  <TableHeader>
                    <span className="cds--visually-hidden">{t.colActions}</span>
                  </TableHeader>
                </TableRow>
              </TableHead>
              <TableBody>
                {users.map((u, i) => {
                  const own = keys.filter((k) => k.user === u.name);
                  const open = showKeys.has(u.name);
                  const cells = (
                    <>
                      <TableCell>
                        <span className="users-table__name">{u.name}</span>
                        {u.self && <span className="app-text-secondary"> ({t.you})</span>}
                        {u.self && <span className="users-table__hint app-text-secondary app-text-small">{t.selfHint}</span>}
                      </TableCell>
                      <TableCell>
                        <Tag size="sm" type={u.admin ? "purple" : "gray"}>
                          {u.admin ? t.roleAdmin : t.roleUser}
                        </Tag>
                      </TableCell>
                      <TableCell>
                        {u.locked ? (
                          <Tag size="sm" type="red">
                            {t.stateLocked}
                          </Tag>
                        ) : u.mustChangePassword ? (
                          <Tag size="sm" type="warm-gray">
                            {t.stateMustChange}
                          </Tag>
                        ) : (
                          <Tag size="sm" type="green">
                            {t.stateActive}
                          </Tag>
                        )}
                      </TableCell>
                      <TableCell>
                        <span className="users-table__lines">
                          <span>{u.twoFactorEnabled ? t.twoFactorShortOn : t.twoFactorShortOff}</span>
                          {u.passkeys > 0 && <span className="app-text-secondary">{t.passkeyCount(u.passkeys)}</span>}
                          {u.apiKeys > 0 && <span className="app-text-secondary">{t.apiKeyCount(u.apiKeys)}</span>}
                        </span>
                      </TableCell>
                      <TableCell>
                        <span className="users-table__lines">
                          <span className="app-tabular">{u.accounts}</span>
                          {u.failingAccounts > 0 && (
                            <span>
                              <Tag size="sm" type="red">
                                {t.failingCount(u.failingAccounts)}
                              </Tag>
                            </span>
                          )}
                        </span>
                      </TableCell>
                      <TableCell>{u.lastLoginAt ? relativeTime(u.lastLoginAt) : t.never}</TableCell>
                      <TableCell className="cds--table-column-menu">
                        {!u.self && (
                          <OverflowMenu
                            ref={(el: HTMLElement | null) => {
                              if (el) menus.current.set(u.name, el);
                              else menus.current.delete(u.name);
                            }}
                            label={t.userActions(u.name)}
                            size="lg"
                            menuAlignment="bottom-end"
                            tooltipAlignment="left"
                          >
                            <MenuItem label={t.resetPassword} onClick={() => ask("reset", u)} />
                            <MenuItem
                              label={u.locked ? t.unlock : t.lock}
                              onClick={() => (u.locked ? void run(() => usersApi.setLocked(u.name, false)) : ask("lock", u))}
                            />
                            <MenuItem label={u.admin ? t.removeAdmin : t.makeAdmin} onClick={() => void run(() => usersApi.setAdmin(u.name, !u.admin))} />
                            {u.twoFactorEnabled && <MenuItem label={t.twoFactorReset} onClick={() => ask("twoFactor", u)} />}
                            {u.passkeys > 0 && <MenuItem label={t.removePasskeys} onClick={() => ask("passkeys", u)} />}
                            <MenuItemDivider />
                            <MenuItem label={t.removeUser} kind="danger" onClick={() => ask("remove", u)} />
                          </OverflowMenu>
                        )}
                      </TableCell>
                    </>
                  );
                  if (own.length === 0) {
                    return (
                      <TableRow key={u.name}>
                        <TableCell headers={`${id}-expand`} />
                        {cells}
                      </TableRow>
                    );
                  }
                  return (
                    <Fragment key={u.name}>
                      <TableExpandRow
                        aria-label={t.apiKeysOf(u.name)}
                        // Names the key list only while it exists.
                        aria-controls={open ? `${id}-keys-${i}` : undefined}
                        expandHeader={`${id}-expand`}
                        expandIconDescription={open ? t.hideApiKeys : t.apiKeysOf(u.name)}
                        isExpanded={open}
                        onExpand={() => toggleKeys(u.name)}
                      >
                        {cells}
                      </TableExpandRow>
                      {open && (
                        <TableExpandedRow colSpan={8} id={`${id}-keys-${i}`}>
                          <ApiKeyItems keys={own} revoke={(k) => void run(() => apiKeyApi.revokeAny(k.id))} />
                        </TableExpandedRow>
                      )}
                    </Fragment>
                  );
                })}
              </TableBody>
            </Table>
          </TableContainer>
          </FeatureFlags>
        )}
      </div>

      {adding && (
        <AddUserModal
          cancel={() => setAdding(false)}
          added={(g) => {
            setAdding(false);
            setGenerated(g);
            reload();
          }}
        />
      )}
      {asking?.kind === "reset" && (
        <ResetPasswordModal
          user={asking.user}
          returnFocus={focusBack}
          cancel={() => setAsking(null)}
          confirm={(removePasskeys, revokeApiKeys) => {
            const name = asking.user.name;
            setAsking(null);
            void run(async () => setGenerated(await usersApi.resetPassword(name, removePasskeys, revokeApiKeys)));
          }}
        />
      )}
      {asking && asking.kind !== "reset" && (
        <ConfirmUser
          asking={asking}
          returnFocus={focusBack}
          cancel={() => setAsking(null)}
          confirm={() => {
            const { kind, user } = asking;
            setAsking(null);
            const name = user.name;
            if (kind === "lock") void run(() => usersApi.setLocked(name, true));
            else if (kind === "twoFactor") void run(() => usersApi.reset2FA(name));
            else if (kind === "passkeys") void run(() => usersApi.removePasskeys(name));
            else void run(() => usersApi.remove(name));
          }}
        />
      )}
    </div>
  );
}

/** The danger confirmations without options: lock, reset 2FA, remove passkeys, remove. */
function ConfirmUser({
  asking,
  returnFocus,
  cancel,
  confirm,
}: {
  asking: NonNullable<Asking>;
  returnFocus: () => HTMLElement | null | undefined;
  cancel: () => void;
  confirm: () => void;
}) {
  const { kind, user } = asking;
  const [heading, text, action] =
    kind === "lock"
      ? [t.lockTitle(user.name), t.lockText, t.lock]
      : kind === "twoFactor"
        ? [t.twoFactorResetTitle(user.name), t.twoFactorResetText, t.twoFactorReset]
        : kind === "passkeys"
          ? [t.removePasskeysTitle(user.name, user.passkeys), t.removePasskeysText, t.removePasskeys]
          : [t.removeUserTitle(user.name), t.removeUserText, t.removeUser];
  return (
    <DangerModal
      size="xs"
      modalHeading={heading}
      primaryButtonText={action}
      secondaryButtonText={t.cancel}
      returnFocus={returnFocus}
      onRequestClose={cancel}
      onRequestSubmit={confirm}
    >
      <p>{text}</p>
    </DangerModal>
  );
}

/**
 * Asks before a password reset. Passkeys are removed and API keys revoked
 * by default: after a compromise, a new password alone would not lock the
 * attacker out.
 */
export function ResetPasswordModal({
  user,
  returnFocus,
  cancel,
  confirm,
}: {
  user: ManagedUser;
  returnFocus?: () => HTMLElement | null | undefined;
  cancel: () => void;
  confirm: (removePasskeys: boolean, revokeApiKeys: boolean) => void;
}) {
  const [removePasskeys, setRemovePasskeys] = useState(true);
  const [revokeApiKeys, setRevokeApiKeys] = useState(true);
  const id = useId();
  return (
    <DangerModal
      size="sm"
      modalHeading={t.resetPasswordTitle(user.name)}
      primaryButtonText={t.confirmReset}
      secondaryButtonText={t.cancel}
      returnFocus={returnFocus}
      onRequestClose={cancel}
      onRequestSubmit={() => confirm(user.passkeys > 0 && removePasskeys, revokeApiKeys)}
    >
      <div className="modal-stack">
        <p>{t.resetPasswordText}</p>
        {(user.passkeys > 0 || user.apiKeys > 0) && (
          <div className="checkbox-list">
            {user.passkeys > 0 && (
              <Checkbox
                id={`${id}-passkeys`}
                labelText={t.resetAlsoPasskeys(user.passkeys)}
                checked={removePasskeys}
                onChange={(_, { checked }) => setRemovePasskeys(checked)}
              />
            )}
            {user.apiKeys > 0 && (
              <Checkbox
                id={`${id}-apikeys`}
                labelText={t.resetAlsoApiKeys(user.apiKeys)}
                checked={revokeApiKeys}
                onChange={(_, { checked }) => setRevokeApiKeys(checked)}
              />
            )}
          </div>
        )}
      </div>
    </DangerModal>
  );
}

/** Add user: a name and the admin choice; the server makes the password. */
function AddUserModal({ cancel, added }: { cancel: () => void; added: (g: GeneratedPassword) => void }) {
  const [name, setName] = useState("");
  const [admin, setAdmin] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [tried, setTried] = useState(false);
  const id = useId();
  const field = useRef<HTMLInputElement>(null);

  const submit = async (e?: FormEvent) => {
    e?.preventDefault();
    if (busy) return;
    setTried(true);
    if (!name.trim()) {
      field.current?.focus();
      return;
    }
    setBusy(true);
    setError("");
    try {
      added(await usersApi.create(name, admin));
    } catch (err) {
      setError(message(err));
      setBusy(false);
    }
  };

  return (
    <AppModal
      size="sm"
      modalHeading={t.addUser}
      primaryButtonText={t.addUser}
      secondaryButtonText={t.cancel}
      loadingStatus={busy ? "active" : "inactive"}
      loadingDescription={t.addUser}
      loadingIconDescription={t.loading}
      preventCloseOnClickOutside
      onRequestClose={() => {
        if (!busy) cancel();
      }}
      onRequestSubmit={() => void submit()}
    >
      <form className="modal-stack" onSubmit={(e) => void submit(e)} noValidate>
        <TextInput
          id={`${id}-name`}
          ref={field}
          data-modal-primary-focus=""
          labelText={t.userName}
          autoCapitalize="none"
          autoComplete="off"
          spellCheck={false}
          required
          invalid={tried && !name.trim()}
          invalidText={t.userNameRequired}
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <Checkbox id={`${id}-admin`} labelText={t.userIsAdmin} checked={admin} onChange={(_, { checked }) => setAdmin(checked)} />
        {error && (
          <p role="alert" className="app-text-error">
            {error}
          </p>
        )}
      </form>
    </AppModal>
  );
}

/** Shows a generated password once, with a copy button. */
export function GeneratedPanel({ value, done }: { value: GeneratedPassword; done: () => void }) {
  return (
    <Tile role="status" className="generated">
      <p className="generated__title">{t.generatedFor(value.name)}</p>
      <CodeSnippet
        type="single"
        className="generated__code"
        aria-label={t.carbon.copy}
        copyButtonDescription={t.carbon.copy}
        feedback={t.carbon.copied}
      >
        {value.password}
      </CodeSnippet>
      <p>{t.generatedHint}</p>
      <div>
        <Button kind="tertiary" size="md" onClick={done}>
          {t.done}
        </Button>
      </div>
    </Tile>
  );
}
