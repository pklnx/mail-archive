import { useCallback, useEffect, useState, type FormEvent } from "react";
import {
  retentionApi,
  usersApi,
  type DeletionReview,
  type DeletionRun,
  type LegalHold,
  type ManagedUser,
  type RetentionAccount,
  type RetentionRules,
} from "./api";
import { day, fileSize } from "./format";
import { t } from "./i18n";
import { PageHeader } from "./PageHeader";
import { parseQuery } from "./searchSyntax";

const button =
  "rounded-md border border-line-control px-2.5 py-1 text-sm hover:bg-surface-hover disabled:opacity-50 pointer-coarse:min-h-11";
const primary = "rounded-md bg-accent px-3 py-1 text-sm font-medium text-on-accent hover:bg-accent-hover disabled:opacity-50 pointer-coarse:min-h-11";
const input =
  "w-full rounded-md border border-line-control bg-field px-2.5 py-1.5 text-sm pointer-coarse:min-h-11";
const tab = "rounded-md px-3 py-1 text-sm pointer-coarse:min-h-11";

const message = (err: unknown) => t.failed(err instanceof Error ? err.message : String(err));

/** The label of an account in a choice: owner / name, marked when removed. */
export function accountLabel(a: Pick<RetentionAccount, "owner" | "name" | "removed">): string {
  return `${a.owner || "-"} / ${a.name}${a.removed ? ` (${t.ruleRemoved})` : ""}`;
}

/** What a hold covers, in words. */
export function holdScope(h: LegalHold): string {
  const parts: string[] = [];
  if (h.user) parts.push(t.holdScopeUser(h.user));
  if (h.account) parts.push(t.holdScopeAccount(h.account, h.accountOwner || "-"));
  if (h.since) parts.push(t.holdScopeFrom(day(h.since)));
  if (h.until) parts.push(t.holdScopeBefore(day(h.until)));
  if (h.filter) {
    const f = h.filter;
    const terms = [f.q ?? "", f.from ? `from:${f.from}` : "", f.to ? `to:${f.to}` : "", f.attachment ? `attachment:${f.attachment}` : ""];
    if (f.hasAttachment) terms.push("has:attachment");
    parts.push(`${terms.filter(Boolean).join(" ")} (${h.matchedAt ? t.holdMatched(h.matched) : t.holdNotMatched})`);
  }
  return parts.length ? parts.join(", ") : t.holdScopeAll;
}

/** Retention rules and legal holds, for admins. */
export function RetentionPage({ close }: { close: () => void }) {
  const [view, setView] = useState<"rules" | "holds" | "runs">("rules");
  const [data, setData] = useState<RetentionRules | null>(null);
  const [error, setError] = useState("");

  const reload = useCallback(() => {
    retentionApi.rules().then(
      (d) => {
        setData(d);
        setError("");
      },
      (err: unknown) => setError(message(err)),
    );
  }, []);
  useEffect(reload, [reload]);

  return (
    <div className="mx-auto flex h-full max-w-4xl flex-col gap-4 overflow-y-auto p-4">
      <PageHeader title={t.retentionTitle} close={close} />
      <div role="tablist" className="flex gap-1">
        {(["rules", "holds", "runs"] as const).map((v) => (
          <button
            key={v}
            type="button"
            role="tab"
            aria-selected={view === v}
            className={`${tab} ${view === v ? "bg-line font-medium" : "hover:bg-surface-hover"}`}
            onClick={() => setView(v)}
          >
            {v === "rules" ? t.retentionRulesTab : v === "holds" ? t.retentionHoldsTab : t.runsTab}
          </button>
        ))}
      </div>
      {error && (
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
      )}
      {!data && !error && <p className="text-sm text-ink-muted">{t.loading}</p>}
      {data && view === "rules" && <RulesTab data={data} reload={reload} />}
      {data && view === "holds" && <HoldsTab accounts={data.accounts} />}
      {view === "runs" && <RunsTab />}
    </div>
  );
}

function RulesTab({ data, reload }: { data: RetentionRules; reload: () => void }) {
  const [error, setError] = useState("");
  const [accountId, setAccountId] = useState("");
  const [folder, setFolder] = useState("");
  const [years, setYears] = useState("10");
  const [timeZone, setTimeZone] = useState(data.defaultTimeZone);
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const account = data.accounts.find((a) => String(a.id) === accountId);

  const add = async (e: FormEvent) => {
    e.preventDefault();
    if (!account) return;
    const n = Number(years);
    const force = n < data.minYears;
    if (force && !window.confirm(t.ruleShort(n, data.minYears))) return;
    setBusy(true);
    setError("");
    try {
      await retentionApi.addRule({ accountId: account.id, folder, years: n, timeZone, note, force });
      setFolder("");
      setNote("");
      reload();
    } catch (err) {
      setError(message(err));
    } finally {
      setBusy(false);
    }
  };

  const remove = async (id: number, what: string) => {
    if (!window.confirm(t.ruleRemoveConfirm(what))) return;
    setError("");
    try {
      await retentionApi.removeRule(id);
    } catch (err) {
      setError(message(err));
    }
    reload();
  };

  return (
    <section className="flex flex-col gap-4">
      <p className="text-sm text-ink-muted">{t.retentionNote}</p>
      <p className="text-sm text-ink-muted">{t.retentionLegal}</p>
      {data.rules.length === 0 ? (
        <p className="text-sm">{t.ruleNone}</p>
      ) : (
        <ul className="flex flex-col divide-y divide-line rounded-lg border border-line text-sm">
          {data.rules.map((r) => {
            const what = `${accountLabel({ owner: r.owner, name: r.account, removed: r.accountRemoved })} / ${r.folder || t.ruleAllFolders}`;
            return (
              <li key={r.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2">
                <span className="min-w-0 flex-1">
                  <span className="font-medium">{what}</span>
                  <span className="text-ink-muted">
                    {" · "}
                    {t.ruleYearsCount(r.years)} · {r.timeZone}
                  </span>
                  {r.note && <span className="block text-ink-muted">{r.note}</span>}
                  <span className="block text-xs text-ink-muted">{t.ruleCreated(r.createdBy, day(r.createdAt))}</span>
                </span>
                <button type="button" className={button} onClick={() => void remove(r.id, what)}>
                  {t.ruleRemove}
                </button>
              </li>
            );
          })}
        </ul>
      )}

      <form onSubmit={(e) => void add(e)} className="grid gap-3 rounded-lg border border-line p-3 text-sm sm:grid-cols-2">
        <label className="flex flex-col gap-1">
          {t.ruleAccount}
          <select className={input} value={accountId} onChange={(e) => (setAccountId(e.target.value), setFolder(""))} required>
            <option value="" />
            {data.accounts.map((a) => (
              <option key={a.id} value={a.id}>
                {accountLabel(a)}
              </option>
            ))}
          </select>
        </label>
        <label className="flex flex-col gap-1">
          {t.ruleFolder}
          <select className={input} value={folder} onChange={(e) => setFolder(e.target.value)} disabled={!account}>
            <option value="">{t.ruleAllFolders}</option>
            {account?.folders.map((f) => (
              <option key={f} value={f}>
                {f}
              </option>
            ))}
          </select>
        </label>
        <label className="flex flex-col gap-1">
          {t.ruleYears}
          <input className={input} type="number" min={1} max={100} value={years} onChange={(e) => setYears(e.target.value)} required />
        </label>
        <label className="flex flex-col gap-1">
          {t.ruleTimeZone}
          <input className={input} value={timeZone} onChange={(e) => setTimeZone(e.target.value)} required />
        </label>
        <label className="flex flex-col gap-1 sm:col-span-2">
          {t.ruleNote}
          <input className={input} value={note} maxLength={500} placeholder={t.ruleNotePlaceholder} onChange={(e) => setNote(e.target.value)} />
        </label>
        {error && (
          <p role="alert" className="text-danger sm:col-span-2">
            {error}
          </p>
        )}
        <button type="submit" className={`${primary} justify-self-start`} disabled={busy || !account}>
          {t.ruleAdd}
        </button>
      </form>
    </section>
  );
}

function HoldsTab({ accounts }: { accounts: RetentionAccount[] }) {
  const [holds, setHolds] = useState<LegalHold[] | null>(null);
  const [released, setReleased] = useState(false);
  const [users, setUsers] = useState<ManagedUser[]>([]);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  const reload = useCallback(() => {
    retentionApi.holds(released).then(setHolds, (err: unknown) => setError(message(err)));
  }, [released]);
  useEffect(reload, [reload]);
  useEffect(() => {
    usersApi.list().then(setUsers, () => setUsers([]));
  }, []);

  const release = async (h: LegalHold) => {
    const note = window.prompt(t.holdReleaseNote);
    if (!note?.trim()) return;
    setError("");
    try {
      await retentionApi.releaseHold(h.id, note.trim());
    } catch (err) {
      setError(message(err));
    }
    reload();
  };

  return (
    <section className="flex flex-col gap-4">
      <p className="text-sm text-ink-muted">{t.holdNote}</p>
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={released} onChange={(e) => setReleased(e.target.checked)} />
        {t.holdShowReleased}
      </label>
      {error && (
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
      )}
      {notice && <p className="text-sm text-ok">{notice}</p>}
      {holds && holds.length === 0 && <p className="text-sm">{t.holdNone}</p>}
      {holds && holds.length > 0 && (
        <ul className="flex flex-col divide-y divide-line rounded-lg border border-line text-sm">
          {holds.map((h) => (
            <li key={h.id} className={`flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2 ${h.releasedAt ? "text-ink-muted" : ""}`}>
              <span className="min-w-0 flex-1">
                <span className="font-medium">{h.name}</span>
                <span className="text-ink-muted"> · {holdScope(h)}</span>
                {h.reason && <span className="block text-ink-muted">{h.reason}</span>}
                <span className="block text-xs text-ink-muted">
                  {h.releasedAt ? t.holdReleased(h.releasedBy, day(h.releasedAt), h.releaseNote) : t.ruleCreated(h.createdBy, day(h.createdAt))}
                </span>
              </span>
              {!h.releasedAt && (
                <button type="button" className={button} onClick={() => void release(h)}>
                  {t.holdRelease}
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
      <HoldForm
        accounts={accounts}
        users={users}
        added={(matched) => {
          setNotice(t.holdAdded(matched));
          reload();
        }}
      />
    </section>
  );
}

function HoldForm({ accounts, users, added }: { accounts: RetentionAccount[]; users: ManagedUser[]; added: (matched?: number) => void }) {
  const [name, setName] = useState("");
  const [reason, setReason] = useState("");
  const [user, setUser] = useState("");
  const [accountId, setAccountId] = useState("");
  const [query, setQuery] = useState("");
  const [all, setAll] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const p = parseQuery(query);
  const filter = p.text || p.from || p.to || p.attachment || p.hasAttachment
    ? { q: p.text, from: p.from, to: p.to, attachment: p.attachment, hasAttachment: p.hasAttachment }
    : null;
  const unscoped = !user && !accountId && !filter && !p.after && !p.before;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const res = await retentionApi.addHold({
        name,
        reason,
        user,
        accountId: accountId ? Number(accountId) : null,
        filter,
        // after: is inclusive, before: exclusive, both local midnight.
        since: p.after ? new Date(`${p.after}T00:00:00`).toISOString() : null,
        until: p.before ? new Date(`${p.before}T00:00:00`).toISOString() : null,
        all: unscoped && all,
      });
      setName("");
      setReason("");
      setQuery("");
      setAll(false);
      added(res.matched);
    } catch (err) {
      setError(message(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={(e) => void submit(e)} className="grid gap-3 rounded-lg border border-line p-3 text-sm sm:grid-cols-2">
      <label className="flex flex-col gap-1">
        {t.holdName}
        <input className={input} value={name} maxLength={200} onChange={(e) => setName(e.target.value)} required />
      </label>
      <label className="flex flex-col gap-1">
        {t.holdReason}
        <input className={input} value={reason} maxLength={2000} onChange={(e) => setReason(e.target.value)} required />
      </label>
      <label className="flex flex-col gap-1">
        {t.holdUser}
        <select className={input} value={user} onChange={(e) => setUser(e.target.value)}>
          <option value="">{t.holdAnyUser}</option>
          {users.map((u) => (
            <option key={u.name} value={u.name}>
              {u.name}
            </option>
          ))}
        </select>
      </label>
      <label className="flex flex-col gap-1">
        {t.holdAccount}
        <select className={input} value={accountId} onChange={(e) => setAccountId(e.target.value)}>
          <option value="">{t.holdAnyAccount}</option>
          {accounts.map((a) => (
            <option key={a.id} value={a.id}>
              {accountLabel(a)}
            </option>
          ))}
        </select>
      </label>
      <label className="flex flex-col gap-1 sm:col-span-2">
        {t.holdFilter}
        <input className={input} value={query} onChange={(e) => setQuery(e.target.value)} placeholder={t.holdFilterHint} />
      </label>
      {unscoped && (
        <label className="flex items-center gap-2 sm:col-span-2">
          <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} />
          <span>
            {t.holdAll}
            <span className="block text-xs text-ink-muted">{t.holdAllNeeded}</span>
          </span>
        </label>
      )}
      {error && (
        <p role="alert" className="text-danger sm:col-span-2">
          {error}
        </p>
      )}
      <button type="submit" className={`${primary} justify-self-start`} disabled={busy || (unscoped && !all)}>
        {t.holdAdd}
      </button>
    </form>
  );
}

/** Whether a run is still open: proposed, or confirmed and being deleted. */
export function runOpen(r: Pick<DeletionRun, "state">): boolean {
  return r.state === "proposed" || r.state === "confirmed";
}

function RunsTab() {
  const [runs, setRuns] = useState<DeletionRun[] | null>(null);
  const [selected, setSelected] = useState<number | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const reload = useCallback(() => {
    retentionApi.runs().then(
      (r) => {
        setRuns(r);
        setError("");
        setSelected((cur) => cur ?? r.find(runOpen)?.id ?? null);
      },
      (err: unknown) => setError(message(err)),
    );
  }, []);
  useEffect(reload, [reload]);
  // A confirmed run deletes in the background: poll until it is done.
  const deleting = runs?.some((r) => r.state === "confirmed") ?? false;
  useEffect(() => {
    if (!deleting) return;
    const id = setInterval(reload, 3000);
    return () => clearInterval(id);
  }, [deleting, reload]);

  const propose = async () => {
    setBusy(true);
    setError("");
    try {
      const run = await retentionApi.propose();
      setSelected(run.id);
    } catch (err) {
      setError(message(err));
    } finally {
      setBusy(false);
    }
    reload();
  };

  const open = runs?.find(runOpen);
  return (
    <section className="flex flex-col gap-4">
      <p className="text-sm text-ink-muted">{t.runsNote}</p>
      <p className="text-sm text-ink-muted">{t.runsCLI}</p>
      {!open && (
        <button type="button" className={`${primary} self-start`} disabled={busy || !runs} onClick={() => void propose()}>
          {t.runPropose}
        </button>
      )}
      {error && (
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
      )}
      {runs && runs.length === 0 && <p className="text-sm">{t.runNone}</p>}
      {runs && runs.length > 0 && (
        <ul className="flex flex-col divide-y divide-line rounded-lg border border-line text-sm">
          {runs.map((r) => (
            <li key={r.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2">
              <span className="min-w-0 flex-1">
                <span className="font-medium">{t.runLine(r.id, r.candidates, fileSize(r.bytes))}</span>
                <span className="text-ink-muted"> · {t.runState(r.state)}</span>
                {(r.state === "done" || r.deleted + r.kept > 0) && (
                  <span className="text-ink-muted"> · {t.runResult(r.deleted, r.kept)}</span>
                )}
                <span className="block text-xs text-ink-muted">{t.runProposedBy(r.createdBy, day(r.createdAt))}</span>
              </span>
              {selected !== r.id && (
                <button type="button" className={button} onClick={() => setSelected(r.id)}>
                  {t.runShow}
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
      {selected !== null && <RunReview key={`${selected}-${runs?.find((r) => r.id === selected)?.state}`} id={selected} changed={reload} />}
    </section>
  );
}

function RunReview({ id, changed }: { id: number; changed: () => void }) {
  const [review, setReview] = useState<DeletionReview | null>(null);
  const [count, setCount] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    retentionApi.run(id).then(setReview, (err: unknown) => setError(message(err)));
  }, [id]);

  const act = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setError("");
    try {
      await action();
    } catch (err) {
      setError(message(err));
    } finally {
      setBusy(false);
    }
    changed();
  };

  if (error && !review) {
    return (
      <p role="alert" className="text-sm text-danger">
        {error}
      </p>
    );
  }
  if (!review) return <p className="text-sm text-ink-muted">{t.loading}</p>;
  const { run } = review;
  return (
    <div className="flex flex-col gap-3 rounded-lg border border-line p-3 text-sm">
      <p className="font-medium">
        {t.runLine(run.id, run.candidates, fileSize(run.bytes))} · {t.runState(run.state)}
      </p>
      {review.groups.length > 0 && (
        // Below 640px each row is a small block of label and value.
        <table className="w-full text-left max-sm:block">
          <thead className="text-ink-muted max-sm:hidden">
            <tr>
              <th className="py-1 font-normal">{t.runGroupsHead}</th>
              <th className="py-1 font-normal">{t.runYear}</th>
              <th className="py-1 text-right font-normal">{t.runMessages}</th>
            </tr>
          </thead>
          <tbody className="max-sm:block">
            {review.groups.map((g) => (
              <tr
                key={`${g.owner}/${g.account}/${g.folder}/${g.year}`}
                className="border-t border-line max-sm:grid max-sm:grid-cols-[auto_1fr] max-sm:gap-x-3 max-sm:py-2"
              >
                <td data-label={t.runGroupsHead} className="py-1 max-sm:contents max-sm:before:text-ink-muted max-sm:before:content-[attr(data-label)]">
                  <span className="max-sm:break-all">
                    {g.owner || "-"} / {g.account} / {g.folder}
                  </span>
                </td>
                <td data-label={t.runYear} className="py-1 tabular-nums max-sm:contents max-sm:before:text-ink-muted max-sm:before:content-[attr(data-label)]">
                  <span>{g.year}</span>
                </td>
                <td data-label={t.runMessages} className="py-1 text-right tabular-nums max-sm:text-left max-sm:contents max-sm:before:text-ink-muted max-sm:before:content-[attr(data-label)]">
                  <span>{g.messages}</span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {review.candidates.length > 0 && (
        <details>
          <summary className="cursor-pointer text-ink-muted">{t.runHashes(review.listed, run.candidates)}</summary>
          <ul className="mt-2 max-h-64 overflow-y-auto font-mono text-xs">
            {review.candidates.map((c) => (
              <li key={c.sha256} className={c.state === "kept" ? "text-ink-muted" : ""}>
                {c.sha256} {c.state !== "pending" && `(${c.state})`}
              </li>
            ))}
          </ul>
        </details>
      )}
      {run.state === "proposed" && run.candidates === 0 && <p>{t.runEmpty}</p>}
      {run.state === "proposed" && (
        <div className="flex flex-wrap items-end gap-2">
          {run.candidates > 0 && (
            <form
              className="flex flex-wrap items-end gap-2"
              onSubmit={(e) => {
                e.preventDefault();
                void act(() => retentionApi.confirm(run.id, Number(count)));
              }}
            >
              <label className="flex flex-col gap-1">
                {t.runConfirmPrompt(run.candidates)}
                <input className={`${input} w-32`} inputMode="numeric" value={count} onChange={(e) => setCount(e.target.value)} />
              </label>
              <button
                type="submit"
                className="rounded-md bg-danger-solid px-3 py-1 text-sm font-medium text-on-accent hover:bg-danger-solid-hover disabled:opacity-50 pointer-coarse:min-h-11"
                disabled={busy || Number(count) !== run.candidates}
              >
                {t.runConfirm}
              </button>
            </form>
          )}
          <button type="button" className={button} disabled={busy} onClick={() => void act(() => retentionApi.cancel(run.id))}>
            {t.runCancel}
          </button>
        </div>
      )}
      {error && (
        <p role="alert" className="text-danger">
          {error}
        </p>
      )}
    </div>
  );
}
