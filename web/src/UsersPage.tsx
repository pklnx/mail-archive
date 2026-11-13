import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState, type FormEvent, type ReactNode, type RefObject } from "react";
import {
  Button,
  Checkbox,
  CodeSnippet,
  MenuItem,
  MenuItemDivider,
  RadioButton,
  RadioButtonGroup,
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
import { LockHint, SmtpCredentialsAdmin } from "./SmtpCredentials";
import { smtpApi, type AdminSMTPCredential } from "./smtpApi";
import { TableSkeleton } from "./tableSkeleton";
import { formatState, parseState, useViewState } from "./urlState";
import { groupsApi, userAdminApi, type CustomerGroup } from "./userAdminApi";
import { usersText } from "./usersText";

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
type Asking = { kind: "reset" | "lock" | "twoFactor" | "passkeys" | "remove" | "group"; user: ManagedUser } | null;

/** A Modal of the Groups section that is open (#470). */
type GroupAsking = { kind: "add" } | { kind: "rename" | "remove"; group: CustomerGroup } | null;

/** The URL of a user's detail page, or of the list. */
const hrefOf = (id = "") => formatState({ ...parseState(""), view: "users", id });

/** The role in words: admin, auditor (#393) or user. */
const roleOf = (user: ManagedUser) => (user.admin ? t.roleAdmin : user.auditor ? usersText().roleAuditorTag : t.roleUser);

const RoleTag = ({ user }: { user: ManagedUser }) => (
  <Tag size="sm" type={user.admin ? "purple" : user.auditor ? "teal" : "gray"}>
    {roleOf(user)}
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
  // Customer groups (#470): their UI shows only while there are two or more.
  const [groups, setGroups] = useState<CustomerGroup[]>([]);
  const [groupAsking, setGroupAsking] = useState<GroupAsking>(null);
  const [groupFilter, setGroupFilter] = useState("");
  const [done, setDone] = useState("");
  // Every smtp account's SMTP secrets (#395 Q2), and after a lock how many
  // of the locked user's still work, with a link to them.
  const [secrets, setSecrets] = useState<AdminSMTPCredential[]>([]);
  const [lockHint, setLockHint] = useState<{ user: string; count: number } | null>(null);
  const [toSecrets, setToSecrets] = useState(false);
  const menus = useMenuButtons();
  const heading = useRef<HTMLHeadingElement>(null);
  const groupsHeading = useRef<HTMLHeadingElement>(null);
  const secretsHeading = useRef<HTMLHeadingElement>(null);
  const us = usersText();

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
      groupsApi.list().then(setGroups, () => setGroups([])),
      smtpApi.listAll().then(setSecrets, () => setSecrets([])),
    ]);
    return listed;
  }, []);
  const multi = groups.length >= 2;
  // A filter of a group that is gone (or of a single group left) shows all.
  const filtered = multi && groups.some((g) => String(g.id) === groupFilter) ? groupFilter : "";
  useEffect(() => {
    void reload();
  }, [reload]);

  // The error of a failed action is set after the reload, whose success
  // would clear it otherwise. Resolves to whether the action succeeded.
  const run = async (action: () => Promise<unknown>) => {
    setError("");
    setDone("");
    setLockHint(null);
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
  // The lock hint's link: the list's SMTP secrets get the focus once shown.
  // A passive effect, so it runs after the page change's focus on the h1.
  const secretsShown = !detail && users !== null && secrets.length > 0;
  useEffect(() => {
    if (!toSecrets || !secretsShown) return;
    secretsHeading.current?.focus();
    secretsHeading.current?.scrollIntoView?.({ block: "start" });
    setToSecrets(false);
  }, [toSecrets, secretsShown]);

  // The section reports the revoke itself; the list reloads either way.
  const revokeSecret = async (c: AdminSMTPCredential) => {
    try {
      await smtpApi.revokeAny(c.id);
    } finally {
      void reload();
    }
  };

  const ask = (kind: NonNullable<Asking>["kind"], user: ManagedUser) => setAsking({ kind, user });
  const focusBack = () => (asking && menus.button(asking.user.name)) ?? heading.current;
  const open = (name: string) => update({ id: name });
  const toList = (replace = false) => update({ id: "" }, { replace });

  /** The actions of the menu, for a row and for the detail page. */
  const items = (u: ManagedUser, withReset: boolean) => (
    <>
      {withReset && <MenuItem label={t.resetPassword} onClick={() => ask("reset", u)} />}
      <MenuItem label={u.locked ? t.unlock : t.lock} onClick={() => (u.locked ? void run(() => userAdminApi.setLocked(u.name, false)) : ask("lock", u))} />
      {/* An auditor's role is fixed (#393). */}
      {!u.auditor && <MenuItem label={u.admin ? t.removeAdmin : t.makeAdmin} onClick={() => void run(() => userAdminApi.setAdmin(u.name, !u.admin))} />}
      {multi && <MenuItem label={us.changeGroup} onClick={() => ask("group", u)} />}
      {u.twoFactorEnabled && <MenuItem label={t.twoFactorReset} onClick={() => ask("twoFactor", u)} />}
      {u.passkeys > 0 && <MenuItem label={t.removePasskeys} onClick={() => ask("passkeys", u)} />}
      <MenuItemDivider />
      <MenuItem label={t.removeUser} kind="danger" onClick={() => ask("remove", u)} />
    </>
  );

  const notices = (
    <>
      {generated && <GeneratedPanel value={generated} done={() => setGenerated(null)} />}
      {done && <p role="status">{done}</p>}
      {lockHint && (
        <LockHint
          user={lockHint.user}
          count={lockHint.count}
          href={hrefOf()}
          open={() => {
            if (state.id) toList();
            setToSecrets(true);
          }}
        />
      )}
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
            group={multi ? detail.group.name : null}
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
            {!users && !error && (
              <TableSkeleton
                columns={[
                  { header: t.colUser },
                  { header: t.colRole, wide: true },
                  { header: t.colState },
                  { header: t.colSignIn, wide: true },
                  { header: t.colAccounts, wide: true },
                  { header: t.colLastLogin, wide: true },
                  { header: "" },
                ]}
              />
            )}
            {users && multi && (
              <Select
                id="users-group-filter"
                className="users-filter"
                labelText={us.colGroup}
                value={filtered}
                onChange={(e) => setGroupFilter(e.target.value)}
              >
                <SelectItem value="" text={us.allGroups} />
                {groups.map((g) => (
                  <SelectItem key={g.id} value={String(g.id)} text={g.name} />
                ))}
              </Select>
            )}
            {users && (
              <UserTable
                users={filtered ? users.filter((u) => String(u.group.id) === filtered) : users}
                withGroup={multi}
                open={open}
                menu={(u) => (
                  <RowMenu label={t.userActions(u.name)} register={menus.register(u.name)}>
                    {items(u, true)}
                  </RowMenu>
                )}
              />
            )}
            {secretsShown && <SmtpCredentialsAdmin credentials={secrets} heading={secretsHeading} revoke={revokeSecret} />}
            {users && multi && (
              <GroupsSection
                groups={groups}
                heading={groupsHeading}
                add={() => setGroupAsking({ kind: "add" })}
                menu={(g) => (
                  <RowMenu label={us.groupActions(g.name)} register={menus.register(`group:${g.id}`)}>
                    <MenuItem label={us.renameGroup} onClick={() => setGroupAsking({ kind: "rename", group: g })} />
                    <MenuItem label={us.removeGroup} kind="danger" disabled={g.users > 0} onClick={() => setGroupAsking({ kind: "remove", group: g })} />
                  </RowMenu>
                )}
              />
            )}
          </>
        )}
      </div>

      {adding && (
        <AddUserModal
          groups={multi ? groups : []}
          initialGroup={filtered}
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
            void run(async () => setGenerated(await userAdminApi.resetPassword(name, removePasskeys, revokeApiKeys)));
          }}
        />
      )}
      {asking?.kind === "group" && (
        <ChangeGroupModal
          user={asking.user}
          groups={groups}
          returnFocus={focusBack}
          cancel={() => setAsking(null)}
          confirm={(to) => {
            const name = asking.user.name;
            setAsking(null);
            void run(async () => {
              const ended = await userAdminApi.setGroup(name, to.id);
              setDone(us.groupChanged(name, to.name, ended.sharesEnded, ended.grantsRevoked ?? 0));
            });
          }}
        />
      )}
      {groupAsking && groupAsking.kind !== "remove" && (
        <GroupNameModal
          group={groupAsking.kind === "rename" ? groupAsking.group : null}
          cancel={() => setGroupAsking(null)}
          save={async (name) => {
            if (groupAsking.kind === "rename") await groupsApi.rename(groupAsking.group.id, name);
            else await groupsApi.create(name);
            setGroupAsking(null);
            setError("");
            setDone("");
            await reload();
          }}
        />
      )}
      {groupAsking?.kind === "remove" && (
        <DangerModal
          size="xs"
          modalHeading={us.removeGroupTitle(groupAsking.group.name)}
          primaryButtonText={us.removeGroup}
          secondaryButtonText={t.cancel}
          returnFocus={() => groupsHeading.current}
          onRequestClose={() => setGroupAsking(null)}
          onRequestSubmit={() => {
            const { id } = groupAsking.group;
            setGroupAsking(null);
            void run(() => groupsApi.remove(id));
          }}
        >
          <p>{us.removeGroupText}</p>
        </DangerModal>
      )}
      {asking && asking.kind !== "reset" && asking.kind !== "group" && (
        <ConfirmUser
          asking={asking}
          returnFocus={focusBack}
          cancel={() => setAsking(null)}
          confirm={() => {
            const { kind, user } = asking;
            setAsking(null);
            const name = user.name;
            if (kind === "lock")
              void run(async () => {
                // A lock does not stop the user's SMTP secrets (#395 decision 9).
                const count = await userAdminApi.setLocked(name, true);
                if (count > 0) setLockHint({ user: name, count });
              });
            else if (kind === "twoFactor") void run(() => userAdminApi.reset2FA(name));
            else if (kind === "passkeys") void run(() => userAdminApi.removePasskeys(name));
            else {
              // Removed from its detail page: the list replaces the page.
              // Removed from its row: the row goes, and the focus goes to
              // the list's h1 instead of falling to the body.
              const fromDetail = state.id === name;
              // An auditor's removal ends its grants (#393): they are named.
              void run(async () => {
                const ended = await userAdminApi.remove(name);
                if (user.auditor) setDone(us.auditorRemoved(name, ended));
              }).then((ok) => {
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
function UserTable({
  users,
  withGroup,
  open,
  menu,
}: {
  users: ManagedUser[];
  withGroup: boolean;
  open: (name: string) => void;
  menu: (u: ManagedUser) => ReactNode;
}) {
  const us = usersText();
  return (
    <TableContainer className="users-table app-table">
      <Table aria-label={t.usersTitle} size="lg" overflowMenuOnHover={false}>
        <TableHead>
          <TableRow>
            <TableHeader>{t.colUser}</TableHeader>
            <TableHeader className="app-wide-only">{t.colRole}</TableHeader>
            {withGroup && <TableHeader className="app-wide-only">{us.colGroup}</TableHeader>}
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
                    {roleOf(u)} · {withGroup && `${u.group.name} · `}
                    {lastLogin(u)}
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
              {withGroup && <TableCell className="app-wide-only">{u.group.name}</TableCell>}
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
  group,
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
  /** The user's group, shown while there are two or more; else null. */
  group: string | null;
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
          ...(group === null ? [] : [{ label: usersText().colGroup, value: group }]),
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
          : [t.removeUserTitle(user.name), user.auditor ? `${usersText().removeAuditorText(user.name)} ${t.removeUserText}` : t.removeUserText, t.removeUser];
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

/**
 * Add user: a name, the admin choice and, while there are two or more
 * customer groups, the group (#470); the server makes the password.
 */
function AddUserModal({
  groups,
  initialGroup,
  cancel,
  added,
}: {
  /** The groups to choose from; empty while there is only one. */
  groups: CustomerGroup[];
  initialGroup: string;
  cancel: () => void;
  added: (g: GeneratedPassword) => void;
}) {
  const us = usersText();
  const [name, setName] = useState("");
  // The role (#393): a user, an admin, or an auditor (never both).
  const [role, setRole] = useState<"user" | "admin" | "auditor">("user");
  const [group, setGroup] = useState(initialGroup);
  const groupField = useRef<HTMLSelectElement>(null);
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
    if (groups.length > 0 && !group) {
      groupField.current?.focus();
      return;
    }
    setBusy(true);
    setError("");
    try {
      added(await userAdminApi.create(name, role === "admin", groups.length > 0 ? Number(group) : undefined, role === "auditor"));
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
        {groups.length > 0 && (
          <Select
            id={`${id}-group`}
            ref={groupField}
            labelText={us.colGroup}
            required
            invalid={tried && !group}
            invalidText={us.groupRequired}
            value={group}
            onChange={(e) => setGroup(e.target.value)}
          >
            <SelectItem value="" text={us.chooseGroup} />
            {groups.map((g) => (
              <SelectItem key={g.id} value={String(g.id)} text={g.name} />
            ))}
          </Select>
        )}
        <RadioButtonGroup
          name={`${id}-role`}
          legendText={us.role}
          orientation="vertical"
          valueSelected={role}
          onChange={(v) => setRole(v as typeof role)}
          helperText={role === "auditor" ? us.auditorHelp : undefined}
        >
          <RadioButton id={`${id}-role-user`} value="user" labelText={us.roleMember} />
          <RadioButton id={`${id}-admin`} value="admin" labelText={t.userIsAdmin} />
          <RadioButton id={`${id}-auditor`} value="auditor" labelText={us.roleAuditor} />
        </RadioButtonGroup>
        {error && (
          <p role="alert" className="app-text-error">
            {error}
          </p>
        )}
      </form>
    </AppModal>
  );
}

/**
 * Change group (#470): the new group, and what the move ends. The user's
 * shares with users of the old group end; their accounts move along.
 */
function ChangeGroupModal({
  user,
  groups,
  returnFocus,
  cancel,
  confirm,
}: {
  user: ManagedUser;
  groups: CustomerGroup[];
  returnFocus: () => HTMLElement | null | undefined;
  cancel: () => void;
  confirm: (to: CustomerGroup) => void;
}) {
  const us = usersText();
  const others = groups.filter((g) => g.id !== user.group.id);
  const [to, setTo] = useState(others.length === 1 ? String(others[0]!.id) : "");
  const [tried, setTried] = useState(false);
  const id = useId();
  const field = useRef<HTMLSelectElement>(null);
  const submit = (e?: FormEvent) => {
    e?.preventDefault();
    setTried(true);
    const chosen = others.find((g) => String(g.id) === to);
    if (chosen) confirm(chosen);
    else field.current?.focus();
  };
  return (
    <AppModal
      size="sm"
      modalHeading={us.changeGroupTitle(user.name)}
      primaryButtonText={us.changeGroup}
      secondaryButtonText={t.cancel}
      returnFocus={returnFocus}
      onRequestClose={cancel}
      onRequestSubmit={() => submit()}
    >
      <form className="modal-stack" onSubmit={submit} noValidate>
        <p>{us.changeGroupText(user.name, user.group.name, user.auditor)}</p>
        <Select
          id={`${id}-group`}
          ref={field}
          data-modal-primary-focus=""
          labelText={us.newGroup}
          required
          invalid={tried && !to}
          invalidText={us.groupRequired}
          value={to}
          onChange={(e) => setTo(e.target.value)}
        >
          <SelectItem value="" text={us.chooseGroup} />
          {others.map((g) => (
            <SelectItem key={g.id} value={String(g.id)} text={g.name} />
          ))}
        </Select>
      </form>
    </AppModal>
  );
}

/**
 * The customer groups under the users table (#470), with Add group and a
 * menu per group: Rename, and Remove while the group has no users.
 */
function GroupsSection({
  groups,
  heading,
  add,
  menu,
}: {
  groups: CustomerGroup[];
  heading: RefObject<HTMLHeadingElement | null>;
  add: () => void;
  menu: (g: CustomerGroup) => ReactNode;
}) {
  const us = usersText();
  const s = settingsText();
  const id = useId();
  return (
    <section className="page__section" aria-labelledby={`${id}-groups`}>
      <div className="app-section-head">
        <h2 id={`${id}-groups`} ref={heading} tabIndex={-1} className="type-heading-03 app-page-title">
          {us.groupsTitle}
        </h2>
        <Button kind="tertiary" size="md" renderIcon={Add} onClick={add}>
          {us.addGroup}
        </Button>
      </div>
      <p className="app-text-secondary">
        {us.groupsHelp} {us.groupHasUsers}
      </p>
      <TableContainer className="users-table app-table">
        <Table aria-label={us.groupsTitle} size="lg" overflowMenuOnHover={false}>
          <TableHead>
            <TableRow>
              <TableHeader>{t.colName}</TableHeader>
              <TableHeader>{us.colUsers}</TableHeader>
              <TableHeader>{t.colAccounts}</TableHeader>
              <TableHeader className="app-wide-only">{s.created}</TableHeader>
              <TableHeader>
                <span className="cds--visually-hidden">{t.colActions}</span>
              </TableHeader>
            </TableRow>
          </TableHead>
          <TableBody>
            {groups.map((g) => (
              <TableRow key={g.id}>
                <TableHeader scope="row">
                  <span className="users-table__name">{g.name}</span>
                </TableHeader>
                <TableCell className="app-tabular">{g.users}</TableCell>
                <TableCell className="app-tabular">{g.accounts}</TableCell>
                <TableCell className="app-wide-only">{shortDate(g.createdAt)}</TableCell>
                <TableCell className="cds--table-column-menu">{menu(g)}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableContainer>
    </section>
  );
}

/** Add group, or rename one: the name; the server's refusal (a taken name) shows in the Modal. */
function GroupNameModal({ group, cancel, save }: { group: CustomerGroup | null; cancel: () => void; save: (name: string) => Promise<void> }) {
  const us = usersText();
  const [name, setName] = useState(group?.name ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [tried, setTried] = useState(false);
  const id = useId();
  const field = useRef<HTMLInputElement>(null);
  const title = group ? us.renameGroupTitle(group.name) : us.addGroup;
  const action = group ? us.renameGroup : us.addGroup;

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
      await save(name.trim());
    } catch (err) {
      setError(message(err));
      setBusy(false);
    }
  };

  return (
    <AppModal
      size="sm"
      modalHeading={title}
      primaryButtonText={action}
      secondaryButtonText={t.cancel}
      loadingStatus={busy ? "active" : "inactive"}
      loadingDescription={action}
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
          labelText={us.groupName}
          autoComplete="off"
          required
          maxLength={100}
          invalid={tried && !name.trim()}
          invalidText={us.groupNameRequired}
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
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
