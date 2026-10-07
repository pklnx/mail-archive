import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { ApiError, UNAUTHORIZED_EVENT, passkeyApi, retryAfter, sessionApi, unknownPasskey, type SessionState, type User } from "./api";
import { PasswordForm, TwoFactorSetup } from "./Profile";
import { ThemeToggle } from "./ThemeToggle";
import { t } from "./i18n";
import logo from "./logo.svg";
import { autofillSupported, browserError, credentialJSON, forgetPasskey, passkeysSupported, requestOptions } from "./webauthn";

const primary = "rounded-md bg-blue-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50";
const input =
  "w-full rounded-md border border-zinc-300 bg-white px-2.5 py-1.5 text-sm outline-none focus:border-blue-500 dark:border-zinc-700 dark:bg-zinc-900";

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
  const [state, setState] = useState<GateState>({ loading: true });

  const check = useCallback(() => {
    sessionApi.get().then(setState, (err: unknown) => setState({ error: err instanceof Error ? err.message : String(err) }));
  }, []);

  useEffect(() => {
    check();
    window.addEventListener(UNAUTHORIZED_EVENT, check);
    return () => window.removeEventListener(UNAUTHORIZED_EVENT, check);
  }, [check]);

  if ("loading" in state) return <Centered>{t.loading}</Centered>;
  if ("error" in state) {
    return (
      <Centered>
        <p className="text-red-600">{t.failed(state.error)}</p>
        <button type="button" className={primary} onClick={check}>
          {t.retry}
        </button>
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
          <h2 className="font-semibold">{t.chooseOwnPassword}</h2>
          <p>{t.chooseOwnPasswordText}</p>
          <PasswordForm done={() => setState({ user: { ...user, mustChangePassword: false } })} />
          <button type="button" className="self-start text-blue-600 hover:underline dark:text-blue-400" onClick={logout}>
            {t.logOut}
          </button>
        </Centered>
      );
    }
    if (state.user.twoFactorRequired && !state.user.twoFactorEnabled) {
      return (
        <Centered>
          <Brand />
          <h2 className="font-semibold">{t.twoFactorSetup}</h2>
          <p>{t.twoFactorRequired}</p>
          <TwoFactorSetup onDone={() => check()} />
          <button type="button" className="self-start text-blue-600 hover:underline dark:text-blue-400" onClick={logout}>{t.logOut}</button>
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
    <div className="relative flex h-full items-center justify-center p-4">
      <div className="absolute top-3 right-3">
        <ThemeToggle />
      </div>
      <div className="flex w-full max-w-sm flex-col gap-4 text-sm">{children}</div>
    </div>
  );
}

function Brand() {
  return (
    <div className="flex items-center gap-3">
      <img src={logo} alt="" width={40} height={40} />
      <h1 className="text-lg font-semibold">{t.appName}</h1>
    </div>
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
        <h2 className="font-semibold">{t.twoFactorLogin}</h2>
        <p>{t.twoFactorLoginText}</p>
        <form className="flex flex-col gap-3" onSubmit={async (e) => {
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
          <label className="flex flex-col gap-1">{t.twoFactorCode}
            {/* Text, not numeric: recovery codes contain letters. */}
            <input className={input} required autoFocus autoComplete="one-time-code" autoCapitalize="none" spellCheck={false} value={password} onChange={(e) => setPassword(e.target.value)} />
          </label>
          {error && <p role="alert" className="text-red-600">{error}</p>}
          <button type="submit" className={`${primary} mt-1`} disabled={busy}>{busy ? t.loggingIn : t.logIn}</button>
        </form>
      </Centered>
    );
  }
  return (
    <Centered>
      <Brand />
      <form className="flex flex-col gap-3" onSubmit={submit}>
        <label className="flex flex-col gap-1">
          {t.userName}
          <input
          className={input}
          required
          autoFocus
          autoCapitalize="none"
          autoComplete={passkeys ? "username webauthn" : "username"}
          spellCheck={false}
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          />
        </label>
        <label className="flex flex-col gap-1">
          {t.fieldPassword}
          <input
          className={input}
          type="password"
          required
          autoComplete="current-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          />
        </label>
        {error && (
          <p role="alert" className="text-red-600">
            {error}
          </p>
        )}
        <button type="submit" className={`${primary} mt-1`} disabled={busy}>
          {busy ? t.loggingIn : t.logIn}
        </button>
      </form>
      {passkeys && (
        <>
          <div className="flex items-center gap-3 text-zinc-500">
            <span className="h-px flex-1 bg-zinc-200 dark:bg-zinc-800" />
            {t.or}
            <span className="h-px flex-1 bg-zinc-200 dark:bg-zinc-800" />
          </div>
          <button
            type="button"
            className="rounded-md border border-zinc-300 px-3 py-1.5 text-sm font-medium hover:bg-zinc-100 disabled:opacity-50 dark:border-zinc-700 dark:hover:bg-zinc-800"
            disabled={busy}
            onClick={() => {
              setError("");
              void passkeyLogin(false);
            }}
          >
            {t.passkeySignIn}
          </button>
        </>
      )}
    </Centered>
  );
}

function SetupNotice({ check }: { check: () => void }) {
  return (
    <Centered>
      <Brand />
      <h2 className="font-semibold">{t.setupTitle}</h2>
      <p>{t.setupText}</p>
      <pre className="overflow-x-auto rounded-md bg-zinc-100 px-3 py-2 dark:bg-zinc-800">./ma user add NAME --admin</pre>
      <button type="button" className={`${primary} self-start`} onClick={check}>
        {t.checkAgain}
      </button>
    </Centered>
  );
}
