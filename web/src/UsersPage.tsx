import { useCallback, useEffect, useState, type FormEvent } from "react";
import { ApiKeyItems } from "./ApiKeys";
import { apiKeyApi, usersApi, type ApiKey, type GeneratedPassword, type ManagedUser } from "./api";
import { relativeTime } from "./format";
import { t } from "./i18n";

const button =
  "rounded-md border border-line-control px-2.5 py-1 text-sm hover:bg-surface-hover disabled:opacity-50 pointer-coarse:min-h-11";
const primary = "rounded-md bg-accent px-3 py-1 text-sm font-medium text-on-accent hover:bg-accent-hover disabled:opacity-50 pointer-coarse:min-h-11";
const input =
  "w-full rounded-md border border-line-control bg-field px-2.5 py-1.5 text-sm pointer-coarse:min-h-11";

const message = (err: unknown) => t.failed(err instanceof Error ? err.message : String(err));

/** User management for admins: logins only, never other users' mail. */
export function UsersPage({ close }: { close: () => void }) {
  const [users, setUsers] = useState<ManagedUser[] | null>(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [generated, setGenerated] = useState<GeneratedPassword | null>(null);
  // The user whose password reset waits for confirmation.
  const [resetting, setResetting] = useState<ManagedUser | null>(null);
  // The user whose API keys are shown, and every user's keys.
  const [showKeys, setShowKeys] = useState("");
  const [keys, setKeys] = useState<ApiKey[]>([]);

  const reload = useCallback(() => {
    usersApi.list().then(
      (u) => {
        setUsers(u);
        setError("");
      },
      (err: unknown) => setError(message(err)),
    );
    apiKeyApi.listAll().then(setKeys, () => setKeys([]));
  }, []);
  useEffect(reload, [reload]);

  const run = async (action: () => Promise<unknown>) => {
    setError("");
    try {
      await action();
    } catch (err) {
      setError(message(err));
    }
    reload();
  };

  return (
    <div className="mx-auto flex h-full max-w-3xl flex-col gap-4 overflow-y-auto p-4">
      <button type="button" className="self-start text-sm text-accent-ink lg:hidden pointer-coarse:min-h-11" onClick={close}>
        {t.backToMail}
      </button>
      <div className="flex flex-wrap items-center gap-2">
        <h1 className="mr-auto text-xl font-semibold">{t.usersTitle}</h1>
        {!adding && (
          <button type="button" className={primary} onClick={() => setAdding(true)}>
            {t.addUser}
          </button>
        )}
      </div>
      <p className="text-sm text-ink-muted">{t.adminsSeeNoMail}</p>

      {generated && <GeneratedPanel value={generated} done={() => setGenerated(null)} />}
      {adding && (
        <AddUserForm
          cancel={() => setAdding(false)}
          added={(g) => {
            setAdding(false);
            setGenerated(g);
            reload();
          }}
        />
      )}
      {error && (
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
      )}
      {!users && !error && <p className="text-sm text-ink-muted">{t.loading}</p>}

      <ul className="flex flex-col gap-3">
        {users?.map((u) => (
          <li key={u.name} className="flex flex-col gap-2 rounded-lg border border-line p-3">
            <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
              <span className="font-semibold">{u.name}</span>
              {u.self && <span className="text-sm text-ink-muted">({t.you})</span>}
              <span className="text-sm text-ink-muted">{u.admin ? t.roleAdmin : t.roleUser}</span>
              <span className={`text-sm ${u.locked ? "text-danger" : "text-ink-muted"}`}>
                {u.locked ? t.stateLocked : u.mustChangePassword ? t.stateMustChange : t.stateActive}
              </span>
              <span className="text-sm text-ink-muted">{u.twoFactorEnabled ? t.twoFactorShortOn : t.twoFactorShortOff}</span>
              {u.passkeys > 0 && <span className="text-sm text-ink-muted">{t.passkeyCount(u.passkeys)}</span>}
              {u.apiKeys > 0 && <span className="text-sm text-ink-muted">{t.apiKeyCount(u.apiKeys)}</span>}
              {u.failingAccounts > 0 && <span className="text-sm font-medium text-danger">{t.failingCount(u.failingAccounts)}</span>}
              <span className="ml-auto text-sm text-ink-muted">
                {t.colAccounts}: {u.accounts} · {t.colLastLogin}: {u.lastLoginAt ? relativeTime(u.lastLoginAt) : t.never}
              </span>
            </div>
            {keys.some((k) => k.user === u.name) && (
              <button type="button" className={`${button} self-start`} onClick={() => setShowKeys(showKeys === u.name ? "" : u.name)}>
                {showKeys === u.name ? t.hideApiKeys : t.showApiKeys}
              </button>
            )}
            {showKeys === u.name && (
              <ApiKeyItems keys={keys.filter((k) => k.user === u.name)} revoke={(k) => void run(() => apiKeyApi.revokeAny(k.id))} />
            )}
            {u.self ? (
              <p className="text-sm text-ink-muted">{t.selfHint}</p>
            ) : (
              <div className="flex flex-wrap gap-2">
                {u.twoFactorEnabled && (
                  <button
                    type="button"
                    className={button}
                    onClick={() => window.confirm(t.twoFactorResetConfirm(u.name)) && run(() => usersApi.reset2FA(u.name))}
                  >
                    {t.twoFactorReset}
                  </button>
                )}
                {u.passkeys > 0 && (
                  <button
                    type="button"
                    className={button}
                    onClick={() => window.confirm(t.confirmRemovePasskeys(u.name, u.passkeys)) && run(() => usersApi.removePasskeys(u.name))}
                  >
                    {t.removePasskeys}
                  </button>
                )}
                <button type="button" className={button} onClick={() => setResetting(u)}>
                  {t.resetPassword}
                </button>
                <button
                  type="button"
                  className={button}
                  onClick={() => (u.locked || window.confirm(t.confirmLock(u.name))) && run(() => usersApi.setLocked(u.name, !u.locked))}
                >
                  {u.locked ? t.unlock : t.lock}
                </button>
                <button type="button" className={button} onClick={() => run(() => usersApi.setAdmin(u.name, !u.admin))}>
                  {u.admin ? t.removeAdmin : t.makeAdmin}
                </button>
                <button
                  type="button"
                  className={`${button} text-danger`}
                  onClick={() => window.confirm(t.confirmRemoveUser(u.name)) && run(() => usersApi.remove(u.name))}
                >
                  {t.removeUser}
                </button>
              </div>
            )}
            {resetting?.name === u.name && (
              <ResetPasswordConfirm
                user={u}
                cancel={() => setResetting(null)}
                confirm={(removePasskeys, revokeApiKeys) => {
                  setResetting(null);
                  void run(async () => setGenerated(await usersApi.resetPassword(u.name, removePasskeys, revokeApiKeys)));
                }}
              />
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}

/**
 * Asks before a password reset. Passkeys are removed and API keys revoked
 * by default: after a compromise, a new password alone would not lock the
 * attacker out.
 */
function ResetPasswordConfirm({
  user,
  cancel,
  confirm,
}: {
  user: ManagedUser;
  cancel: () => void;
  confirm: (removePasskeys: boolean, revokeApiKeys: boolean) => void;
}) {
  const [removePasskeys, setRemovePasskeys] = useState(true);
  const [revokeApiKeys, setRevokeApiKeys] = useState(true);
  return (
    <div role="group" className="flex flex-col gap-2 rounded-md border border-warning-line bg-warning-soft p-3 text-sm">
      <p>{t.confirmResetPassword(user.name)}</p>
      {user.passkeys > 0 && (
        <label className="flex items-center gap-2">
          <input type="checkbox" checked={removePasskeys} onChange={(e) => setRemovePasskeys(e.target.checked)} />
          {t.resetAlsoPasskeys(user.passkeys)}
        </label>
      )}
      {user.apiKeys > 0 && (
        <label className="flex items-center gap-2">
          <input type="checkbox" checked={revokeApiKeys} onChange={(e) => setRevokeApiKeys(e.target.checked)} />
          {t.resetAlsoApiKeys(user.apiKeys)}
        </label>
      )}
      <div className="flex gap-2">
        <button type="button" className={primary} onClick={() => confirm(user.passkeys > 0 && removePasskeys, revokeApiKeys)}>
          {t.confirmReset}
        </button>
        <button type="button" className={button} onClick={cancel}>
          {t.cancel}
        </button>
      </div>
    </div>
  );
}

function AddUserForm({ cancel, added }: { cancel: () => void; added: (g: GeneratedPassword) => void }) {
  const [name, setName] = useState("");
  const [admin, setAdmin] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const submit = async (e: FormEvent) => {
    e.preventDefault();
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
    <form className="flex flex-col gap-3 rounded-lg border border-line p-3 text-sm" onSubmit={submit}>
      <label className="flex flex-col gap-1">
        {t.userName}
        <input className={input} required autoFocus autoCapitalize="none" spellCheck={false} value={name} onChange={(e) => setName(e.target.value)} />
      </label>
      <label className="flex items-center gap-2">
        <input type="checkbox" checked={admin} onChange={(e) => setAdmin(e.target.checked)} />
        {t.userIsAdmin}
      </label>
      {error && (
        <p role="alert" className="text-danger">
          {error}
        </p>
      )}
      <div className="flex gap-2">
        <button type="submit" className={primary} disabled={busy}>
          {t.addUser}
        </button>
        <button type="button" className={button} onClick={cancel}>
          {t.cancel}
        </button>
      </div>
    </form>
  );
}

/** Shows a generated password once, with a copy button. */
function GeneratedPanel({ value, done }: { value: GeneratedPassword; done: () => void }) {
  const [copied, setCopied] = useState(false);
  return (
    <div role="status" className="flex flex-col gap-2 rounded-lg border border-warning-line bg-warning-soft p-3 text-sm">
      <p className="font-semibold">{t.generatedFor(value.name)}</p>
      <code className="w-fit rounded bg-field px-2 py-1 font-mono text-base select-all">{value.password}</code>
      <p>{t.generatedHint}</p>
      <div className="flex gap-2">
        <button
          type="button"
          className={button}
          onClick={() => navigator.clipboard?.writeText(value.password).then(() => setCopied(true), () => setCopied(false))}
        >
          {copied ? t.copied : t.copy}
        </button>
        <button type="button" className={primary} onClick={done}>
          {t.done}
        </button>
      </div>
    </div>
  );
}
