// The auditor's shell (#393 part 3): the grant choice after login, and the
// banner over every view with the case, its owner and its end, and the
// switch to another case. A lazy chunk (#288 budget): only auditors load it.
import { useId, useState, type FormEvent } from "react";
import { Button, Form, InlineNotification, RadioButton, RadioButtonGroup, Select, SelectItem } from "@carbon/react";
import type { Session, SessionGrant } from "./api";
import { auditorApi } from "./auditorApi";
import { auditorText, lastDay, periodText } from "./auditorText";
import { day } from "./format";
import { t } from "./i18n";
import { FormError } from "./notices";

const failure = (err: unknown) => t.failed(err instanceof Error ? err.message : String(err));

/**
 * The grant step at login: an auditor with several active grants picks the
 * one this session reads through. It sends nothing but that choice: until
 * it is made the server refuses every other request and records each
 * refusal (#441, coordinator note).
 */
export function GrantChoice({ grants, done, logout }: { grants: SessionGrant[]; done: (s: Session) => void; logout: () => void }) {
  const a = auditorText();
  const id = useId();
  const [chosen, setChosen] = useState(grants[0]?.id ?? 0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      done(await auditorApi.select(chosen));
    } catch (err) {
      setError(failure(err));
      setBusy(false);
    }
  };
  return (
    <>
      <div>
        <h2 className="type-heading-03">{a.chooseTitle}</h2>
        <p className="auth-page__text">{a.chooseText}</p>
      </div>
      <Form className="auth-page__form" onSubmit={(e) => void submit(e)}>
        <RadioButtonGroup
          name={`${id}-grant`}
          legendText={a.switchCase}
          orientation="vertical"
          valueSelected={String(chosen)}
          onChange={(v) => setChosen(Number(v))}
        >
          {grants.map((g) => (
            <RadioButton
              key={g.id}
              id={`${id}-${g.id}`}
              value={String(g.id)}
              labelText={
                <span className="app-lines">
                  <span>{a.caseOf(g.label, g.owner)}</span>
                  <span className="app-text-secondary app-text-small">
                    {[a.validRange(day(g.startsAt), lastDay(g.endsAt)), periodText(g)].filter(Boolean).join(" ")}
                  </span>
                </span>
              }
            />
          ))}
        </RadioButtonGroup>
        <FormError text={error} />
        <Button type="submit" className="auth-page__wide" disabled={busy || !chosen}>
          {a.chooseAction}
        </Button>
      </Form>
      <div>
        <Button kind="ghost" onClick={logout}>
          {t.logOut}
        </Button>
      </div>
    </>
  );
}

/**
 * The banner over every view of an auditor: which case, whose, until when,
 * and that every access is recorded. With several active grants a Select
 * switches the session to another one.
 */
export function AuditorBanner({ grant, grants, switched }: { grant: SessionGrant; grants: SessionGrant[]; switched: (s: Session) => void }) {
  const a = auditorText();
  const id = useId();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const change = async (to: number) => {
    setBusy(true);
    setError("");
    try {
      switched(await auditorApi.select(to));
    } catch (err) {
      setError(failure(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="auditor-banner">
      <InlineNotification
        kind="info"
        lowContrast
        hideCloseButton
        role="status"
        statusIconDescription={t.carbon.status.info}
        title={a.bannerTitle}
        subtitle={[a.bannerText(grant.label, grant.owner, lastDay(grant.endsAt)), periodText(grant)].filter(Boolean).join(" ")}
      />
      {grants.length > 1 && (
        <Select
          id={`${id}-case`}
          className="auditor-banner__switch"
          size="sm"
          labelText={a.switchCase}
          value={String(grant.id)}
          disabled={busy}
          onChange={(e) => void change(Number(e.target.value))}
        >
          {grants.map((g) => (
            <SelectItem key={g.id} value={String(g.id)} text={a.caseOf(g.label, g.owner)} />
          ))}
        </Select>
      )}
      <FormError text={error} />
    </div>
  );
}
