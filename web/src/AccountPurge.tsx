import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { InlineNotification, ProgressBar, TextInput } from "@carbon/react";
import { accountsApi, ApiError, type AccountPurge, type PurgeBlockers, type PurgePreview } from "./api";
import { DangerModal } from "./DangerModal";
import { fileSize, formatCount, formatDuration } from "./format";
import { t } from "./i18n";

function message(err: unknown): string {
  return t.failed(err instanceof Error ? err.message : String(err));
}

/** Whether something keeps the account from being deleted completely. */
export function purgeBlocked(b: PurgeBlockers): boolean {
  return b.notRemoved || b.syncing || b.purgeOpen || b.rules.length > 0 || b.holds.length > 0;
}

/** The blockers in the UI's language, naming the rules and holds. */
export function purgeBlockerLines(b: PurgeBlockers): string[] {
  const out: string[] = [];
  if (b.notRemoved) out.push(t.purgeNotRemoved);
  if (b.syncing) out.push(t.purgeSyncing);
  if (b.purgeOpen) out.push(t.purgeOtherOpen);
  for (const r of b.rules) out.push(t.purgeRule(r.id, r.folder, r.years));
  for (const h of b.holds) {
    out.push(h.released ? t.purgeHoldReleased(h.id, h.name) : h.namesAccount ? t.purgeHoldNames(h.id, h.name) : t.purgeHoldCovers(h.id, h.name));
  }
  if (b.rules.length > 0) out.push(t.purgeFixRules);
  if (b.holds.some((h) => !h.released)) out.push(t.purgeFixHolds);
  return out;
}

interface ReviewProps {
  preview: PurgePreview;
  busy: boolean;
  error: string;
  onConfirm: (count: number) => void;
  onCancel: () => void;
}

/**
 * The preview of a complete deletion in a danger Modal, and the
 * typed-count confirmation when nothing blocks it. Something blocking
 * leaves only the reasons and the close button: there is nothing to
 * confirm.
 */
export function PurgeReview({ preview: p, busy, error, onConfirm, onCancel }: ReviewProps) {
  const [count, setCount] = useState("");
  const id = useId();
  const heading = useRef<HTMLSpanElement>(null);
  const field = useRef<HTMLInputElement>(null);
  const blocked = purgeBlocked(p.blockers);
  // An exact match only: an empty field is not "0".
  const matches = count.trim() === String(p.deleted);
  const confirm = () => {
    if (matches && !blocked && !busy) onConfirm(p.deleted);
  };
  // Focus moves into the dialog when it opens: to the field, or to the
  // heading when something blocks the deletion. A layout effect, so the
  // focus is set in the same commit that shows the dialog; Carbon's Modal
  // then focuses the same element (data-modal-primary-focus).
  useLayoutEffect(() => {
    (field.current ?? heading.current)?.focus();
  }, []);
  const title = t.purgeTitle(p.account);
  return (
    <DangerModal
      aria-label={title}
      modalHeading={
        <span ref={heading} tabIndex={-1} className="purge__heading" data-modal-primary-focus={blocked ? "" : undefined}>
          {title}
        </span>
      }
      size="sm"
      passiveModal={blocked}
      preventCloseOnClickOutside
      primaryButtonText={t.purgeConfirm}
      primaryButtonDisabled={!matches}
      secondaryButtonText={t.cancel}
      loadingStatus={busy ? "active" : "inactive"}
      loadingDescription={t.purgeConfirm}
      loadingIconDescription={t.loading}
      onRequestClose={() => {
        if (!busy) onCancel();
      }}
      onRequestSubmit={confirm}
    >
      <div className="purge">
        <ul className="bullet-list">
          <li>{t.purgeLocations(p.locations, formatCount(p.locations))}</li>
          <li>
            {t.purgeDeleted(p.deleted, formatCount(p.deleted), fileSize(p.deletedBytes))}{" "}
            {p.grace ? t.purgeFiles(p.deleted, formatDuration(p.grace)) : t.purgeFilesLater(p.deleted)}
          </li>
          {p.kept > 0 && (
            <li>
              {t.purgeKept(p.kept, formatCount(p.kept))}
              <ul className="bullet-list bullet-list--nested">
                {p.keptIn.map((k) => (
                  <li key={`${k.owner}/${k.account}`}>
                    <span className="break-all">
                      {k.owner || "-"} / {k.account}
                    </span>
                    : {t.messageCount(k.messages, formatCount(k.messages))}
                  </li>
                ))}
              </ul>
            </li>
          )}
          <li>{t.purgeAccountGone}</li>
        </ul>
        {blocked ? (
          <InlineNotification
            kind="error"
            lowContrast
            hideCloseButton
            role="alert"
            className="notification--full"
            statusIconDescription={t.carbon.status.error}
            title={t.purgeBlocked}
          >
            <ul className="bullet-list">
              {purgeBlockerLines(p.blockers).map((line) => (
                <li key={line}>{line}</li>
              ))}
            </ul>
          </InlineNotification>
        ) : (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              confirm();
            }}
          >
            <TextInput
              id={`${id}-count`}
              ref={field}
              data-modal-primary-focus=""
              className="purge__count"
              labelText={t.purgeTypeCount(p.deleted)}
              inputMode="numeric"
              autoComplete="off"
              value={count}
              onChange={(e) => setCount(e.target.value)}
            />
          </form>
        )}
        {error && (
          <p role="alert" className="text-error">
            {error}
          </p>
        )}
      </div>
    </DangerModal>
  );
}

interface DialogProps {
  name: string;
  onClose: () => void;
  /** The purge was confirmed; the server deletes in the background. */
  onConfirmed: () => void;
  /**
   * The server refused with a conflict (a purge open elsewhere, the
   * account changed, the count changed): reload the account list now.
   */
  onConflict?: () => void;
}

/** Loads the preview of deleting the account completely and confirms it with the typed count. */
export function PurgeDialog({ name, onClose, onConfirmed, onConflict }: DialogProps) {
  const [preview, setPreview] = useState<PurgePreview | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [tick, setTick] = useState(0);

  useEffect(() => {
    const ctrl = new AbortController();
    accountsApi.purgePreview(name, ctrl.signal).then(setPreview, (err: unknown) => {
      if (!ctrl.signal.aborted) setError(message(err));
    });
    return () => ctrl.abort();
  }, [name, tick]);

  const confirm = async (count: number) => {
    setBusy(true);
    setError("");
    try {
      await accountsApi.purge(name, count);
      onConfirmed();
    } catch (err) {
      setError(message(err));
      if (err instanceof ApiError && err.status === 409) onConflict?.();
      // The numbers or the blockers may have changed: show them as they are now.
      setTick((n) => n + 1);
    } finally {
      setBusy(false);
    }
  };

  if (!preview) {
    // Loading, or the preview failed: only the close button.
    return (
      <DangerModal aria-label={t.purgeTitle(name)} modalHeading={t.purgeTitle(name)} size="sm" passiveModal preventCloseOnClickOutside onRequestClose={onClose}>
        {error ? (
          <p role="alert" className="text-error">
            {error}
          </p>
        ) : (
          <p className="text-secondary">{t.loading}</p>
        )}
      </DangerModal>
    );
  }
  // A new preview after an error starts with an empty field.
  return <PurgeReview key={tick} preview={preview} busy={busy} error={error} onConfirm={(n) => void confirm(n)} onCancel={onClose} />;
}

/**
 * "Being deleted completely…", with the counts from the account list. Only
 * the unchanging status line is live, so screen readers announce the state
 * once and not every count update.
 */
export function PurgeProgress({ purge }: { purge: AccountPurge | null }) {
  return (
    <div className="purge-progress">
      <p role="status" className="purge-progress__state">
        {t.purgeRunning}
      </p>
      {/* No helperText: Carbon would add an English "Loading" for screen readers. */}
      <ProgressBar
        size="small"
        hideLabel
        label={t.purgeRunning}
        value={purge && purge.expected > 0 ? purge.deleted : undefined}
        max={purge && purge.expected > 0 ? purge.expected : undefined}
      />
      {purge && <p className="text-secondary">{t.purgeProgress(formatCount(purge.deleted), formatCount(purge.expected), formatCount(purge.kept))}</p>}
    </div>
  );
}
