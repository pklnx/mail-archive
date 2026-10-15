import { useEffect, useMemo, useState } from "react";
import { auditApi, usersApi, type AuditEntry, type AuditFilter, type ManagedUser } from "./api";
import { foldEntries, shortUserAgent, type LogRow } from "./auditLog";
import { timestamp } from "./format";
import { t } from "./i18n";
import { PageHeader } from "./PageHeader";

const button =
  "rounded-md border border-line-control px-2.5 py-1 text-sm hover:bg-surface-hover disabled:opacity-50 pointer-coarse:min-h-11";
const input =
  "rounded-md border border-line-control bg-field px-2.5 py-1.5 text-sm pointer-coarse:min-h-11";

/** The action groups of the filter, as the API names them. */
export const auditGroups = ["login", "search", "read", "export", "accounts", "users", "profile", "api_keys", "sync", "retention"];

const isoDay = (d: Date) => d.toISOString().slice(0, 10);

/** The default period: the last 30 days. */
export function defaultFilter(now = new Date()): AuditFilter {
  return { since: isoDay(new Date(now.getTime() - 30 * 86_400_000)), until: "", group: "", who: "" };
}

/** The target of an entry in words: the subject of a visible message, otherwise a name or the start of the hash. */
export function targetLabel(e: AuditEntry): string {
  if (!e.target) return "";
  if (e.target.subject) return e.target.subject;
  if (e.target.name) return e.target.name;
  return e.target.type === "message" ? `${e.target.id.slice(0, 12)}…` : e.target.id;
}

/** The detail worth a glance: the search text, a login's method and reason, changed settings. */
export function entryDetail(e: AuditEntry): string {
  const d = e.details;
  if (e.action === "message.search") {
    const q = typeof d.q === "string" ? d.q : "";
    const rest = Object.entries(d)
      .filter(([k, v]) => k !== "q" && typeof v === "string" && v !== "")
      .map(([k, v]) => `${k}:${String(v)}`);
    return [q ? `“${q}”` : "", ...rest].filter(Boolean).join(" ");
  }
  if (e.action.startsWith("auth.")) return [d.method, d.reason].filter((v) => typeof v === "string").join(", ");
  if (e.action === "profile.change" && Array.isArray(d.fields)) {
    return d.fields
      .filter((f): f is string => typeof f === "string")
      .map((f) => {
        const c = d[f] as { from?: unknown; to?: unknown } | undefined;
        return `${f}: ${String(c?.from ?? "")} → ${String(c?.to ?? "")}`;
      })
      .join(", ");
  }
  return "";
}

/** The audit log: a user's own entries, or everyone's for admins. */
export function LogPage({ admin, close, open }: { admin: boolean; close: () => void; open: (sha: string) => void }) {
  const [filter, setFilter] = useState<AuditFilter>(() => defaultFilter());
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [cursor, setCursor] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [users, setUsers] = useState<ManagedUser[]>([]);
  const [expanded, setExpanded] = useState<Set<number>>(new Set());

  useEffect(() => {
    if (admin) usersApi.list().then(setUsers, () => setUsers([]));
  }, [admin]);

  useEffect(() => {
    const ctl = new AbortController();
    setLoading(true);
    setError("");
    auditApi.list(filter, "", ctl.signal).then(
      (page) => {
        setEntries(page.entries);
        setCursor(page.nextCursor ?? "");
        setLoading(false);
      },
      (err: unknown) => {
        if (ctl.signal.aborted) return;
        setError(t.failed(err instanceof Error ? err.message : String(err)));
        setLoading(false);
      },
    );
    return () => ctl.abort();
  }, [filter]);

  const more = async () => {
    setLoading(true);
    try {
      const page = await auditApi.list(filter, cursor);
      setEntries((cur) => [...cur, ...page.entries]);
      setCursor(page.nextCursor ?? "");
    } catch (err) {
      setError(t.failed(err instanceof Error ? err.message : String(err)));
    } finally {
      setLoading(false);
    }
  };

  const rows = useMemo(() => foldEntries(entries), [entries]);
  const toggle = (id: number) =>
    setExpanded((cur) => {
      const next = new Set(cur);
      if (!next.delete(id)) next.add(id);
      return next;
    });
  const set = (patch: Partial<AuditFilter>) => setFilter((f) => ({ ...f, ...patch }));

  return (
    <div className="mx-auto flex h-full max-w-5xl flex-col gap-4 overflow-y-auto p-4">
      <PageHeader title={t.logTitle} close={close} />
      <p className="text-sm text-ink-muted">{admin ? t.logAdminNote : t.logOwnNote}</p>

      <div className="flex flex-wrap items-end gap-3 text-sm">
        <label className="flex flex-col gap-1">
          {t.logFrom}
          <input className={input} type="date" value={filter.since} onChange={(e) => set({ since: e.target.value })} />
        </label>
        <label className="flex flex-col gap-1">
          {t.logTo}
          <input className={input} type="date" value={filter.until} onChange={(e) => set({ until: e.target.value })} />
        </label>
        <label className="flex flex-col gap-1">
          {t.logAction}
          <select className={input} value={filter.group} onChange={(e) => set({ group: e.target.value })}>
            <option value="">{t.logAllActions}</option>
            {auditGroups.map((g) => (
              <option key={g} value={g}>
                {t.logGroup(g)}
              </option>
            ))}
          </select>
        </label>
        {admin && (
          <label className="flex flex-col gap-1">
            {t.logWho}
            <select className={input} value={filter.who} onChange={(e) => set({ who: e.target.value })}>
              <option value="">{t.logEveryone}</option>
              <option value="cli">{t.logCommandLine}</option>
              <option value="api_key">{t.logApiKeys}</option>
              {users.map((u) => (
                <option key={u.name} value={u.name}>
                  {u.name}
                </option>
              ))}
            </select>
          </label>
        )}
      </div>

      {error && (
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
      )}
      {!loading && !error && rows.length === 0 && <p className="text-sm text-ink-muted">{t.logEmpty}</p>}

      {rows.length > 0 && (
        <ul className="flex flex-col divide-y divide-line rounded-lg border border-line text-sm">
          {rows.map((row) => (
            <LogItem
              key={row.entry.id}
              row={row}
              admin={admin}
              open={open}
              expanded={expanded.has(row.entry.id)}
              toggle={() => toggle(row.entry.id)}
            />
          ))}
        </ul>
      )}
      {cursor && (
        <button type="button" className={`${button} self-start`} disabled={loading} onClick={() => void more()}>
          {t.logMore}
        </button>
      )}
      {loading && rows.length === 0 && <p className="text-sm text-ink-muted">{t.loading}</p>}
    </div>
  );
}

function LogItem({
  row,
  admin,
  open,
  expanded,
  toggle,
}: {
  row: LogRow;
  admin: boolean;
  open: (sha: string) => void;
  expanded: boolean;
  toggle: () => void;
}) {
  return (
    <li className="flex flex-col gap-1 px-3 py-2">
      <EntryLine entry={row.entry} admin={admin} open={open} />
      {row.folded.length > 0 && (
        <button type="button" className="self-start text-xs text-accent-ink hover:underline" onClick={toggle}>
          {expanded ? t.logHideFolded : t.logFolded(row.folded.length)}
        </button>
      )}
      {expanded && (
        <ul className="ml-4 flex flex-col gap-1 border-l border-line pl-3">
          {row.folded.map((e) => (
            <li key={e.id}>
              <EntryLine entry={e} admin={admin} open={open} />
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}

function EntryLine({ entry: e, admin, open }: { entry: AuditEntry; admin: boolean; open: (sha: string) => void }) {
  const target = targetLabel(e);
  const detail = entryDetail(e);
  const failed = e.action === "auth.login_failed";
  return (
    <div className="flex flex-col gap-0.5 md:grid md:grid-cols-[12rem_1fr_14rem] md:gap-3">
      <span className="text-ink-muted tabular-nums">{timestamp(e.at)}</span>
      <span className="min-w-0">
        <span className={`font-medium ${failed ? "text-danger" : ""}`}>{t.auditAction(e.action)}</span>
        {admin && e.user && <span className="text-ink-muted"> · {e.user}</span>}
        {e.actor === "cli" && <span className="text-ink-muted"> · {t.logCLI}</span>}
        {e.apiKey && <span className="text-ink-muted"> · {t.logViaKey(e.apiKey)}</span>}
        {target && (
          <>
            {" · "}
            {e.target?.type === "message" && e.target.subject ? (
              <button type="button" className="text-accent-ink hover:underline" onClick={() => open(e.target!.id)}>
                {target}
              </button>
            ) : (
              <span className={e.target?.type === "message" ? "font-mono text-xs" : ""}>{target}</span>
            )}
          </>
        )}
        {detail && <span className="block break-words text-ink-muted">{detail}</span>}
      </span>
      <span className="truncate text-xs text-ink-muted" title={e.userAgent ?? ""}>
        {[e.clientIp, e.userAgent ? shortUserAgent(e.userAgent) : ""].filter(Boolean).join(" · ")}
      </span>
    </div>
  );
}
