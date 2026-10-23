import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Button, CodeSnippet, Form, InlineNotification, PasswordInput, TextInput } from "@carbon/react";
import { ApiError, UNAUTHORIZED_EVENT, passkeyApi, retryAfter, sessionApi, unknownPasskey, type SessionState, type User } from "./api";
import { PasswordForm, TwoFactorSetup } from "./Profile";
import { ThemeToggle } from "./ThemeToggle";
import { applySettings, t } from "./i18n";
import logo from "./logo.svg";
import { autofillSupported, browserError, credentialJSON, forgetPasskey, passkeysSupported, requestOptions } from "./webauthn";

type GateState = SessionState | { loading: true } | { error: string };

interface Props {
  /** Renders the app for a logged-in user. */
  children: (user: User, logout: () => void) => ReactNode;
}

/**
 * Shows the app only with a valid session. Without one it shows the login
 * page, or how to create the first admin. Any 401 from the API (the session
 * ended) brings the login page back; the URL is kept, so the user returns
 * to the same view after logging in.
 */
export function AuthGate({ children }: Props) {
  const [state, setGateState] = useState<GateState>({ loading: true });

  // The user's settings apply before the app renders: until then only
  // "Loading…" or the login page is on screen.
  const setState = useCallback((s: GateState) => {
    if ("user" in s && s.user) applySettings({ language: s.user.language, locale: s.user.locale });
    setGateState(s);
  }, []);

  const check = useCallback(() => {
    sessionApi.get().then(setState, (err: unknown) => setState({ error: err instanceof Error ? err.message : String(err) }));
  }, [setState]);

  useEffect(() => {
    check();
    window.addEventListener(UNAUTHORIZED_EVENT, check);
    return () => window.removeEventListener(UNAUTHORIZED_EVENT, check);
  }, [check]);

  if ("loading" in state) {
    return (
      <Centered>
        <p className="auth-page__text">{t.loading}</p>
      </Centered>
    );
  }
  if ("error" in state) {
    return (
      <Centered>
        <ErrorNotice text={t.failed(state.error)} />
        <div>
          <Button onClick={check}>{t.retry}</Button>
        </div>
      </Centered>
    );
  }
  if (state.user) {
    const logout = () => {
      sessionApi.logout().finally(() => setState({ user: null, setupRequired: false }));
    };
    if (state.user.mustChangePassword) {
      const user = state.user;
      return (
        <Centered>
          <Brand />
          <Heading title={t.chooseOwnPassword} text={t.chooseOwnPasswordText} />
          <div className="auth-page__form">
            <PasswordForm done={() => setState({ user: { ...user, mustChangePassword: false } })} />
          </div>
          <div>
            <Button kind="ghost" onClick={logout}>
              {t.logOut}
            </Button>
          </div>
        </Centered>
      );
    }
    if (state.user.twoFactorRequired && !state.user.twoFactorEnabled) {
      return (
        <Centered>
          <Brand />
          <Heading title={t.twoFactorSetup} text={t.twoFactorRequired} />
          <div className="auth-page__form">
            <TwoFactorSetup user={state.user.name} onDone={() => check()} />
          </div>
          <div>
            <Button kind="ghost" onClick={logout}>
              {t.logOut}
            </Button>
          </div>
        </Centered>
      );
    }
    return children(state.user, logout);
  }
  if (state.setupRequired) return <SetupNotice check={check} />;
  return <LoginPage done={(user) => setState({ user })} passkeyOrigin={state.passkeyOrigin} />;
}

function Centered({ children }: { children: ReactNode }) {
  return (
    <div className="auth-page">
      <div className="auth-page__theme">
        <ThemeToggle />
      </div>
      <main className="auth-page__column">{children}</main>
    </div>
  );
}

function Brand() {
  return (
    <div className="auth-page__brand">
      <img src={logo} alt="" width={40} height={40} />
      <h1 className="type-heading-04">{t.appName}</h1>
    </div>
  );
}

function Heading({ title, text }: { title: string; text: string }) {
  return (
    <div>
      <h2 className="type-heading-03">{title}</h2>
      <p className="auth-page__text">{text}</p>
    </div>
  );
}

/** An error on the login screens: announced, and not closable (the next try clears it). */
function ErrorNotice({ text, alert = false }: { text: string; alert?: boolean }) {
  return (
    <InlineNotification
      className="auth-page__wide"
      kind="error"
      lowContrast
      hideCloseButton
      role={alert ? "alert" : "status"}
      statusIconDescription={t.carbon.status.error}
      subtitle={text}
    />
  );
}

function loginError(err: unknown): string {
  const wait = retryAfter(err);
  if (wait !== null) return t.tooManyAttempts(wait);
  if (err instanceof ApiError && err.status === 401) return t.wrongLogin;
  if (err instanceof ApiError && err.status === 403 && err.message.includes("locked")) return t.userLocked;
  return t.failed(err instanceof Error ? err.message : String(err));
}

export function passkeyError(err: unknown): string {
  const wait = retryAfter(err);
  if (wait !== null) return t.tooManyAttempts(wait);
  const name = browserError(err);
  if (name === "NotAllowedError" || name === "AbortError") return t.passkeyCancelled;
  if (unknownPasskey(err)) return t.passkeyUnknown;
  if (err instanceof ApiError && err.status === 401) return err.message.includes("expired") ? t.passkeyExpired : t.passkeyFailed;
  if (err instanceof ApiError && err.status === 403 && err.message.includes("locked")) return t.userLocked;
  return t.failed(err instanceof Error ? err.message : String(err));
}

function LoginPage({ done, passkeyOrigin }: { done: (user: User) => void; passkeyOrigin?: string }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [challenge, setChallenge] = useState<string | null>(null);

  // Passkeys work only at the server's public URL.
  const passkeys = !!passkeyOrigin && passkeyOrigin === window.location.origin && passkeysSupported();
  // The running passkey request; a new one (or leaving the page) aborts it.
  const pending = useRef<AbortController | null>(null);
  const doneRef = useRef(done);
  doneRef.current = done;

  const passkeyLogin = useCallback(
    async (autofill: boolean): Promise<void> => {
      pending.current?.abort();
      const ctrl = new AbortController();
      pending.current = ctrl;
      let token: string;
      let cred: PublicKeyCredential | null;
      try {
        const ceremony = await passkeyApi.beginLogin();
        token = ceremony.token;
        cred = (await navigator.credentials.get({
          publicKey: requestOptions(ceremony.publicKey),
          signal: ctrl.signal,
          ...(autofill ? { mediation: "conditional" as CredentialMediationRequirement } : {}),
        })) as PublicKeyCredential | null;
      } catch (err) {
        // Autofill fails quietly; it is only an offer. After a failed
        // button press, offer it again.
        if (!ctrl.signal.aborted && !autofill) {
          setError(passkeyError(err));
          if (await autofillSupported()) void passkeyLogin(true);
        }
        return;
      }
      if (!cred || ctrl.signal.aborted) return;
      setBusy(true);
      setError("");
      try {
        doneRef.current(await passkeyApi.finishLogin(token, credentialJSON(cred)));
      } catch (err) {
        const unknown = unknownPasskey(err);
        if (unknown) await forgetPasskey(unknown.rpId, unknown.credentialId);
        setError(passkeyError(err));
        setBusy(false);
        // Offer the autofill again for the next try.
        if (await autofillSupported()) void passkeyLogin(true);
      }
    },
    [],
  );

  useEffect(() => {
    if (!passkeys || challenge) return;
    let stopped = false;
    void autofillSupported().then((ok) => {
      if (ok && !stopped) void passkeyLogin(true);
    });
    return () => {
      stopped = true;
      pending.current?.abort();
    };
  }, [passkeys, challenge, passkeyLogin]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const result = await sessionApi.login(username, password);
      if (result.user) done(result.user);
      else if (result.twoFactorRequired && result.challenge) {
        // Second step. The code field reuses the password state: clear it,
        // so the password is never shown in the visible code field.
        setChallenge(result.challenge);
        setPassword("");
        setBusy(false);
      }
      else throw new Error("invalid login response");
    } catch (err) {
      setError(loginError(err));
      setPassword("");
      setBusy(false);
    }
  };


  if (challenge) {
    return (
      <Centered>
        <Brand />
        <Heading title={t.twoFactorLogin} text={t.twoFactorLoginText} />
        <Form className="auth-page__form" onSubmit={async (e) => {
          e.preventDefault(); setBusy(true); setError("");
          try { done(await sessionApi.login2FA(challenge, password)); }
          catch (err) {
            if (err instanceof ApiError && err.status === 401 && err.message.includes("expired")) {
              setChallenge(null);
              setError(t.twoFactorExpired);
            } else {
              setError(err instanceof ApiError && err.status === 401 ? t.invalidTwoFactor : loginError(err));
            }
            setPassword("");
            setBusy(false);
          }
        }}>
          {/* Text, not numeric: recovery codes contain letters, so one field takes both. */}
          <TextInput
            id="login-code"
            labelText={t.twoFactorCode}
            required
            autoFocus
            autoComplete="one-time-code"
            autoCapitalize="none"
            spellCheck={false}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
          {error && <ErrorNotice text={error} alert />}
          <Button type="submit" className="auth-page__wide" disabled={busy}>
            {busy ? t.loggingIn : t.logIn}
          </Button>
        </Form>
      </Centered>
    );
  }
  return (
    <Centered>
      <Brand />
      <Form className="auth-page__form" onSubmit={submit}>
        <TextInput
          id="login-user"
          labelText={t.userName}
          required
          autoFocus
          autoCapitalize="none"
          autoComplete={passkeys ? "username webauthn" : "username"}
          spellCheck={false}
          value={username}
          onChange={(e) => setUsername(e.target.value)}
        />
        <PasswordInput
          id="login-password"
          labelText={t.fieldPassword}
          showPasswordLabel={t.carbon.showPassword}
          hidePasswordLabel={t.carbon.hidePassword}
          required
          autoComplete="current-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        {error && <ErrorNotice text={error} alert />}
        <Button type="submit" className="auth-page__wide" disabled={busy}>
          {busy ? t.loggingIn : t.logIn}
        </Button>
      </Form>
      {passkeys && (
        <>
          <p className="auth-page__or auth-page__text">{t.or}</p>
          <Button
            kind="tertiary"
            className="auth-page__wide"
            disabled={busy}
            onClick={() => {
              setError("");
              void passkeyLogin(false);
            }}
          >
            {t.passkeySignIn}
          </Button>
        </>
      )}
    </Centered>
  );
}

function SetupNotice({ check }: { check: () => void }) {
  return (
    <Centered>
      <Brand />
      <Heading title={t.setupTitle} text={t.setupText} />
      <CodeSnippet
        type="single"
        className="auth-page__wide"
        aria-label={t.carbon.copy}
        copyButtonDescription={t.carbon.copy}
        feedback={t.carbon.copied}
      >
        ./ma user add NAME --admin
      </CodeSnippet>
      <div>
        <Button onClick={check}>{t.checkAgain}</Button>
      </div>
    </Centered>
  );
}
