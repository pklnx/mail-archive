import { useState, type FormEvent } from "react";
import { ApiError, profileApi, retryAfter } from "./api";
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
    </div>
  );
}
