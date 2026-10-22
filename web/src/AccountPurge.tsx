import { useEffect, useId, useState } from "react";
import { accountsApi, type PurgeBlockers, type PurgePreview } from "./api";
import { fileSize, formatCount, formatDuration } from "./format";
import { button, input } from "./formStyles";
import { t } from "./i18n";

const dangerSolid =
  "rounded-md bg-danger-solid px-3 py-1 text-sm font-medium text-on-accent hover:bg-danger-solid-hover disabled:opacity-50 pointer-coarse:min-h-11";

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

/** The preview of a complete deletion, and the typed-count confirmation when nothing blocks it. */
export function PurgeReview({ preview: p, busy, error, onConfirm, onCancel }: ReviewProps) {
  const [count, setCount] = useState("");
  const id = useId();
  const blocked = purgeBlocked(p.blockers);
  // An exact match only: an empty field is not "0".
  const matches = count.trim() === String(p.deleted);
  return (
    <div className="flex flex-col gap-2 rounded-md border border-danger-line p-3 text-sm" role="group" aria-labelledby={`${id}-title`}>
      <p id={`${id}-title`} className="font-semibold">
        {t.purgeTitle(p.account)}
      </p>
      <ul className="list-disc pl-5">
        <li>{t.purgeLocations(p.locations, formatCount(p.locations))}</li>
        <li>
          {t.purgeDeleted(p.deleted, formatCount(p.deleted), fileSize(p.deletedBytes))}{" "}
          {p.grace ? t.purgeFiles(formatDuration(p.grace)) : t.purgeFilesLater}
        </li>
        {p.kept > 0 && (
          <li>
            {t.purgeKept(p.kept, formatCount(p.kept))}
            <ul className="list-[circle] pl-5">
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
        <div role="alert" className="flex flex-col gap-1 text-danger">
          <p className="font-medium">{t.purgeBlocked}</p>
          <ul className="list-disc pl-5">
            {purgeBlockerLines(p.blockers).map((line) => (
              <li key={line}>{line}</li>
            ))}
          </ul>
        </div>
      ) : (
        <form
          className="flex flex-wrap items-end gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (matches) onConfirm(p.deleted);
          }}
        >
          <label className="flex flex-col gap-1">
            {t.purgeTypeCount(p.deleted)}
            <input
              className={`${input} w-32`}
              inputMode="numeric"
              autoComplete="off"
              value={count}
              onChange={(e) => setCount(e.target.value)}
            />
          </label>
          <button type="submit" className={dangerSolid} disabled={busy || !matches}>
            {t.purgeConfirm}
          </button>
        </form>
      )}
      {error && (
        <p role="alert" className="text-danger">
          {error}
        </p>
      )}
      <div>
        <button type="button" className={button} disabled={busy} onClick={onCancel}>
          {t.cancel}
        </button>
      </div>
    </div>
  );
}

interface DialogProps {
  name: string;
  onClose: () => void;
  /** The purge was confirmed; the server deletes in the background. */
  onConfirmed: () => void;
}

/** Loads the preview of deleting the account completely and confirms it with the typed count. */
export function PurgeDialog({ name, onClose, onConfirmed }: DialogProps) {
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
      // The numbers or the blockers may have changed: show them as they are now.
      setTick((n) => n + 1);
    } finally {
      setBusy(false);
    }
  };

  if (!preview) {
    return error ? (
      <div className="flex flex-col gap-2 text-sm">
        <p role="alert" className="text-danger">
          {error}
        </p>
        <div>
          <button type="button" className={button} onClick={onClose}>
            {t.cancel}
          </button>
        </div>
      </div>
    ) : (
      <p className="text-sm text-ink-muted">{t.loading}</p>
    );
  }
  // A new preview after an error starts with an empty field.
  return <PurgeReview key={tick} preview={preview} busy={busy} error={error} onConfirm={(n) => void confirm(n)} onCancel={onClose} />;
}

/** "Being deleted…", with the counts for admins (refreshed whenever the account list is). */
export function PurgeProgress({ name, admin, refresh }: { name: string; admin: boolean; refresh: unknown }) {
  const [purge, setPurge] = useState<PurgePreview["purge"]>(null);
  useEffect(() => {
    if (!admin) return;
    const ctrl = new AbortController();
    accountsApi.purgePreview(name, ctrl.signal).then(
      (p) => setPurge(p.purge),
      () => {},
    );
    return () => ctrl.abort();
  }, [name, admin, refresh]);
  return (
    <p className="text-sm font-medium text-danger" aria-live="polite">
      {purge ? t.purgeProgress(purge.deleted, purge.expected, purge.kept) : t.purgeRunning}
    </p>
  );
}
