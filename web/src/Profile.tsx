import { useEffect, useId, useState, type FormEvent } from "react";
import {
  Button,
  CodeSnippet,
  Form,
  Layer,
  PasswordInput,
  Select,
  SelectItem,
  SkeletonText,
  Tab,
  TabList,
  TabPanel,
  TabPanels,
  Tabs,
  Tag,
  TextInput,
} from "@carbon/react";
import { Document, DocumentPdf } from "@carbon/icons-react";
import { ApiError, profileApi, retryAfter, type TwoFactorSetup as TwoFactorSetupState } from "./api";
import { applySettings, canonical, languages, lang, messagesFor, needsReload, settings, t, type Settings } from "./i18n";
import { ApiKeySection } from "./ApiKeys";
import { AppModal } from "./DangerModal";
import { Loading } from "./lazyView";
import { FormError, Success } from "./notices";
import { PasskeySection } from "./Passkeys";
import { recoveryFileName, recoveryPdf, recoveryText, saveFile, type RecoverySheet } from "./recoveryCodes";

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
  const id = useId();

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

  const password = { showPasswordLabel: t.carbon.showPassword, hidePasswordLabel: t.carbon.hidePassword, required: true };
  return (
    <Form className="profile-form" onSubmit={(e) => void submit(e)}>
      <PasswordInput
        id={`${id}-current`}
        labelText={t.currentPassword}
        autoComplete="current-password"
        value={current}
        onChange={(e) => setCurrent(e.target.value)}
        {...password}
      />
      <PasswordInput
        id={`${id}-new`}
        labelText={t.newPassword}
        helperText={t.passwordRules}
        autoComplete="new-password"
        value={next}
        onChange={(e) => setNext(e.target.value)}
        {...password}
      />
      <PasswordInput
        id={`${id}-repeat`}
        labelText={t.repeatPassword}
        autoComplete="new-password"
        value={repeat}
        onChange={(e) => setRepeat(e.target.value)}
        {...password}
      />
      <FormError text={error} />
      <div>
        <Button type="submit" disabled={busy}>
          {t.changePassword}
        </Button>
      </div>
    </Form>
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
  const id = useId();
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
    <div className="profile-form">
      <Select
        id={`${id}-language`}
        labelText={t.language}
        value={value.language}
        disabled={pending !== null}
        onChange={(e) => void change("language", e.target.value)}
      >
        <SelectItem value="auto" text={t.settingAuto} />
        {languages.map((l) => (
          <SelectItem key={l} value={l} lang={l} text={messagesFor(l).languageName} />
        ))}
      </Select>
      <Select
        id={`${id}-formats`}
        labelText={t.formats}
        helperText={t.formatsHint}
        value={value.locale}
        disabled={pending !== null}
        onChange={(e) => void change("locale", e.target.value)}
      >
        <SelectItem value="auto" text={t.settingAuto} />
        {formats.map((f) => (
          <SelectItem key={f} value={f} text={formatLabel(f)} />
        ))}
      </Select>
      <FormError text={error} />
    </div>
  );
}

type ProfileTab = "display" | "security" | "apiKeys";
const tabs: ProfileTab[] = ["display", "security", "apiKeys"];

export function ProfilePage({ name }: { name: string }) {
  const [changed, setChanged] = useState(false);
  const [tab, setTab] = useState<ProfileTab>("display");
  // Counts 2FA changes, so the sections that ask for a code read the status again.
  const [twoFactorVersion, setTwoFactorVersion] = useState(0);
  const twoFactorChanged = () => setTwoFactorVersion((v) => v + 1);
  return (
    <div className="accounts-page">
      <div className="page">
        <header className="page-header">
          <h1 className="page-header__title type-heading-04">{t.profile}</h1>
          <p className="page-header__intro app-text-secondary">{t.loggedInAs(name)}</p>
        </header>
        {/* Every panel stays mounted (Carbon hides the inactive ones), so a
            started 2FA setup or a new key survives a switch of tabs. */}
        <Tabs selectedIndex={tabs.indexOf(tab)} onChange={({ selectedIndex }) => setTab(tabs[selectedIndex] ?? "display")}>
          <TabList
            aria-label={t.profileSections}
            contained
            leftOverflowButtonProps={{ "aria-label": t.carbon.scrollLeft }}
            rightOverflowButtonProps={{ "aria-label": t.carbon.scrollRight }}
          >
            <Tab>{t.tabDisplay}</Tab>
            <Tab>{t.tabSecurity}</Tab>
            <Tab>{t.apiKeys}</Tab>
          </TabList>
          {/* A contained tab's panel is a layer of its own: the fields in it sit on the next one. */}
          <TabPanels>
            <TabPanel className="profile-panel">
              <Layer>
                <DisplaySettings />
              </Layer>
            </TabPanel>
            <TabPanel className="profile-panel">
              <Layer>
                <section className="page__section">
                  <h2 className="type-heading-03">{t.changePassword}</h2>
                  {changed && <Success text={t.passwordChanged} />}
                  <PasswordForm done={() => setChanged(true)} />
                </section>
                <section className="page__section">
                  <h2 className="type-heading-03">{t.twoFactor}</h2>
                  <TwoFactorSetup user={name} status onChange={twoFactorChanged} />
                </section>
                <section className="page__section">
                  <h2 className="type-heading-03">{t.passkeys}</h2>
                  <PasskeySection twoFactorVersion={twoFactorVersion} />
                </section>
              </Layer>
            </TabPanel>
            <TabPanel className="profile-panel">
              <Layer>
                <ApiKeySection twoFactorVersion={twoFactorVersion} />
              </Layer>
            </TabPanel>
          </TabPanels>
        </Tabs>
      </div>
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
 * The new recovery codes, shown once in a Modal, with downloads made in the
 * browser. Only "I have saved the codes" leaves it: Escape, a click outside
 * and the (hidden) close button do nothing.
 */
export function RecoveryCodes({ sheet, done }: { sheet: RecoverySheet; done: () => void }) {
  return (
    <AppModal
      size="sm"
      className="recovery-modal"
      modalHeading={t.twoFactorRecovery}
      primaryButtonText={t.twoFactorCodesSaved}
      preventCloseOnClickOutside
      onRequestClose={() => undefined}
      onRequestSubmit={done}
    >
      <div className="modal-stack">
        <p>{t.twoFactorRecoveryText}</p>
        <CodeSnippet
          type="multi"
          className="recovery-modal__codes"
          copyText={sheet.codes.join("\n")}
          aria-label={t.twoFactorRecovery}
          copyButtonDescription={t.carbon.copy}
          feedback={t.carbon.copied}
          showMoreText={t.carbon.showMore}
          showLessText={t.carbon.showLess}
        >
          {sheet.codes.join("\n")}
        </CodeSnippet>
        <div className="profile-actions">
          <Button
            kind="tertiary"
            size="md"
            renderIcon={DocumentPdf}
            onClick={() => saveFile(recoveryPdf(sheet), recoveryFileName(sheet.user, sheet.createdAt, "pdf"), "application/pdf")}
          >
            {t.twoFactorDownloadPdf}
          </Button>
          <Button
            kind="tertiary"
            size="md"
            renderIcon={Document}
            onClick={() => saveFile(recoveryText(sheet), recoveryFileName(sheet.user, sheet.createdAt, "txt"), "text/plain;charset=utf-8")}
          >
            {t.twoFactorDownloadTxt}
          </Button>
        </div>
      </div>
    </AppModal>
  );
}

/**
 * Turns two-factor authentication on or off. onChange is called with the new
 * state after confirming or disabling; onDone after the recovery codes of a
 * new setup were saved. status shows a tag with the current state.
 */
export function TwoFactorSetup({
  user,
  status = false,
  onDone,
  onChange,
}: {
  user: string;
  status?: boolean;
  onDone?: () => void;
  onChange?: (enabled: boolean) => void;
}) {
  const [state, setState] = useState<TwoFactorSetupState | null>(null);
  const [setup, setSetup] = useState<TwoFactorSetupState | null>(null);
  const [recovery, setRecovery] = useState<RecoverySheet | null>(null);
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const id = useId();
  // One primary per view: in the profile's Security tab (status) the password
  // form's Save is the primary; on the forced setup screen this is the action.
  const actionKind = status ? "tertiary" : "primary";

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

  if (!state)
    return error ? (
      <FormError text={error} />
    ) : (
      <Loading>
        <SkeletonText paragraph lineCount={3} />
      </Loading>
    );
  const tag = status && (
    <div>
      <Tag size="md" type={state.enabled ? "green" : "gray"}>
        {state.enabled ? t.twoFactorStateOn : t.twoFactorStateOff}
      </Tag>
    </div>
  );
  // The codes sit in a Modal above the page; the section below already
  // shows the new state.
  const codes = recovery && <RecoveryCodes sheet={recovery} done={() => { setRecovery(null); onDone?.(); }} />;
  const codeInput = (suffix: string, recoveryCodes: boolean) => (
    <TextInput
      id={`${id}-${suffix}`}
      labelText={t.twoFactorCode}
      required
      autoComplete="one-time-code"
      {...(recoveryCodes ? { autoCapitalize: "none", spellCheck: false } : { inputMode: "numeric" as const })}
      value={code}
      onChange={(e) => setCode(e.target.value)}
    />
  );

  if (!state.enabled) {
    if (!setup) return (
      <div className="profile-form">
        {codes}
        {tag}
        <p className="app-text-secondary">{t.twoFactorSetupText}</p>
        <div>
          <Button kind={actionKind} onClick={() => void start()} disabled={busy}>{t.twoFactorSetup}</Button>
        </div>
        <FormError text={error} />
      </div>
    );
    return (
      <div className="profile-form">
        {codes}
        {tag}
        {setup.qrDataUrl && <img src={setup.qrDataUrl} alt={t.twoFactorQR} className="two-factor__qr" />}
        <p className="app-text-secondary">{t.twoFactorSetupText}</p>
        <p className="app-text-secondary">{t.twoFactorManualKey}</p>
        <CodeSnippet
          type="single"
          className="two-factor__key"
          copyText={setup.secret}
          aria-label={t.twoFactorManualKey}
          copyButtonDescription={t.carbon.copy}
          feedback={t.carbon.copied}
        >
          {setup.secret}
        </CodeSnippet>
        <Form className="profile-form" onSubmit={(e) => void confirm(e)}>
          {codeInput("confirm", false)}
          <FormError text={error} />
          <div>
            <Button type="submit" kind={actionKind} disabled={busy}>{t.twoFactorConfirm}</Button>
          </div>
        </Form>
      </div>
    );
  }
  return (
    <div className="profile-form">
      {codes}
      {tag}
      {state.admin ? <p className="app-text-secondary">{t.twoFactorAdminRequired}</p> : (
        <>
          <Form className="profile-form" onSubmit={(e) => void regenerate(e)}>
            {codeInput("regenerate", false)}
            <div>
              <Button type="submit" kind="secondary" disabled={busy}>{t.twoFactorRegenerate}</Button>
            </div>
          </Form>
          <Form className="profile-form profile-form--divided" onSubmit={(e) => void disable(e)}>
            <p className="app-text-secondary">{t.twoFactorDisableText}</p>
            <PasswordInput
              id={`${id}-password`}
              labelText={t.currentPassword}
              showPasswordLabel={t.carbon.showPassword}
              hidePasswordLabel={t.carbon.hidePassword}
              required
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
            {codeInput("disable", true)}
            <div>
              <Button type="submit" kind="danger--tertiary" disabled={busy}>{t.twoFactorDisable}</Button>
            </div>
          </Form>
        </>
      )}
      <FormError text={error} />
    </div>
  );
}
