import { useCallback, useEffect, useState, type FormEvent } from "react";
import {
  retentionApi,
  usersApi,
  type LegalHold,
  type ManagedUser,
  type RetentionAccount,
  type RetentionRules,
} from "./api";
import { day } from "./format";
import { t } from "./i18n";
import { parseQuery } from "./searchSyntax";

const button =
  "rounded-md border border-zinc-300 px-2.5 py-1 text-sm hover:bg-zinc-100 disabled:opacity-50 dark:border-zinc-700 dark:hover:bg-zinc-800";
const primary = "rounded-md bg-blue-600 px-3 py-1 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50";
const input =
  "w-full rounded-md border border-zinc-300 bg-white px-2.5 py-1.5 text-sm outline-none focus:border-blue-500 dark:border-zinc-700 dark:bg-zinc-900";
const tab = "rounded-md px-3 py-1 text-sm";

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
  const [view, setView] = useState<"rules" | "holds">("rules");
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
      <button type="button" className="self-start text-sm text-blue-600 lg:hidden" onClick={close}>
        {t.backToMail}
      </button>
      <h1 className="text-xl font-semibold">{t.retentionTitle}</h1>
      <div role="tablist" className="flex gap-1">
        {(["rules", "holds"] as const).map((v) => (
          <button
            key={v}
            type="button"
            role="tab"
            aria-selected={view === v}
            className={`${tab} ${view === v ? "bg-zinc-200 font-medium dark:bg-zinc-800" : "hover:bg-zinc-100 dark:hover:bg-zinc-900"}`}
            onClick={() => setView(v)}
          >
            {v === "rules" ? t.retentionRulesTab : t.retentionHoldsTab}
          </button>
        ))}
      </div>
      {error && (
        <p role="alert" className="text-sm text-red-600">
          {error}
        </p>
      )}
      {!data && !error && <p className="text-sm text-zinc-500">{t.loading}</p>}
      {data && view === "rules" && <RulesTab data={data} reload={reload} />}
      {data && view === "holds" && <HoldsTab accounts={data.accounts} />}
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
      <p className="text-sm text-zinc-500">{t.retentionNote}</p>
      <p className="text-sm text-zinc-500">{t.retentionLegal}</p>
      {data.rules.length === 0 ? (
        <p className="text-sm">{t.ruleNone}</p>
      ) : (
        <ul className="flex flex-col divide-y divide-zinc-200 rounded-lg border border-zinc-200 text-sm dark:divide-zinc-800 dark:border-zinc-800">
          {data.rules.map((r) => {
            const what = `${accountLabel({ owner: r.owner, name: r.account, removed: r.accountRemoved })} / ${r.folder || t.ruleAllFolders}`;
            return (
              <li key={r.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2">
                <span className="min-w-0 flex-1">
                  <span className="font-medium">{what}</span>
                  <span className="text-zinc-500">
                    {" · "}
                    {t.ruleYearsCount(r.years)} · {r.timeZone}
                  </span>
                  {r.note && <span className="block text-zinc-600 dark:text-zinc-400">{r.note}</span>}
                  <span className="block text-xs text-zinc-500">{t.ruleCreated(r.createdBy, day(r.createdAt))}</span>
                </span>
                <button type="button" className={button} onClick={() => void remove(r.id, what)}>
                  {t.ruleRemove}
                </button>
              </li>
            );
          })}
        </ul>
      )}

      <form onSubmit={(e) => void add(e)} className="grid gap-3 rounded-lg border border-zinc-200 p-3 text-sm sm:grid-cols-2 dark:border-zinc-800">
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
          <p role="alert" className="text-red-600 sm:col-span-2">
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
      <p className="text-sm text-zinc-500">{t.holdNote}</p>
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={released} onChange={(e) => setReleased(e.target.checked)} />
        {t.holdShowReleased}
      </label>
      {error && (
        <p role="alert" className="text-sm text-red-600">
          {error}
        </p>
      )}
      {notice && <p className="text-sm text-green-700 dark:text-green-400">{notice}</p>}
      {holds && holds.length === 0 && <p className="text-sm">{t.holdNone}</p>}
      {holds && holds.length > 0 && (
        <ul className="flex flex-col divide-y divide-zinc-200 rounded-lg border border-zinc-200 text-sm dark:divide-zinc-800 dark:border-zinc-800">
          {holds.map((h) => (
            <li key={h.id} className={`flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2 ${h.releasedAt ? "text-zinc-500" : ""}`}>
              <span className="min-w-0 flex-1">
                <span className="font-medium">{h.name}</span>
                <span className="text-zinc-500"> · {holdScope(h)}</span>
                {h.reason && <span className="block text-zinc-600 dark:text-zinc-400">{h.reason}</span>}
                <span className="block text-xs text-zinc-500">
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
    <form onSubmit={(e) => void submit(e)} className="grid gap-3 rounded-lg border border-zinc-200 p-3 text-sm sm:grid-cols-2 dark:border-zinc-800">
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
            <span className="block text-xs text-zinc-500">{t.holdAllNeeded}</span>
          </span>
        </label>
      )}
      {error && (
        <p role="alert" className="text-red-600 sm:col-span-2">
          {error}
        </p>
      )}
      <button type="submit" className={`${primary} justify-self-start`} disabled={busy || (unscoped && !all)}>
        {t.holdAdd}
      </button>
    </form>
  );
}
