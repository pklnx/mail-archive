import { useCallback, useEffect, useState, type FormEvent } from "react";
import { ApiError, apiKeyApi, profileApi, retryAfter, type ApiKey, type ApiKeyList, type ApiKeyScope, type NewApiKey } from "./api";
import { relativeTime, shortDate } from "./format";
import { t } from "./i18n";

const primary = "rounded-md bg-accent px-3 py-1.5 text-sm font-medium text-on-accent hover:bg-accent-hover disabled:opacity-50 pointer-coarse:min-h-11";
const button =
  "rounded-md border border-line-control px-2.5 py-1 text-sm hover:bg-surface-hover disabled:opacity-50 pointer-coarse:min-h-11";
const input =
  "w-full rounded-md border border-line-control bg-field px-2.5 py-1.5 text-sm pointer-coarse:min-h-11";

/** The expiry choices, in days; null is no expiry. */
export const expiryChoices: (number | null)[] = [30, 90, 365, null];

/** The message for a failed key creation. */
export function apiKeyError(err: unknown, max: number): string {
  const wait = retryAfter(err);
  if (wait !== null) return t.tooManyAttempts(wait);
  if (err instanceof ApiError) {
    if (err.status === 401) return err.message.includes("password") ? t.wrongCurrentPassword : t.invalidTwoFactor;
    if (err.status === 409 && err.message.includes("name")) return t.apiKeyNameTaken;
    if (err.status === 409) return t.apiKeyLimit(max);
  }
  return t.failed(err instanceof Error ? err.message : String(err));
}

/** One line under a key's name: state, expiry and last use. */
export function apiKeyDetails(k: ApiKey): string {
  const parts = [`ma_…${k.hint}`, k.scopes.join(", ")];
  if (k.state !== "active") parts.push(t.apiKeyState(k.state));
  else if (k.expiresAt) parts.push(t.apiKeyExpires(shortDate(k.expiresAt)));
  parts.push(k.lastUsedAt ? t.apiKeyUsed(relativeTime(k.lastUsedAt)) : t.apiKeyNeverUsed);
  return parts.join(" · ");
}

/** A list of keys with a revoke button for the active ones. */
export function ApiKeyItems({ keys, revoke }: { keys: ApiKey[]; revoke: (k: ApiKey) => void }) {
  return (
    <ul className="flex flex-col gap-2">
      {keys.map((k) => (
        <li key={k.id} className="flex items-center gap-3 rounded-md border border-line px-3 py-2">
          <div className={`mr-auto flex flex-col ${k.state === "active" ? "" : "opacity-60"}`}>
            <span className="font-medium">{k.name}</span>
            <span className="text-xs text-ink-muted">{apiKeyDetails(k)}</span>
          </div>
          {k.state === "active" && (
            <button type="button" className={button} onClick={() => window.confirm(t.apiKeyRevokeConfirm(k.name)) && revoke(k)}>
              {t.apiKeyRevoke}
            </button>
          )}
        </li>
      ))}
    </ul>
  );
}

/** Shows a new key once, with a copy button. */
export function NewKeyPanel({ created, done }: { created: NewApiKey; done: () => void }) {
  const [copied, setCopied] = useState(false);
  return (
    <div role="status" className="flex flex-col gap-2 rounded-lg border border-warning-line bg-warning-soft p-3 text-sm">
      <p className="font-semibold">{t.apiKeyCreated(created.name)}</p>
      <code className="rounded bg-field px-2 py-1 font-mono text-sm break-all select-all">{created.key}</code>
      <div className="flex gap-2">
        <button
          type="button"
          className={button}
          onClick={() => navigator.clipboard?.writeText(created.key).then(() => setCopied(true), () => setCopied(false))}
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

/**
 * The user's API keys on the profile page: list, create, revoke.
 * twoFactorVersion changes when two-factor authentication was turned on or
 * off elsewhere on the page; the section then asks the server again whether
 * a code is needed.
 */
export function ApiKeySection({ twoFactorVersion = 0 }: { twoFactorVersion?: number }) {
  const [list, setList] = useState<ApiKeyList | null>(null);
  const [twoFactor, setTwoFactor] = useState(false);
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<ApiKeyScope[]>(["read"]);
  const [expiry, setExpiry] = useState<number | null>(90);
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [created, setCreated] = useState<NewApiKey | null>(null);

  const load = useCallback(() => {
    apiKeyApi.list().then(setList, (err: unknown) => setError(t.failed(err instanceof Error ? err.message : String(err))));
  }, []);
  useEffect(load, [load]);
  useEffect(() => {
    let current = true;
    profileApi.twoFactor.get().then((s) => current && setTwoFactor(s.enabled), () => undefined);
    return () => {
      current = false;
    };
  }, [twoFactorVersion]);

  if (!list) return error ? <p role="alert" className="text-sm text-danger">{error}</p> : <p className="text-sm text-ink-muted">{t.loading}</p>;

  const active = list.keys.filter((k) => k.state === "active").length;
  const toggle = (s: ApiKeyScope, on: boolean) => setScopes((cur) => (on ? [...cur.filter((x) => x !== s), s] : cur.filter((x) => x !== s)));

  const create = async (e: FormEvent) => {
    e.preventDefault();
    if (scopes.length === 0) {
      setError(t.apiKeyNoScope);
      return;
    }
    setBusy(true);
    setError("");
    setCreated(null);
    try {
      setCreated(await apiKeyApi.create({ name: name.trim(), scopes, expiresInDays: expiry, currentPassword: password, code }));
      setName("");
      load();
    } catch (err) {
      setError(apiKeyError(err, list.max));
    } finally {
      setPassword("");
      setCode("");
      setBusy(false);
    }
  };

  const revoke = async (k: ApiKey) => {
    setError("");
    try {
      await apiKeyApi.revoke(k.id);
    } catch (err) {
      setError(t.failed(err instanceof Error ? err.message : String(err)));
    }
    load();
  };

  return (
    <div className="flex flex-col gap-3 text-sm">
      <p className="text-ink-muted">{t.apiKeysText}</p>
      <p className="text-ink-muted">{t.apiKeysKeptOnPasswordChange}</p>
      {created && <NewKeyPanel created={created} done={() => setCreated(null)} />}
      {list.keys.length === 0 ? <p className="text-ink-muted">{t.apiKeyNone}</p> : <ApiKeyItems keys={list.keys} revoke={(k) => void revoke(k)} />}
      {active >= list.max ? (
        <p className="text-ink-muted">{t.apiKeyLimit(list.max)}</p>
      ) : (
        <form className="flex flex-col gap-3 border-t border-line pt-3" onSubmit={create}>
          <label className="flex flex-col gap-1">
            {t.apiKeyName}
            <input className={input} required maxLength={64} placeholder={t.apiKeyNameHint} value={name} onChange={(e) => setName(e.target.value)} />
          </label>
          <fieldset className="flex flex-col gap-1">
            <legend>{t.apiKeyScopes}</legend>
            <label className="flex items-center gap-2">
              <input type="checkbox" checked={scopes.includes("read")} onChange={(e) => toggle("read", e.target.checked)} />
              {t.apiKeyScopeRead}
            </label>
            <label className="flex items-center gap-2">
              <input type="checkbox" checked={scopes.includes("sync")} onChange={(e) => toggle("sync", e.target.checked)} />
              {t.apiKeyScopeSync}
            </label>
          </fieldset>
          <label className="flex flex-col gap-1">
            {t.apiKeyExpiry}
            <select className={input} value={expiry ?? ""} onChange={(e) => setExpiry(e.target.value === "" ? null : Number(e.target.value))}>
              {expiryChoices.map((d) => (
                <option key={d ?? "never"} value={d ?? ""}>
                  {d === null ? t.apiKeyExpiryNever : t.apiKeyExpiryDays(d)}
                </option>
              ))}
            </select>
          </label>
          <p className="text-ink-muted">{twoFactor ? t.apiKeyConfirm2FA : t.apiKeyConfirm}</p>
          <label className="flex flex-col gap-1">
            {t.currentPassword}
            <input className={input} type="password" required autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
          </label>
          {twoFactor && (
            <label className="flex flex-col gap-1">
              {t.twoFactorCode}
              <input className={input} required autoComplete="one-time-code" autoCapitalize="none" spellCheck={false} value={code} onChange={(e) => setCode(e.target.value)} />
            </label>
          )}
          {error && (
            <p role="alert" className="text-danger">
              {error}
            </p>
          )}
          <button type="submit" className={`${primary} self-start`} disabled={busy}>
            {t.apiKeyCreate}
          </button>
        </form>
      )}
    </div>
  );
}
