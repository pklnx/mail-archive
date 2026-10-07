import { useCallback, useEffect, useState, type FormEvent } from "react";
import { ApiError, passkeyApi, profileApi, retryAfter, type PasskeyList } from "./api";
import { relativeTime } from "./format";
import { t } from "./i18n";
import { browserError, creationOptions, credentialJSON, forgetPasskey, passkeysSupported } from "./webauthn";

const primary = "rounded-md bg-blue-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50";
const button =
  "rounded-md border border-zinc-300 px-2.5 py-1 text-sm hover:bg-zinc-100 disabled:opacity-50 dark:border-zinc-700 dark:hover:bg-zinc-800";
const input =
  "w-full rounded-md border border-zinc-300 bg-white px-2.5 py-1.5 text-sm outline-none focus:border-blue-500 dark:border-zinc-700 dark:bg-zinc-900";

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

/** The user's passkeys on the profile page: list, add, remove. */
export function PasskeySection() {
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
    profileApi.twoFactor.get().then((s) => setTwoFactor(s.enabled), () => undefined);
  }, []);
  useEffect(load, [load]);

  if (!list) return error ? <p role="alert" className="text-sm text-red-600">{error}</p> : <p className="text-sm text-zinc-500">{t.loading}</p>;
  if (!list.available) return <p className="text-sm text-zinc-500">{t.passkeysUnavailable}</p>;

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
      <p className="text-zinc-500">{t.passkeysText}</p>
      {list.passkeys.length === 0 ? (
        <p className="text-zinc-500">{t.passkeyNone}</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {list.passkeys.map((p) => (
            <li key={p.id} className="flex items-center gap-3 rounded-md border border-zinc-200 px-3 py-2 dark:border-zinc-800">
              <div className="mr-auto flex flex-col">
                <span className="font-medium">{p.name}</span>
                <span className="text-xs text-zinc-500">{p.lastUsedAt ? t.passkeyUsed(relativeTime(p.lastUsedAt)) : t.passkeyNeverUsed}</span>
              </div>
              <button type="button" className={button} onClick={() => void remove(p.id, p.name)}>
                {t.passkeyRemove}
              </button>
            </li>
          ))}
        </ul>
      )}
      {status && (
        <p role="status" className="text-green-700 dark:text-green-400">
          {status}
        </p>
      )}
      {!here && <p className="text-zinc-500">{t.passkeysOtherOrigin(list.origin ?? "")}</p>}
      {here && !passkeysSupported() && <p className="text-zinc-500">{t.passkeysUnsupported}</p>}
      {here && list.passkeys.length >= list.max && <p className="text-zinc-500">{t.passkeyLimit(list.max)}</p>}
      {canAdd && (
        <form className="flex flex-col gap-3 border-t border-zinc-200 pt-3 dark:border-zinc-800" onSubmit={add}>
          <label className="flex flex-col gap-1">
            {t.passkeyName}
            <input className={input} required maxLength={64} placeholder={t.passkeyNameHint} value={name} onChange={(e) => setName(e.target.value)} />
          </label>
          <p className="text-zinc-500">{twoFactor ? t.passkeyConfirm2FA : t.passkeyConfirm}</p>
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
            <p role="alert" className="text-red-600">
              {error}
            </p>
          )}
          <button type="submit" className={`${primary} self-start`} disabled={busy}>
            {t.passkeyAdd}
          </button>
        </form>
      )}
      {!canAdd && error && (
        <p role="alert" className="text-red-600">
          {error}
        </p>
      )}
    </div>
  );
}
