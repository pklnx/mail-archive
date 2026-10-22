import { useEffect, useState, type FormEvent } from "react";
import { ApiError, profileApi, retryAfter, type TwoFactorSetup as TwoFactorSetupState } from "./api";
import { applySettings, canonical, languages, lang, messagesFor, needsReload, settings, t, type Settings } from "./i18n";
import { ApiKeySection } from "./ApiKeys";
import { PageHeader } from "./PageHeader";
import { PasskeySection } from "./Passkeys";
import { Tabs } from "./Tabs";
import { recoveryFileName, recoveryPdf, recoveryText, saveFile, type RecoverySheet } from "./recoveryCodes";

const primary = "rounded-md bg-accent px-3 py-1.5 text-sm font-medium text-on-accent hover:bg-accent-hover disabled:opacity-50 pointer-coarse:min-h-11";
const secondary =
  "rounded-md border border-line-control px-2.5 py-1 text-sm hover:bg-surface-hover pointer-coarse:min-h-11";
const input =
  "w-full rounded-md border border-line-control bg-field px-2.5 py-1.5 text-sm pointer-coarse:min-h-11";

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
      <p className="text-ink-muted">{t.passwordRules}</p>
      {error && (
        <p role="alert" className="text-danger">
          {error}
        </p>
      )}
      <button type="submit" className={`${primary} self-start`} disabled={busy}>
        {t.changePassword}
      </button>
    </form>
  );
}

/** The formats offered besides "auto"; a stored tag outside the list is shown too. */
export const formatChoices = ["de-DE", "de-AT", "de-CH", "en-GB", "en-IE", "en-US"];

const sampleDate = new Date(2026, 9, 12);

/** A format option: its name in the UI language, a date and a number. */
export function formatLabel(tag: string, uiLang: string = lang): string {
  let name = tag;
  try {
    name = new Intl.DisplayNames([uiLang], { type: "language" }).of(tag) ?? tag;
  } catch {
    // no Intl.DisplayNames in some older WebViews: the tag will do
  }
  const date = new Intl.DateTimeFormat(tag, { dateStyle: "medium" }).format(sampleDate);
  const number = (1234.5).toLocaleString(tag);
  return `${name} · ${date} · ${number}`;
}

/** The setting as the selects show it: a value the UI cannot use shows as "auto". */
function shown(s: Settings): Settings {
  return {
    language: languages.some((l) => l === s.language) ? s.language : "auto",
    locale: (s.locale !== "auto" && canonical(s.locale)) || "auto",
  };
}

/**
 * The language and formats of the UI, stored for the user on the server.
 * A change that alters the texts or formats on screen reloads the page.
 */
export function DisplaySettings({ reload = () => location.reload() }: { reload?: () => void }) {
  const [current, setCurrent] = useState<Settings>(settings);
  const [pending, setPending] = useState<Settings | null>(null);
  const [error, setError] = useState("");
  const value = shown(pending ?? current);
  const formats = value.locale === "auto" || formatChoices.includes(value.locale) ? formatChoices : [...formatChoices, value.locale];

  const change = async (field: keyof Settings, v: string) => {
    const next = { ...current, [field]: v };
    setError("");
    setPending(next);
    try {
      await profileApi.update({ [field]: v });
      const reloading = needsReload(next, current);
      applySettings(next);
      setCurrent(next);
      if (reloading) reload();
    } catch (err) {
      setError(t.failed(err instanceof Error ? err.message : String(err)));
    } finally {
      setPending(null);
    }
  };

  return (
    <div className="flex flex-col gap-3 text-sm">
      <label className="flex flex-col gap-1">
        {t.language}
        <select className={input} value={value.language} disabled={pending !== null} onChange={(e) => void change("language", e.target.value)}>
          <option value="auto">{t.settingAuto}</option>
          {languages.map((l) => (
            <option key={l} value={l} lang={l}>
              {messagesFor(l).languageName}
            </option>
          ))}
        </select>
      </label>
      <label className="flex flex-col gap-1">
        {t.formats}
        <select className={input} value={value.locale} disabled={pending !== null} onChange={(e) => void change("locale", e.target.value)}>
          <option value="auto">{t.settingAuto}</option>
          {formats.map((f) => (
            <option key={f} value={f}>
              {formatLabel(f)}
            </option>
          ))}
        </select>
      </label>
      <p className="text-ink-muted">{t.formatsHint}</p>
      {error && (
        <p role="alert" className="text-danger">
          {error}
        </p>
      )}
    </div>
  );
}

type ProfileTab = "display" | "security" | "apiKeys";

export function ProfilePage({ name, close }: { name: string; close: () => void }) {
  const [changed, setChanged] = useState(false);
  const [tab, setTab] = useState<ProfileTab>("display");
  // Counts 2FA changes, so the sections that ask for a code read the status again.
  const [twoFactorVersion, setTwoFactorVersion] = useState(0);
  const twoFactorChanged = () => setTwoFactorVersion((v) => v + 1);
  return (
    <div className="mx-auto flex h-full max-w-md flex-col gap-4 overflow-y-auto p-4">
      <PageHeader title={t.profile} close={close} />
      <p className="text-sm text-ink-muted">{t.loggedInAs(name)}</p>
      <Tabs
        label={t.profileSections}
        selected={tab}
        onSelect={setTab}
        tabs={[
          { id: "display", label: t.tabDisplay, panel: <DisplaySettings /> },
          {
            id: "security",
            label: t.tabSecurity,
            panel: (
              <>
                <h2 className="font-semibold">{t.changePassword}</h2>
                {changed && (
                  <p role="status" className="text-sm text-ok">
                    {t.passwordChanged}
                  </p>
                )}
                <PasswordForm done={() => setChanged(true)} />
                <h2 className="font-semibold">{t.twoFactor}</h2>
                <TwoFactorSetup user={name} onChange={twoFactorChanged} />
                <h2 className="font-semibold">{t.passkeys}</h2>
                <PasskeySection twoFactorVersion={twoFactorVersion} />
              </>
            ),
          },
          { id: "apiKeys", label: t.apiKeys, panel: <ApiKeySection twoFactorVersion={twoFactorVersion} /> },
        ]}
      />
    </div>
  );
}

function twoFactorError(err: unknown): string {
  const wait = retryAfter(err);
  if (wait !== null) return t.tooManyAttempts(wait);
  if (err instanceof ApiError && err.status === 401) return err.message.includes("password") ? t.wrongCurrentPassword : t.invalidTwoFactor;
  return t.failed(err instanceof Error ? err.message : String(err));
}

/**
 * The new recovery codes, shown once, with downloads made in the browser.
 * Only "I have saved the codes" leaves the view.
 */
export function RecoveryCodes({ sheet, done }: { sheet: RecoverySheet; done: () => void }) {
  return (
    <div className="flex flex-col gap-3">
      <h3 className="font-semibold">{t.twoFactorRecovery}</h3>
      <p className="text-sm text-ink-muted">{t.twoFactorRecoveryText}</p>
      <pre className="rounded-md bg-surface-hover p-3 text-xs">{sheet.codes.join("\n")}</pre>
      <div className="flex flex-wrap gap-2">
        <button type="button" className={secondary}
          onClick={() => saveFile(recoveryPdf(sheet), recoveryFileName(sheet.user, sheet.createdAt, "pdf"), "application/pdf")}>
          {t.twoFactorDownloadPdf}
        </button>
        <button type="button" className={secondary}
          onClick={() => saveFile(recoveryText(sheet), recoveryFileName(sheet.user, sheet.createdAt, "txt"), "text/plain;charset=utf-8")}>
          {t.twoFactorDownloadTxt}
        </button>
      </div>
      <button type="button" className={`${primary} self-start`} onClick={done}>{t.twoFactorCodesSaved}</button>
    </div>
  );
}

/**
 * Turns two-factor authentication on or off. onChange is called with the new
 * state after confirming or disabling; onDone after the recovery codes of a
 * new setup were saved.
 */
export function TwoFactorSetup({ user, onDone, onChange }: { user: string; onDone?: () => void; onChange?: (enabled: boolean) => void }) {
  const [state, setState] = useState<TwoFactorSetupState | null>(null);
  const [setup, setSetup] = useState<TwoFactorSetupState | null>(null);
  const [recovery, setRecovery] = useState<RecoverySheet | null>(null);
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  // Dated when the codes arrive, so every download shows the same time.
  const sheetFor = (codes: string[]): RecoverySheet => ({ user, origin: window.location.origin, createdAt: new Date(), codes });

  const load = async () => {
    try { setState(await profileApi.twoFactor.get()); }
    catch (err) { setError(twoFactorError(err)); }
  };
  useEffect(() => { void load(); }, []);

  const start = async () => {
    setBusy(true); setError("");
    try { setSetup((await profileApi.twoFactor.setup())!); }
    catch (err) { setError(twoFactorError(err)); }
    finally { setBusy(false); }
  };

  const confirm = async (e: FormEvent) => {
    e.preventDefault(); setBusy(true); setError("");
    try {
      const result = await profileApi.twoFactor.confirm(code);
      // onDone only after the recovery codes were shown (see below).
      setRecovery(sheetFor(result!.recoveryCodes));
      setSetup(null); setCode(""); setState((s) => s ? { ...s, enabled: true, setupPending: false } : s);
      onChange?.(true);
    } catch (err) { setError(twoFactorError(err)); }
    finally { setBusy(false); }
  };

  const disable = async (e: FormEvent) => {
    e.preventDefault(); setBusy(true); setError("");
    try {
      await profileApi.twoFactor.disable(password, code);
      setPassword(""); setCode(""); setState((s) => s ? { ...s, enabled: false } : s);
      onChange?.(false);
    } catch (err) { setError(twoFactorError(err)); }
    finally { setBusy(false); }
  };

  const regenerate = async (e: FormEvent) => {
    e.preventDefault(); setBusy(true); setError("");
    try {
      const result = await profileApi.twoFactor.regenerateRecoveryCodes(code);
      setRecovery(sheetFor(result!.recoveryCodes)); setCode("");
    } catch (err) { setError(twoFactorError(err)); }
    finally { setBusy(false); }
  };

  if (!state) return <p className="text-sm text-ink-muted">{t.loading}</p>;
  if (recovery) return <RecoveryCodes sheet={recovery} done={() => { setRecovery(null); onDone?.(); }} />;
  if (!state.enabled) {
    if (!setup) return (
      <div className="flex flex-col gap-3">
        <p className="text-sm text-ink-muted">{t.twoFactorSetupText}</p>
        <button type="button" className={primary} onClick={start} disabled={busy}>{t.twoFactorSetup}</button>
        {error && <p role="alert" className="text-danger">{error}</p>}
      </div>
    );
    return (
      <div className="flex flex-col gap-3">
        {setup.qrDataUrl && <img src={setup.qrDataUrl} alt={t.twoFactorQR} className="h-56 w-56 self-center rounded bg-white p-2" />}
        <p className="text-sm text-ink-muted">{t.twoFactorSetupText}</p>
        <p className="text-sm text-ink-muted">{t.twoFactorManualKey}</p>
        <code className="break-all rounded-md bg-surface-hover p-2 font-mono text-xs select-all">{setup.secret}</code>
        <form className="flex flex-col gap-3" onSubmit={confirm}>
          <label className="flex flex-col gap-1">{t.twoFactorCode}
            <input className={input} inputMode="numeric" autoComplete="one-time-code" required value={code} onChange={(e) => setCode(e.target.value)} />
          </label>
          {error && <p role="alert" className="text-danger">{error}</p>}
          <button type="submit" className={primary} disabled={busy}>{t.twoFactorConfirm}</button>
        </form>
      </div>
    );
  }
  return (
    <div className="flex flex-col gap-4">
      <p className="text-sm text-ok">{t.twoFactorOn}</p>
      {state.admin ? <p className="text-sm text-ink-muted">{t.twoFactorAdminRequired}</p> : (
        <>
          <form className="flex flex-col gap-3" onSubmit={regenerate}>
            <label className="flex flex-col gap-1">{t.twoFactorCode}
              <input className={input} inputMode="numeric" autoComplete="one-time-code" required value={code} onChange={(e) => setCode(e.target.value)} />
            </label>
            <button type="submit" className={primary} disabled={busy}>{t.twoFactorRegenerate}</button>
          </form>
          <form className="flex flex-col gap-3 border-t border-line pt-4" onSubmit={disable}>
            <p className="text-sm text-ink-muted">{t.twoFactorDisableText}</p>
            <label className="flex flex-col gap-1">{t.currentPassword}
              <input className={input} type="password" required value={password} onChange={(e) => setPassword(e.target.value)} />
            </label>
            <label className="flex flex-col gap-1">{t.twoFactorCode}
              <input className={input} required autoComplete="one-time-code" autoCapitalize="none" spellCheck={false} value={code} onChange={(e) => setCode(e.target.value)} />
            </label>
            <button type="submit" className="self-start rounded-md border border-danger-line px-3 py-1.5 text-sm text-danger hover:bg-danger-soft" disabled={busy}>{t.twoFactorDisable}</button>
          </form>
        </>
      )}
      {error && <p role="alert" className="text-danger">{error}</p>}
    </div>
  );
}
