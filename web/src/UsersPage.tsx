import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState, type FormEvent, type ReactNode, type RefObject } from "react";
import {
  Button,
  Checkbox,
  CodeSnippet,
  MenuItem,
  MenuItemDivider,
  Table,
  TableBody,
  TableCell,
  TableContainer,
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
import { relativeTime, shortDate } from "./format";
import { t } from "./i18n";
import { AppLink, DetailHeader, RowMenu, SummaryTiles, useMenuButtons, usePageFocus } from "./settingsParts";
import { settingsText } from "./settingsText";
import { formatState, parseState, useViewState } from "./urlState";

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

/** The URL of a user's detail page, or of the list. */
const hrefOf = (id = "") => formatState({ ...parseState(""), view: "users", id });

const RoleTag = ({ user }: { user: ManagedUser }) => (
  <Tag size="sm" type={user.admin ? "purple" : "gray"}>
    {user.admin ? t.roleAdmin : t.roleUser}
  </Tag>
);

const StateTag = ({ user }: { user: ManagedUser }) =>
  user.locked ? (
    <Tag size="sm" type="red">
      {t.stateLocked}
    </Tag>
  ) : user.mustChangePassword ? (
    <Tag size="sm" type="warm-gray">
      {t.stateMustChange}
    </Tag>
  ) : (
    <Tag size="sm" type="green">
      {t.stateActive}
    </Tag>
  );

const lastLogin = (u: ManagedUser) => (u.lastLoginAt ? relativeTime(u.lastLoginAt) : t.never);

/**
 * User management for admins: logins only, never other users' mail. The
 * list is a table; each user has a detail page (?view=users&id=NAME) with
 * the same actions and the user's API keys.
 */
export function UsersPage() {
  const [state, update] = useViewState();
  const [users, setUsers] = useState<ManagedUser[] | null>(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [generated, setGenerated] = useState<GeneratedPassword | null>(null);
  const [asking, setAsking] = useState<Asking>(null);
  const [keys, setKeys] = useState<ApiKey[]>([]);
  const menus = useMenuButtons();
  const heading = useRef<HTMLHeadingElement>(null);

  const reload = useCallback(async () => {
    const [listed] = await Promise.all([
      usersApi.list().then(
        (u) => {
          setUsers(u);
          setError("");
        },
        (err: unknown) => setError(message(err)),
      ),
      apiKeyApi.listAll().then(setKeys, () => setKeys([])),
    ]);
    return listed;
  }, []);
  useEffect(() => {
    void reload();
  }, [reload]);

  // The error of a failed action is set after the reload, whose success
  // would clear it otherwise. Resolves to whether the action succeeded.
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
    return !failure;
  };

  const detail = users && state.id ? users.find((u) => u.name === state.id) : undefined;
  // A user that does not exist (any more) returns to the list.
  const unknown = users !== null && state.id !== "" && !detail;
  useEffect(() => {
    if (unknown) update({ id: "" }, { replace: true });
  }, [unknown, update]);
  usePageFocus(users === null ? null : detail ? `user:${detail.name}` : "list", heading);

  const ask = (kind: NonNullable<Asking>["kind"], user: ManagedUser) => setAsking({ kind, user });
  const focusBack = () => (asking && menus.button(asking.user.name)) ?? heading.current;
  const open = (name: string) => update({ id: name });
  const toList = (replace = false) => update({ id: "" }, { replace });

  /** The actions of the menu, for a row and for the detail page. */
  const items = (u: ManagedUser, withReset: boolean) => (
    <>
      {withReset && <MenuItem label={t.resetPassword} onClick={() => ask("reset", u)} />}
      <MenuItem label={u.locked ? t.unlock : t.lock} onClick={() => (u.locked ? void run(() => usersApi.setLocked(u.name, false)) : ask("lock", u))} />
      <MenuItem label={u.admin ? t.removeAdmin : t.makeAdmin} onClick={() => void run(() => usersApi.setAdmin(u.name, !u.admin))} />
      {u.twoFactorEnabled && <MenuItem label={t.twoFactorReset} onClick={() => ask("twoFactor", u)} />}
      {u.passkeys > 0 && <MenuItem label={t.removePasskeys} onClick={() => ask("passkeys", u)} />}
      <MenuItemDivider />
      <MenuItem label={t.removeUser} kind="danger" onClick={() => ask("remove", u)} />
    </>
  );

  const notices = (
    <>
      {generated && <GeneratedPanel value={generated} done={() => setGenerated(null)} />}
      {error && (
        <p role="alert" className="app-text-error">
          {error}
        </p>
      )}
    </>
  );

  return (
    <div className="accounts-page">
      <div className="page page--wide">
        {detail ? (
          <UserDetail
            user={detail}
            keys={keys.filter((k) => k.user === detail.name)}
            heading={heading}
            notices={notices}
            toList={() => toList()}
            openProfile={() => update({ view: "profile" })}
            reset={() => ask("reset", detail)}
            menu={
              !detail.self && (
                <RowMenu label={t.userActions(detail.name)} register={menus.register(detail.name)}>
                  {items(detail, false)}
                </RowMenu>
              )
            }
            revoke={(k) => run(() => apiKeyApi.revokeAny(k.id))}
          />
        ) : (
          <>
            <header className="page-header">
              <div className="page-header__row">
                <h1 ref={heading} tabIndex={-1} className="page-header__title type-heading-04 app-page-title">
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
            {notices}
            {!users && !error && <p className="app-text-secondary">{t.loading}</p>}
            {users && <UserTable users={users} open={open} menu={(u) => (
              <RowMenu label={t.userActions(u.name)} register={menus.register(u.name)}>
                {items(u, true)}
              </RowMenu>
            )} />}
          </>
        )}
      </div>

      {adding && (
        <AddUserModal
          cancel={() => setAdding(false)}
          added={(g) => {
            setAdding(false);
            setGenerated(g);
            void reload();
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
            else {
              // Removed from its detail page: the list replaces the page.
              // Removed from its row: the row goes, and the focus goes to
              // the list's h1 instead of falling to the body.
              const fromDetail = state.id === name;
              void run(() => usersApi.remove(name)).then((ok) => {
                if (ok && fromDetail) toList(true);
                else if (ok) heading.current?.focus();
              });
            }
          }}
        />
      )}
    </div>
  );
}

/**
 * The users as a table. The name is the row header and links to the user's
 * page; the own row has no menu. Below 42rem the secondary columns give way
 * to a second line under the name.
 */
function UserTable({ users, open, menu }: { users: ManagedUser[]; open: (name: string) => void; menu: (u: ManagedUser) => ReactNode }) {
  return (
    <TableContainer className="users-table app-table">
      <Table aria-label={t.usersTitle} size="lg" overflowMenuOnHover={false}>
        <TableHead>
          <TableRow>
            <TableHeader>{t.colUser}</TableHeader>
            <TableHeader className="app-wide-only">{t.colRole}</TableHeader>
            <TableHeader>{t.colState}</TableHeader>
            <TableHeader className="app-wide-only">{t.colSignIn}</TableHeader>
            <TableHeader className="app-wide-only">{t.colAccounts}</TableHeader>
            <TableHeader className="app-wide-only">{t.colLastLogin}</TableHeader>
            <TableHeader>
              <span className="cds--visually-hidden">{t.colActions}</span>
            </TableHeader>
          </TableRow>
        </TableHead>
        <TableBody>
          {users.map((u) => (
            <TableRow key={u.name}>
              <TableHeader scope="row">
                <AppLink href={hrefOf(u.name)} go={() => open(u.name)} className="users-table__name">
                  {u.name}
                </AppLink>
                {u.self && <span className="app-text-secondary"> ({t.you})</span>}
                {u.self && <span className="users-table__hint app-text-secondary app-text-small">{t.selfHint}</span>}
                <span className="users-table__more app-narrow-only app-text-secondary app-text-small">
                  <span>
                    {u.admin ? t.roleAdmin : t.roleUser} · {lastLogin(u)}
                  </span>
                  {u.failingAccounts > 0 && (
                    <span>
                      <Tag size="sm" type="red">
                        {t.failingCount(u.failingAccounts)}
                      </Tag>
                    </span>
                  )}
                </span>
              </TableHeader>
              <TableCell className="app-wide-only">
                <RoleTag user={u} />
              </TableCell>
              <TableCell>
                <StateTag user={u} />
              </TableCell>
              <TableCell className="app-wide-only">
                <span className="users-table__lines">
                  <span>{u.twoFactorEnabled ? t.twoFactorShortOn : t.twoFactorShortOff}</span>
                  {u.passkeys > 0 && <span className="app-text-secondary">{t.passkeyCount(u.passkeys)}</span>}
                  {u.apiKeys > 0 && <span className="app-text-secondary">{t.apiKeyCount(u.apiKeys)}</span>}
                </span>
              </TableCell>
              <TableCell className="app-wide-only">
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
              <TableCell className="app-wide-only">{lastLogin(u)}</TableCell>
              <TableCell className="cds--table-column-menu">{!u.self && menu(u)}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </TableContainer>
  );
}

/**
 * A user's page: the role and state, Reset password and the other actions,
 * the summary tiles and the API keys with Revoke. The own user's page has
 * no user actions (they are on the profile), but keeps Revoke.
 */
function UserDetail({
  user,
  keys,
  heading,
  notices,
  toList,
  openProfile,
  reset,
  menu,
  revoke,
}: {
  user: ManagedUser;
  keys: ApiKey[];
  heading: RefObject<HTMLHeadingElement | null>;
  notices: ReactNode;
  toList: () => void;
  openProfile: () => void;
  reset: () => void;
  menu: ReactNode;
  revoke: (k: ApiKey) => Promise<unknown>;
}) {
  const s = settingsText();
  const id = useId();
  const keysHeading = useRef<HTMLHeadingElement>(null);
  // After a revoke, the key's Revoke button is gone: the focus goes to the
  // section's heading once the reloaded keys are shown.
  const [revoked, setRevoked] = useState(0);
  useLayoutEffect(() => {
    if (revoked > 0) keysHeading.current?.focus();
  }, [revoked]);
  return (
    <>
      <DetailHeader
        parent={{ label: t.usersTitle, href: hrefOf(), go: toList }}
        title={user.name}
        heading={heading}
        tags={
          <>
            <RoleTag user={user} />
            <StateTag user={user} />
          </>
        }
        actions={
          !user.self && (
            <Button kind="tertiary" onClick={reset}>
              {t.resetPassword}
            </Button>
          )
        }
        menu={menu}
      >
        {user.self && (
          <p className="page-header__intro app-text-secondary">
            {t.selfHint}{" "}
            <AppLink href="?view=profile" go={openProfile}>
              {s.openProfile}
            </AppLink>
          </p>
        )}
      </DetailHeader>
      {notices}
      <SummaryTiles
        items={[
          {
            label: t.colAccounts,
            value: (
              <span className="app-inline-list">
                <span className="app-tabular">{user.accounts}</span>
                {user.failingAccounts > 0 && (
                  <Tag size="sm" type="red">
                    {t.failingCount(user.failingAccounts)}
                  </Tag>
                )}
              </span>
            ),
          },
          { label: t.colLastLogin, value: lastLogin(user) },
          { label: t.twoFactor, value: user.twoFactorEnabled ? t.twoFactorStateOn : t.twoFactorStateOff },
          { label: t.passkeys, value: <span className="app-tabular">{user.passkeys}</span> },
          { label: s.created, value: shortDate(user.createdAt) },
        ]}
      />
      <section className="page__section" aria-labelledby={`${id}-keys`}>
        <h2 id={`${id}-keys`} ref={keysHeading} tabIndex={-1} className="type-heading-03 app-page-title">
          {t.apiKeys}
        </h2>
        {keys.length === 0 ? (
          <p className="app-text-secondary">{t.apiKeyNone}</p>
        ) : (
          <ApiKeyItems keys={keys} revoke={(k) => void revoke(k).then(() => setRevoked((n) => n + 1))} />
        )}
      </section>
    </>
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
