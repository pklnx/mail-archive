import { useEffect, useState, type FormEvent } from "react";
import { ApiError, profileApi, retryAfter, type TwoFactorSetup as TwoFactorSetupState } from "./api";
import { t } from "./i18n";

const primary = "rounded-md bg-blue-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50";
const input =
  "w-full rounded-md border border-zinc-300 bg-white px-2.5 py-1.5 text-sm outline-none focus:border-blue-500 dark:border-zinc-700 dark:bg-zinc-900";

/** Same rule as the server (auth.MinPasswordLength), checked early for a localized message. */
const minLength = 12;

function changeError(err: unknown): string {
  const wait = retryAfter(err);
  if (wait !== null) return t.tooManyAttempts(wait);
  if (err instanceof ApiError && err.status === 401) return t.wrongCurrentPassword;
  return t.failed(err instanceof Error ? err.message : String(err));
}

/** Changes the logged-in user's password; calls done after success. */
export function PasswordForm({ done }: { done: () => void }) {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [repeat, setRepeat] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError("");
    if ([...next].length < minLength) return setError(t.passwordRules);
    if (next !== repeat) return setError(t.passwordsDiffer);
    setBusy(true);
    try {
      await profileApi.changePassword(current, next);
      setCurrent("");
      setNext("");
      setRepeat("");
      done();
    } catch (err) {
      setError(changeError(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form className="flex flex-col gap-3 text-sm" onSubmit={submit}>
      <label className="flex flex-col gap-1">
        {t.currentPassword}
        <input className={input} type="password" required autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} />
      </label>
      <label className="flex flex-col gap-1">
        {t.newPassword}
        <input className={input} type="password" required autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} />
      </label>
      <label className="flex flex-col gap-1">
        {t.repeatPassword}
        <input className={input} type="password" required autoComplete="new-password" value={repeat} onChange={(e) => setRepeat(e.target.value)} />
      </label>
      <p className="text-zinc-500">{t.passwordRules}</p>
      {error && (
        <p role="alert" className="text-red-600">
          {error}
        </p>
      )}
      <button type="submit" className={`${primary} self-start`} disabled={busy}>
        {t.changePassword}
      </button>
    </form>
  );
}

export function ProfilePage({ name, close }: { name: string; close: () => void }) {
  const [changed, setChanged] = useState(false);
  return (
    <div className="mx-auto flex h-full max-w-md flex-col gap-4 overflow-y-auto p-4">
      <button type="button" className="self-start text-sm text-blue-600 lg:hidden" onClick={close}>
        {t.backToMail}
      </button>
      <h1 className="text-xl font-semibold">{t.profile}</h1>
      <p className="text-sm text-zinc-500">{t.loggedInAs(name)}</p>
      <h2 className="font-semibold">{t.changePassword}</h2>
      {changed && (
        <p role="status" className="text-sm text-green-700 dark:text-green-400">
          {t.passwordChanged}
        </p>
      )}
      <PasswordForm done={() => setChanged(true)} />
      <h2 className="font-semibold">{t.twoFactor}</h2>
      <TwoFactorSetup />
    </div>
  );
}


export function TwoFactorSetup({ onDone }: { onDone?: () => void }) {
  const [state, setState] = useState<TwoFactorSetupState | null>(null);
  const [setup, setSetup] = useState<TwoFactorSetupState | null>(null);
  const [recovery, setRecovery] = useState<string[] | null>(null);
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const load = async () => {
    try { setState(await profileApi.twoFactor.get()); }
    catch (err) { setError(err instanceof Error ? err.message : String(err)); }
  };
  useEffect(() => { void load(); }, []);

  const start = async () => {
    setBusy(true); setError("");
    try { setSetup((await profileApi.twoFactor.setup())!); }
    catch (err) { setError(err instanceof Error ? err.message : String(err)); }
    finally { setBusy(false); }
  };

  const confirm = async (e: FormEvent) => {
    e.preventDefault(); setBusy(true); setError("");
    try {
      const result = await profileApi.twoFactor.confirm(code);
      setRecovery(result!.recoveryCodes);
      setSetup(null); setCode(""); setState((s) => s ? { ...s, enabled: true, setupPending: false } : s);
      onDone?.();
    } catch (err) { setError(err instanceof Error ? err.message : String(err)); }
    finally { setBusy(false); }
  };

  const disable = async (e: FormEvent) => {
    e.preventDefault(); setBusy(true); setError("");
    try {
      await profileApi.twoFactor.disable(password, code);
      setPassword(""); setCode(""); setState((s) => s ? { ...s, enabled: false } : s);
    } catch (err) { setError(err instanceof Error ? err.message : String(err)); }
    finally { setBusy(false); }
  };

  const regenerate = async (e: FormEvent) => {
    e.preventDefault(); setBusy(true); setError("");
    try {
      const result = await profileApi.twoFactor.regenerateRecoveryCodes(code);
      setRecovery(result!.recoveryCodes); setCode("");
    } catch (err) { setError(err instanceof Error ? err.message : String(err)); }
    finally { setBusy(false); }
  };

  if (!state) return <p className="text-sm text-zinc-500">{t.loading}</p>;
  if (recovery) return (
    <div className="flex flex-col gap-3">
      <h3 className="font-semibold">{t.twoFactorRecovery}</h3>
      <p className="text-sm text-zinc-500">{t.twoFactorRecoveryText}</p>
      <pre className="rounded-md bg-zinc-100 p-3 text-xs dark:bg-zinc-800">{recovery.join("\n")}</pre>
      <button type="button" className={primary} onClick={() => setRecovery(null)}>{t.done}</button>
    </div>
  );
  if (!state.enabled) {
    if (!setup) return (
      <div className="flex flex-col gap-3">
        <p className="text-sm text-zinc-500">{t.twoFactorSetupText}</p>
        <button type="button" className={primary} onClick={start} disabled={busy}>{t.twoFactorSetup}</button>
        {error && <p role="alert" className="text-red-600">{error}</p>}
      </div>
    );
    return (
      <div className="flex flex-col gap-3">
        {setup.qrDataUrl && <img src={setup.qrDataUrl} alt="TOTP QR code" className="h-56 w-56 self-center" />}
        <p className="break-all rounded-md bg-zinc-100 p-2 text-xs dark:bg-zinc-800">{setup.secret}</p>
        <form className="flex flex-col gap-3" onSubmit={confirm}>
          <label className="flex flex-col gap-1">{t.twoFactorCode}
            <input className={input} inputMode="numeric" autoComplete="one-time-code" required value={code} onChange={(e) => setCode(e.target.value)} />
          </label>
          {error && <p role="alert" className="text-red-600">{error}</p>}
          <button type="submit" className={primary} disabled={busy}>{t.twoFactorConfirm}</button>
        </form>
      </div>
    );
  }
  return (
    <div className="flex flex-col gap-4">
      <p className="text-sm text-green-700 dark:text-green-400">{t.twoFactor}: enabled.</p>
      {state.admin ? <p className="text-sm text-zinc-500">{t.twoFactorAdminRequired}</p> : (
        <>
          <form className="flex flex-col gap-3" onSubmit={regenerate}>
            <label className="flex flex-col gap-1">{t.twoFactorCode}
              <input className={input} inputMode="numeric" autoComplete="one-time-code" required value={code} onChange={(e) => setCode(e.target.value)} />
            </label>
            <button type="submit" className={primary} disabled={busy}>{t.twoFactorRegenerate}</button>
          </form>
          <form className="flex flex-col gap-3 border-t border-zinc-200 pt-4 dark:border-zinc-800" onSubmit={disable}>
            <p className="text-sm text-zinc-500">{t.twoFactorDisableText}</p>
            <label className="flex flex-col gap-1">{t.currentPassword}
              <input className={input} type="password" required value={password} onChange={(e) => setPassword(e.target.value)} />
            </label>
            <label className="flex flex-col gap-1">{t.twoFactorCode}
              <input className={input} required value={code} onChange={(e) => setCode(e.target.value)} />
            </label>
            <button type="submit" className="self-start rounded-md border border-red-300 px-3 py-1.5 text-sm text-red-700 hover:bg-red-50 dark:border-red-900 dark:text-red-300" disabled={busy}>{t.twoFactorDisable}</button>
          </form>
        </>
      )}
      {error && <p role="alert" className="text-red-600">{error}</p>}
    </div>
  );
}
