import { useCallback, useEffect, useId, useState, type FormEvent } from "react";
import {
  Button,
  Checkbox,
  CheckboxGroup,
  CodeSnippet,
  Form,
  PasswordInput,
  Select,
  SelectItem,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableHeader,
  TableRow,
  Tag,
  TextInput,
  Tile,
} from "@carbon/react";
import { ApiError, apiKeyApi, profileApi, retryAfter, type ApiKey, type ApiKeyList, type ApiKeyScope, type NewApiKey } from "./api";
import { DangerModal } from "./DangerModal";
import { relativeTime, shortDate } from "./format";
import { t } from "./i18n";
import { FormError } from "./notices";

/** The expiry choices, in days; null is no expiry. */
export const expiryChoices: (number | null)[] = [30, 90, 365, null];

/** The message for a failed key creation. */
export function apiKeyError(err: unknown, max: number): string {
  const wait = retryAfter(err);
  if (wait !== null) return t.tooManyAttempts(wait);
  if (err instanceof ApiError) {
    if (err.status === 401) return err.message.includes("password") ? t.wrongCurrentPassword : t.invalidTwoFactor;
    if (err.status === 409 && err.message.includes("name")) return t.apiKeyNameTaken;
    if (err.status === 409) return t.apiKeyLimit(max);
  }
  return t.failed(err instanceof Error ? err.message : String(err));
}

/** One line under a key's name: state, expiry and last use. */
export function apiKeyDetails(k: ApiKey): string {
  const parts = [`ma_…${k.hint}`, k.scopes.join(", ")];
  if (k.state !== "active") parts.push(t.apiKeyState(k.state));
  else if (k.expiresAt) parts.push(t.apiKeyExpires(shortDate(k.expiresAt)));
  parts.push(k.lastUsedAt ? t.apiKeyUsed(relativeTime(k.lastUsedAt)) : t.apiKeyNeverUsed);
  return parts.join(" · ");
}

/** Asks before a key is revoked, in a danger Modal. */
function RevokeKeyModal({ apiKey, cancel, confirm }: { apiKey: ApiKey; cancel: () => void; confirm: () => void }) {
  return (
    <DangerModal
      size="xs"
      modalHeading={t.apiKeyRevokeTitle(apiKey.name)}
      primaryButtonText={t.apiKeyRevoke}
      secondaryButtonText={t.cancel}
      onRequestClose={cancel}
      onRequestSubmit={confirm}
    >
      <p>{t.apiKeyRevokeText}</p>
    </DangerModal>
  );
}

/** The revoke confirmation for a list or table of keys: which key is asked about. */
function useRevoke(revoke: (k: ApiKey) => void) {
  const [asking, setAsking] = useState<ApiKey | null>(null);
  const modal = asking && (
    <RevokeKeyModal
      apiKey={asking}
      cancel={() => setAsking(null)}
      confirm={() => {
        const k = asking;
        setAsking(null);
        revoke(k);
      }}
    />
  );
  return { ask: setAsking, modal };
}

/**
 * A list of keys with a revoke button for the active ones; revoking asks
 * in a danger Modal first.
 */
export function ApiKeyItems({ keys, revoke }: { keys: ApiKey[]; revoke: (k: ApiKey) => void }) {
  const { ask, modal } = useRevoke(revoke);
  return (
    <>
      <ul className="api-key-list">
        {keys.map((k) => (
          <li key={k.id} className={`api-key-list__item${k.state === "active" ? "" : " api-key-list__item--off"}`}>
            <div className="api-key-list__text">
              <span className="api-key-list__name">{k.name}</span>
              <span className="app-text-secondary app-text-small app-break-words">{apiKeyDetails(k)}</span>
            </div>
            {k.state === "active" && (
              <Button kind="danger--ghost" size="sm" onClick={() => ask(k)}>
                {t.apiKeyRevoke}
              </Button>
            )}
          </li>
        ))}
      </ul>
      {modal}
    </>
  );
}

const stateTag: Record<string, "green" | "gray" | "red"> = { active: "green", expired: "gray", revoked: "red" };

/**
 * The user's own keys on the profile, as a table: scopes, state, expiry and
 * last use, and a revoke button for the active ones (asks first).
 */
export function ApiKeyTable({ keys, revoke }: { keys: ApiKey[]; revoke: (k: ApiKey) => void }) {
  const { ask, modal } = useRevoke(revoke);
  return (
    <>
      <TableContainer className="profile-table">
        <Table aria-label={t.apiKeys} size="md">
          <TableHead>
            <TableRow>
              <TableHeader>{t.colName}</TableHeader>
              <TableHeader>{t.apiKeyScopes}</TableHeader>
              <TableHeader>{t.colState}</TableHeader>
              <TableHeader>{t.apiKeyExpiry}</TableHeader>
              <TableHeader>{t.colLastUsed}</TableHeader>
              <TableHeader>
                <span className="cds--visually-hidden">{t.colActions}</span>
              </TableHeader>
            </TableRow>
          </TableHead>
          <TableBody>
            {keys.map((k) => (
              <TableRow key={k.id} className={k.state === "active" ? undefined : "profile-table__row--off"}>
                <TableCell>
                  <span className="profile-table__name">{k.name}</span>
                  <span className="profile-table__hint app-text-secondary app-text-small">ma_…{k.hint}</span>
                </TableCell>
                <TableCell>{k.scopes.join(", ")}</TableCell>
                <TableCell>
                  <Tag size="sm" type={stateTag[k.state] ?? "gray"}>
                    {t.apiKeyState(k.state)}
                  </Tag>
                </TableCell>
                <TableCell>{k.expiresAt ? shortDate(k.expiresAt) : t.apiKeyExpiryNever}</TableCell>
                <TableCell>{k.lastUsedAt ? relativeTime(k.lastUsedAt) : t.apiKeyNeverUsed}</TableCell>
                <TableCell className="profile-table__action">
                  {k.state === "active" && (
                    <Button kind="danger--ghost" size="sm" onClick={() => ask(k)}>
                      {t.apiKeyRevoke}
                    </Button>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableContainer>
      {modal}
    </>
  );
}

/** Shows a new key once, with a copy button. */
export function NewKeyPanel({ created, done }: { created: NewApiKey; done: () => void }) {
  return (
    <Tile role="status" className="generated">
      <p className="generated__title">{t.apiKeyCreated(created.name)}</p>
      <CodeSnippet
        type="single"
        className="generated__code"
        copyText={created.key}
        aria-label={t.carbon.copy}
        copyButtonDescription={t.carbon.copy}
        feedback={t.carbon.copied}
      >
        {created.key}
      </CodeSnippet>
      <div>
        <Button kind="primary" size="md" onClick={done}>
          {t.done}
        </Button>
      </div>
    </Tile>
  );
}

/**
 * The user's API keys on the profile page: list, create, revoke.
 * twoFactorVersion changes when two-factor authentication was turned on or
 * off elsewhere on the page; the section then asks the server again whether
 * a code is needed.
 */
export function ApiKeySection({ twoFactorVersion = 0 }: { twoFactorVersion?: number }) {
  const [list, setList] = useState<ApiKeyList | null>(null);
  const [twoFactor, setTwoFactor] = useState(false);
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<ApiKeyScope[]>(["read"]);
  const [expiry, setExpiry] = useState<number | null>(90);
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [created, setCreated] = useState<NewApiKey | null>(null);
  const id = useId();

  const load = useCallback(() => {
    apiKeyApi.list().then(setList, (err: unknown) => setError(t.failed(err instanceof Error ? err.message : String(err))));
  }, []);
  useEffect(load, [load]);
  useEffect(() => {
    let current = true;
    profileApi.twoFactor.get().then((s) => current && setTwoFactor(s.enabled), () => undefined);
    return () => {
      current = false;
    };
  }, [twoFactorVersion]);

  if (!list) return error ? <FormError text={error} /> : <p className="app-text-secondary">{t.loading}</p>;

  const active = list.keys.filter((k) => k.state === "active").length;
  const toggle = (s: ApiKeyScope, on: boolean) => setScopes((cur) => (on ? [...cur.filter((x) => x !== s), s] : cur.filter((x) => x !== s)));

  const create = async (e: FormEvent) => {
    e.preventDefault();
    if (scopes.length === 0) {
      setError(t.apiKeyNoScope);
      return;
    }
    setBusy(true);
    setError("");
    setCreated(null);
    try {
      setCreated(await apiKeyApi.create({ name: name.trim(), scopes, expiresInDays: expiry, currentPassword: password, code }));
      setName("");
      load();
    } catch (err) {
      setError(apiKeyError(err, list.max));
    } finally {
      setPassword("");
      setCode("");
      setBusy(false);
    }
  };

  const revoke = async (k: ApiKey) => {
    setError("");
    try {
      await apiKeyApi.revoke(k.id);
    } catch (err) {
      setError(t.failed(err instanceof Error ? err.message : String(err)));
    }
    load();
  };

  return (
    <div className="profile-form">
      <p className="app-text-secondary">{t.apiKeysText}</p>
      <p className="app-text-secondary">{t.apiKeysKeptOnPasswordChange}</p>
      {created && <NewKeyPanel created={created} done={() => setCreated(null)} />}
      {list.keys.length === 0 ? (
        <p className="app-text-secondary">{t.apiKeyNone}</p>
      ) : (
        <ApiKeyTable keys={list.keys} revoke={(k) => void revoke(k)} />
      )}
      {active >= list.max ? (
        <>
          <p className="app-text-secondary">{t.apiKeyLimit(list.max)}</p>
          {/* A failed revoke; with the form below, its own error line shows it. */}
          <FormError text={error} />
        </>
      ) : (
        <section className="profile-form profile-form--divided">
          <h2 className="type-heading-03">{t.apiKeyCreate}</h2>
          <Form className="profile-form" onSubmit={create}>
            <TextInput
              id={`${id}-name`}
              labelText={t.apiKeyName}
              placeholder={t.apiKeyNameHint}
              required
              maxLength={64}
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
            <CheckboxGroup legendText={t.apiKeyScopes}>
              <Checkbox
                id={`${id}-read`}
                labelText={t.apiKeyScopeRead}
                checked={scopes.includes("read")}
                onChange={(_, { checked }) => toggle("read", checked)}
              />
              <Checkbox
                id={`${id}-sync`}
                labelText={t.apiKeyScopeSync}
                checked={scopes.includes("sync")}
                onChange={(_, { checked }) => toggle("sync", checked)}
              />
            </CheckboxGroup>
            <Select
              id={`${id}-expiry`}
              labelText={t.apiKeyExpiry}
              value={expiry ?? ""}
              onChange={(e) => setExpiry(e.target.value === "" ? null : Number(e.target.value))}
            >
              {expiryChoices.map((d) => (
                <SelectItem key={d ?? "never"} value={d ?? ""} text={d === null ? t.apiKeyExpiryNever : t.apiKeyExpiryDays(d)} />
              ))}
            </Select>
            <p className="app-text-secondary">{twoFactor ? t.apiKeyConfirm2FA : t.apiKeyConfirm}</p>
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
            {twoFactor && (
              <TextInput
                id={`${id}-code`}
                labelText={t.twoFactorCode}
                required
                autoComplete="one-time-code"
                autoCapitalize="none"
                spellCheck={false}
                value={code}
                onChange={(e) => setCode(e.target.value)}
              />
            )}
            <FormError text={error} />
            <div>
              <Button type="submit" disabled={busy}>
                {t.apiKeyCreate}
              </Button>
            </div>
          </Form>
        </section>
      )}
    </div>
  );
}
