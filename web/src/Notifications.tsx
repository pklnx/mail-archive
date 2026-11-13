import { useId, useState, type FormEvent } from "react";
import { Button, Form, InlineNotification, Tag, TextInput } from "@carbon/react";
import { ApiError, retryAfter } from "./api";
import { lang, t } from "./i18n";
import { longDate } from "./format";
import { FormError, Success } from "./notices";
import { notificationsApi, type Notifications } from "./notificationsApi";
import { notifyText } from "./notifyText";

/** The message for a failed request of this section, by the server's code. */
export function notifyError(err: unknown): string {
  const n = notifyText();
  const wait = retryAfter(err);
  if (wait !== null) return n.tooManyCodes(wait);
  if (err instanceof ApiError) {
    switch (err.body.code) {
      case "wrong_code":
        return n.wrongCode;
      case "no_code":
      case "code_spent":
        return n.noCode;
      case "mail_failed":
        return n.mailFailed;
    }
    if (err.status === 400) return n.invalidEmail;
  }
  return t.failed(err instanceof Error ? err.message : String(err));
}

/**
 * Profile → Notifications (#397, part 5), for admins: one address for alert
 * mail, confirmed with a code mailed to it. state is what the server said
 * (available is true here: the tab is hidden otherwise).
 */
export function NotificationsSection({ initial }: { initial: Notifications }) {
  const n = notifyText();
  const [state, setState] = useState<Notifications>(initial);
  const [email, setEmail] = useState(initial.email ?? "");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [done, setDone] = useState("");
  const id = useId();
  const waiting = state.codeExpiresAt !== null && !state.confirmed;

  const run = async (f: () => Promise<Notifications | null>, success: string) => {
    setBusy(true);
    setError("");
    setDone("");
    try {
      const next = await f();
      if (next) setState(next);
      setDone(success);
      return true;
    } catch (err) {
      setError(notifyError(err));
      return false;
    } finally {
      setBusy(false);
    }
  };

  const sendCode = (e: FormEvent) => {
    e.preventDefault();
    const address = email.trim();
    void run(() => notificationsApi.set(address, lang), n.codeSentNow(address));
  };

  const confirm = async (e: FormEvent) => {
    e.preventDefault();
    if (await run(() => notificationsApi.confirm(code), n.confirmedNow)) setCode("");
  };

  const remove = async () => {
    const ok = await run(async () => {
      await notificationsApi.remove();
      return { ...state, email: null, confirmed: false, confirmedAt: null, bounced: false, codeExpiresAt: null };
    }, n.removed);
    if (ok) setEmail("");
  };

  return (
    <section className="page__section">
      <h2 className="type-heading-03">{n.title}</h2>
      <div className="profile-form">
        <p className="app-text-secondary">{n.intro}</p>
        {state.email !== null && (
          <div>
            <Tag size="md" type={state.confirmed ? "green" : "gray"}>
              {state.confirmed ? n.stateConfirmed : n.stateUnconfirmed}
            </Tag>
          </div>
        )}
        {state.bounced && (
          <InlineNotification
            className="notification--full"
            kind="warning"
            lowContrast
            hideCloseButton
            statusIconDescription={t.carbon.status.warning}
            subtitle={n.bounced}
          />
        )}
        {state.confirmed && state.email && <p>{n.confirmed(state.email)}</p>}
        <Form className="profile-form" onSubmit={sendCode}>
          <TextInput
            id={`${id}-email`}
            type="email"
            labelText={n.email}
            helperText={n.emailHelp}
            autoComplete="email"
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
          <div>
            <Button type="submit" kind={waiting ? "tertiary" : "primary"} disabled={busy || email.trim() === ""}>
              {waiting || state.email === email.trim() ? n.resend : n.sendCode}
            </Button>
          </div>
        </Form>
        {waiting && (
          <Form className="profile-form profile-form--divided" onSubmit={(e) => void confirm(e)}>
            <p className="app-text-secondary">{n.codeSent(state.email ?? "", longDate(state.codeExpiresAt))}</p>
            <TextInput
              id={`${id}-code`}
              labelText={n.code}
              required
              autoComplete="one-time-code"
              autoCapitalize="characters"
              spellCheck={false}
              maxLength={12}
              value={code}
              onChange={(e) => setCode(e.target.value)}
            />
            <div>
              <Button type="submit" kind="primary" disabled={busy || code.trim() === ""}>
                {n.confirm}
              </Button>
            </div>
          </Form>
        )}
        <FormError text={error} />
        {done && <Success text={done} />}
        {state.email !== null && (
          <div>
            <Button kind="danger--tertiary" size="md" disabled={busy} onClick={() => void remove()}>
              {n.remove}
            </Button>
          </div>
        )}
      </div>
    </section>
  );
}
