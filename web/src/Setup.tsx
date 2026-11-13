import { useState, type FormEvent } from "react";
import { Button, Form, InlineNotification, PasswordInput, TextInput } from "@carbon/react";
import { ApiError, retryAfter, sessionApi, type Session, type SetupState } from "./api";
import { t } from "./i18n";
import { Brand, Centered, ErrorNotice, Heading } from "./authLayout";
import { setupText, type SetupText } from "./setupText";

/** User names the server accepts (auth.ValidateUserName), after trimming and lowercasing. */
const userNamePattern = /^[a-z0-9._-]{1,64}$/;

interface Props {
  /** The token of the setup link (setupLink.ts). */
  token: string;
  /** The server's setup state; undefined from a server that does not say. */
  setup?: SetupState;
  /** The admin is created and logged in. */
  done: (s: Session) => void;
  /** The link is of no use: show the login page. */
  toLogin: () => void;
}

/**
 * The page of the setup link (#396): the first admin of a new instance
 * chooses a user name and password, once. The answer is a login; the admin
 * then sets up 2FA (AuthGate). An expired, used or unknown link says so
 * and offers the login page.
 */
export function SetupPage({ token, setup, done, toLogin }: Props) {
  const st = setupText();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [repeat, setRepeat] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // Set once the server refused the link itself: no form any more.
  const [refused, setRefused] = useState<string | null>(null);

  const linkProblem = refused ?? linkState(setup, st);
  if (linkProblem !== null) {
    return (
      <Centered>
        <Brand />
        <Heading title={t.setupTitle} />
        <InlineNotification
          className="auth-page__wide"
          kind={setup === "complete" && refused === null ? "info" : "error"}
          lowContrast
          hideCloseButton
          role="status"
          statusIconDescription={setup === "complete" && refused === null ? t.carbon.status.info : t.carbon.status.error}
          subtitle={linkProblem}
        />
        <div>
          <Button onClick={toLogin}>{st.toLogin}</Button>
        </div>
      </Centered>
    );
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError("");
    const name = username.trim().toLowerCase();
    if (!userNamePattern.test(name)) return setError(st.nameInvalid);
    if ([...password].length < 12) return setError(st.passwordShort);
    if (password !== repeat) return setError(st.passwordsDiffer);
    setBusy(true);
    try {
      done(await sessionApi.setup(token, name, password));
    } catch (err) {
      setBusy(false);
      const wait = retryAfter(err);
      if (wait !== null) return setError(t.tooManyAttempts(wait));
      if (err instanceof ApiError && err.status === 401) return setRefused(st.linkInvalid);
      if (err instanceof ApiError && err.status === 404) return setRefused(st.linkUsed);
      setError(t.failed(err instanceof Error ? err.message : String(err)));
    }
  };

  return (
    <Centered>
      <Brand />
      <Heading title={t.setupTitle} text={st.formText} />
      <Form className="auth-page__form" onSubmit={(e) => void submit(e)}>
        <TextInput
          id="setup-user"
          labelText={t.userName}
          helperText={st.nameRules}
          required
          autoFocus
          autoCapitalize="none"
          autoComplete="username"
          spellCheck={false}
          value={username}
          onChange={(e) => setUsername(e.target.value)}
        />
        <PasswordInput
          id="setup-password"
          labelText={t.fieldPassword}
          helperText={t.passwordRules}
          showPasswordLabel={t.carbon.showPassword}
          hidePasswordLabel={t.carbon.hidePassword}
          required
          autoComplete="new-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        <PasswordInput
          id="setup-repeat"
          labelText={st.repeatPassword}
          showPasswordLabel={t.carbon.showPassword}
          hidePasswordLabel={t.carbon.hidePassword}
          required
          autoComplete="new-password"
          value={repeat}
          onChange={(e) => setRepeat(e.target.value)}
        />
        {error && <ErrorNotice text={error} alert />}
        <Button type="submit" className="auth-page__wide" disabled={busy}>
          {busy ? st.creating : st.create}
        </Button>
      </Form>
    </Centered>
  );
}

/** Why the link is of no use before anything is sent, or null when the form shows. */
function linkState(setup: SetupState | undefined, st: SetupText): string | null {
  switch (setup) {
    case "expired":
      return st.linkExpired;
    case "complete":
      return st.linkUsed;
    case "cli":
      return st.linkUnknown;
  }
  return null;
}
