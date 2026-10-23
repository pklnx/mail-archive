import { useCallback, useEffect, useId, useRef, useState, type FormEvent } from "react";
import {
  Button,
  PasswordInput,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableHeader,
  TableRow,
  TextInput,
} from "@carbon/react";
import { Add } from "@carbon/icons-react";
import { ApiError, passkeyApi, profileApi, retryAfter, type Passkey, type PasskeyList } from "./api";
import { AppModal, DangerModal } from "./DangerModal";
import { relativeTime } from "./format";
import { t } from "./i18n";
import { FormError, Success } from "./notices";
import { browserError, creationOptions, credentialJSON, forgetPasskey, passkeysSupported } from "./webauthn";

/** The message for a failed registration. */
export function registrationError(err: unknown, max: number): string {
  const wait = retryAfter(err);
  if (wait !== null) return t.tooManyAttempts(wait);
  switch (browserError(err)) {
    case "NotAllowedError":
    case "AbortError":
      return t.passkeyCancelled;
    case "InvalidStateError":
      return t.passkeyOnDevice;
  }
  if (err instanceof ApiError) {
    if (err.status === 401) return err.message.includes("password") ? t.wrongCurrentPassword : t.invalidTwoFactor;
    if (err.status === 409 && err.message.includes("name")) return t.passkeyNameTaken;
    if (err.status === 409 && err.message.includes("already registered")) return t.passkeyOnDevice;
    if (err.status === 409) return t.passkeyLimit(max);
    if (err.status === 400 && err.message.includes("not accepted")) return t.passkeyFailed;
  }
  return t.failed(err instanceof Error ? err.message : String(err));
}

/**
 * The user's passkeys on the profile page: list, add, remove.
 * twoFactorVersion changes when two-factor authentication was turned on or
 * off elsewhere on the page; the section then asks the server again whether
 * a code is needed.
 */
export function PasskeySection({ twoFactorVersion = 0 }: { twoFactorVersion?: number }) {
  const [list, setList] = useState<PasskeyList | null>(null);
  const [twoFactor, setTwoFactor] = useState(false);
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<Passkey | null>(null);
  const [error, setError] = useState("");
  const [status, setStatus] = useState("");
  const addButton = useRef<HTMLButtonElement>(null);

  const load = useCallback(() => {
    passkeyApi.list().then(setList, (err: unknown) => setError(t.failed(err instanceof Error ? err.message : String(err))));
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
  if (!list.available) return <p className="app-text-secondary">{t.passkeysUnavailable}</p>;

  const here = list.origin === window.location.origin;
  const canAdd = here && passkeysSupported() && list.passkeys.length < list.max;

  const remove = async (id: number) => {
    setError("");
    setStatus("");
    try {
      const removed = await passkeyApi.remove(id);
      if (removed.rpId) await forgetPasskey(removed.rpId, removed.credentialId);
    } catch (err) {
      setError(t.failed(err instanceof Error ? err.message : String(err)));
    }
    load();
  };

  return (
    <div className="profile-form">
      <p className="app-text-secondary">{t.passkeysText}</p>
      {list.passkeys.length === 0 ? (
        <p className="app-text-secondary">{t.passkeyNone}</p>
      ) : (
        <TableContainer className="profile-table">
          <Table aria-label={t.passkeys} size="md">
            <TableHead>
              <TableRow>
                <TableHeader>{t.colName}</TableHeader>
                <TableHeader>{t.colLastUsed}</TableHeader>
                <TableHeader>
                  <span className="cds--visually-hidden">{t.colActions}</span>
                </TableHeader>
              </TableRow>
            </TableHead>
            <TableBody>
              {list.passkeys.map((p) => (
                <TableRow key={p.id}>
                  <TableCell>
                    <span className="profile-table__name">{p.name}</span>
                  </TableCell>
                  <TableCell>{p.lastUsedAt ? relativeTime(p.lastUsedAt) : t.passkeyNeverUsed}</TableCell>
                  <TableCell className="profile-table__action">
                    <Button kind="danger--ghost" size="sm" onClick={() => setRemoving(p)}>
                      {t.passkeyRemove}
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
      {status && <Success text={status} />}
      {!here && <p className="app-text-secondary">{t.passkeysOtherOrigin(list.origin ?? "")}</p>}
      {here && !passkeysSupported() && <p className="app-text-secondary">{t.passkeysUnsupported}</p>}
      {here && list.passkeys.length >= list.max && <p className="app-text-secondary">{t.passkeyLimit(list.max)}</p>}
      <FormError text={error} />
      {canAdd && (
        <div>
          <Button
            ref={addButton}
            kind="tertiary"
            renderIcon={Add}
            onClick={() => {
              setError("");
              setStatus("");
              setAdding(true);
            }}
          >
            {t.passkeyAdd}
          </Button>
        </div>
      )}
      {adding && (
        <AddPasskeyModal
          twoFactor={twoFactor}
          max={list.max}
          returnFocus={() => addButton.current}
          cancel={() => setAdding(false)}
          added={(name) => {
            setAdding(false);
            setStatus(t.passkeyAdded(name));
            load();
          }}
        />
      )}
      {removing && (
        <DangerModal
          size="xs"
          modalHeading={t.passkeyRemoveTitle(removing.name)}
          primaryButtonText={t.passkeyRemove}
          secondaryButtonText={t.cancel}
          onRequestClose={() => setRemoving(null)}
          onRequestSubmit={() => {
            const id = removing.id;
            setRemoving(null);
            void remove(id);
          }}
        >
          <p>{t.passkeyRemoveText}</p>
        </DangerModal>
      )}
    </div>
  );
}

/**
 * Adds a passkey: a name, the password and (with 2FA) a code, then the
 * browser's passkey dialog. A failure keeps the Modal open with the name and
 * clears password and code.
 */
export function AddPasskeyModal({
  twoFactor,
  max,
  returnFocus,
  cancel,
  added,
}: {
  twoFactor: boolean;
  max: number;
  returnFocus?: () => HTMLElement | null | undefined;
  cancel: () => void;
  added: (name: string) => void;
}) {
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const id = useId();
  const complete = name.trim() !== "" && password !== "" && (!twoFactor || code !== "");

  const add = async (e?: FormEvent) => {
    e?.preventDefault();
    if (busy || !complete) return;
    setBusy(true);
    setError("");
    try {
      const { token, publicKey } = await passkeyApi.beginRegistration(name.trim(), password, code);
      const cred = (await navigator.credentials.create({ publicKey: creationOptions(publicKey) })) as PublicKeyCredential | null;
      if (!cred) throw new DOMException("no passkey", "NotAllowedError");
      await passkeyApi.finishRegistration(token, credentialJSON(cred));
      added(name.trim());
    } catch (err) {
      setError(registrationError(err, max));
      setPassword("");
      setCode("");
      setBusy(false);
    }
  };

  return (
    <AppModal
      size="sm"
      modalHeading={t.passkeyAdd}
      primaryButtonText={t.passkeyAdd}
      secondaryButtonText={t.cancel}
      primaryButtonDisabled={!complete}
      loadingStatus={busy ? "active" : "inactive"}
      loadingDescription={t.passkeyAdd}
      loadingIconDescription={t.loading}
      preventCloseOnClickOutside
      returnFocus={returnFocus}
      onRequestClose={() => {
        if (!busy) cancel();
      }}
      onRequestSubmit={() => void add()}
    >
      <form className="modal-stack" onSubmit={(e) => void add(e)}>
        <TextInput
          id={`${id}-name`}
          data-modal-primary-focus=""
          labelText={t.passkeyName}
          placeholder={t.passkeyNameHint}
          required
          maxLength={64}
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <p>{twoFactor ? t.passkeyConfirm2FA : t.passkeyConfirm}</p>
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
        {/* Enter in a field submits; the Modal's own button is outside the form. */}
        <button type="submit" className="cds--visually-hidden" tabIndex={-1} aria-hidden />
      </form>
    </AppModal>
  );
}
