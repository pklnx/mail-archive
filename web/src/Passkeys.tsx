import { useCallback, useEffect, useState, type FormEvent } from "react";
import { ApiError, passkeyApi, profileApi, retryAfter, type PasskeyList } from "./api";
import { relativeTime } from "./format";
import { t } from "./i18n";
import { browserError, creationOptions, credentialJSON, forgetPasskey, passkeysSupported } from "./webauthn";

const primary = "rounded-md bg-accent px-3 py-1.5 text-sm font-medium text-on-accent hover:bg-accent-hover disabled:opacity-50 pointer-coarse:min-h-11";
const button =
  "rounded-md border border-line-control px-2.5 py-1 text-sm hover:bg-surface-hover disabled:opacity-50 pointer-coarse:min-h-11";
const input =
  "w-full rounded-md border border-line-control bg-field px-2.5 py-1.5 text-sm pointer-coarse:min-h-11";

/** The message for a failed registration. */
export function registrationError(err: unknown, max: number): string {
  const wait = retryAfter(err);
  if (wait !== null) return t.tooManyAttempts(wait);
  switch (browserError(err)) {
    case "NotAllowedError":
    case "AbortError":
      return t.passkeyCancelled;
    case "InvalidStateError":
      return t.passkeyOnDevice;
  }
  if (err instanceof ApiError) {
    if (err.status === 401) return err.message.includes("password") ? t.wrongCurrentPassword : t.invalidTwoFactor;
    if (err.status === 409 && err.message.includes("name")) return t.passkeyNameTaken;
    if (err.status === 409 && err.message.includes("already registered")) return t.passkeyOnDevice;
    if (err.status === 409) return t.passkeyLimit(max);
    if (err.status === 400 && err.message.includes("not accepted")) return t.passkeyFailed;
  }
  return t.failed(err instanceof Error ? err.message : String(err));
}

/**
 * The user's passkeys on the profile page: list, add, remove.
 * twoFactorVersion changes when two-factor authentication was turned on or
 * off elsewhere on the page; the section then asks the server again whether
 * a code is needed.
 */
export function PasskeySection({ twoFactorVersion = 0 }: { twoFactorVersion?: number }) {
  const [list, setList] = useState<PasskeyList | null>(null);
  const [twoFactor, setTwoFactor] = useState(false);
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [status, setStatus] = useState("");

  const load = useCallback(() => {
    passkeyApi.list().then(setList, (err: unknown) => setError(t.failed(err instanceof Error ? err.message : String(err))));
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
  if (!list.available) return <p className="text-sm text-ink-muted">{t.passkeysUnavailable}</p>;

  const here = list.origin === window.location.origin;
  const canAdd = here && passkeysSupported() && list.passkeys.length < list.max;

  const add = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    setStatus("");
    try {
      const { token, publicKey } = await passkeyApi.beginRegistration(name.trim(), password, code);
      const cred = (await navigator.credentials.create({ publicKey: creationOptions(publicKey) })) as PublicKeyCredential | null;
      if (!cred) throw new DOMException("no passkey", "NotAllowedError");
      await passkeyApi.finishRegistration(token, credentialJSON(cred));
      setStatus(t.passkeyAdded(name.trim()));
      setName("");
      setPassword("");
      setCode("");
      load();
    } catch (err) {
      setError(registrationError(err, list.max));
      setPassword("");
      setCode("");
    } finally {
      setBusy(false);
    }
  };

  const remove = async (id: number, passkeyName: string) => {
    if (!window.confirm(t.passkeyRemoveConfirm(passkeyName))) return;
    setError("");
    setStatus("");
    try {
      const removed = await passkeyApi.remove(id);
      if (removed.rpId) await forgetPasskey(removed.rpId, removed.credentialId);
    } catch (err) {
      setError(t.failed(err instanceof Error ? err.message : String(err)));
    }
    load();
  };

  return (
    <div className="flex flex-col gap-3 text-sm">
      <p className="text-ink-muted">{t.passkeysText}</p>
      {list.passkeys.length === 0 ? (
        <p className="text-ink-muted">{t.passkeyNone}</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {list.passkeys.map((p) => (
            <li key={p.id} className="flex items-center gap-3 rounded-md border border-line px-3 py-2">
              <div className="mr-auto flex flex-col">
                <span className="font-medium">{p.name}</span>
                <span className="text-xs text-ink-muted">{p.lastUsedAt ? t.passkeyUsed(relativeTime(p.lastUsedAt)) : t.passkeyNeverUsed}</span>
              </div>
              <button type="button" className={button} onClick={() => void remove(p.id, p.name)}>
                {t.passkeyRemove}
              </button>
            </li>
          ))}
        </ul>
      )}
      {status && (
        <p role="status" className="text-ok">
          {status}
        </p>
      )}
      {!here && <p className="text-ink-muted">{t.passkeysOtherOrigin(list.origin ?? "")}</p>}
      {here && !passkeysSupported() && <p className="text-ink-muted">{t.passkeysUnsupported}</p>}
      {here && list.passkeys.length >= list.max && <p className="text-ink-muted">{t.passkeyLimit(list.max)}</p>}
      {canAdd && (
        <form className="flex flex-col gap-3 border-t border-line pt-3" onSubmit={add}>
          <label className="flex flex-col gap-1">
            {t.passkeyName}
            <input className={input} required maxLength={64} placeholder={t.passkeyNameHint} value={name} onChange={(e) => setName(e.target.value)} />
          </label>
          <p className="text-ink-muted">{twoFactor ? t.passkeyConfirm2FA : t.passkeyConfirm}</p>
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
            {t.passkeyAdd}
          </button>
        </form>
      )}
      {!canAdd && error && (
        <p role="alert" className="text-danger">
          {error}
        </p>
      )}
    </div>
  );
}
