import { Fragment, useEffect, useId, useMemo, useState, type MouseEvent, type ReactNode } from "react";
import {
  Button,
  Link,
  Select,
  SelectItem,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableHeader,
  TableRow,
} from "@carbon/react";
import { DateRange } from "./DateRange";
import { auditApi, usersApi, type AuditEntry, type AuditFilter, type ManagedUser } from "./api";
import { foldEntries, shortUserAgent } from "./auditLog";
import { isPlainClick } from "./clicks";
import { dayOfDate, shiftDay, timestamp } from "./format";
import { t } from "./i18n";
import { TableSkeleton } from "./tableSkeleton";
import { formatState, parseState } from "./urlState";

/** The action groups of the filter, as the API names them. */
export const auditGroups = ["login", "search", "read", "export", "accounts", "users", "profile", "api_keys", "sync", "retention"];

/** The default period: the last 30 local days. */
export function defaultFilter(now = new Date()): AuditFilter {
  return { since: shiftDay(dayOfDate(now), -30), until: "", group: "", who: "" };
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
export function LogPage({ admin, open }: { admin: boolean; open: (sha: string) => void }) {
  const [filter, setFilter] = useState<AuditFilter>(() => defaultFilter());
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [cursor, setCursor] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [users, setUsers] = useState<ManagedUser[]>([]);
  const [expanded, setExpanded] = useState<Set<number>>(new Set());
  const id = useId();

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
    <div className="accounts-page">
      <div className="page page--wide">
        <header className="page-header">
          <h1 className="page-header__title type-heading-04">{t.logTitle}</h1>
          <p className="page-header__intro app-text-secondary">{admin ? t.logAdminNote : t.logOwnNote}</p>
        </header>

        <div className="log-filters">
          {/* The search filters' range picker (#377); inclusive local days, sent as instants (auditQuery, #402). */}
          <DateRange
            ids={{ start: `${id}-from`, end: `${id}-to` }}
            labels={{ start: t.logFrom, end: t.logTo }}
            start={filter.since}
            end={filter.until}
            onChange={(since, until) => set({ since, until })}
          />
          <Select id={`${id}-action`} labelText={t.logAction} value={filter.group} onChange={(e) => set({ group: e.target.value })}>
            <SelectItem value="" text={t.logAllActions} />
            {auditGroups.map((g) => (
              <SelectItem key={g} value={g} text={t.logGroup(g)} />
            ))}
          </Select>
          {admin && (
            <Select id={`${id}-who`} labelText={t.logWho} value={filter.who} onChange={(e) => set({ who: e.target.value })}>
              <SelectItem value="" text={t.logEveryone} />
              <SelectItem value="cli" text={t.logCommandLine} />
              <SelectItem value="api_key" text={t.logApiKeys} />
              {users.map((u) => (
                <SelectItem key={u.name} value={u.name} text={u.name} />
              ))}
            </Select>
          )}
        </div>

        {error && (
          <p role="alert" className="app-text-error">
            {error}
          </p>
        )}
        {!loading && !error && rows.length === 0 && <p className="app-text-secondary">{t.logEmpty}</p>}

        {rows.length > 0 && (
          <TableContainer className="log-table">
            <Table aria-label={t.logTitle} size="md">
              <TableHead>
                <TableRow>
                  <TableHeader>{t.logTime}</TableHeader>
                  <TableHeader>{t.logAction}</TableHeader>
                  {/* Only admins see other users' entries; for the others the column would be empty (#310). */}
                  {admin && <TableHeader>{t.logWho}</TableHeader>}
                  <TableHeader>{t.logTarget}</TableHeader>
                  <TableHeader>{t.logClient}</TableHeader>
                </TableRow>
              </TableHead>
              <TableBody>
                {rows.map((row) => {
                  const isOpen = expanded.has(row.entry.id);
                  const folded = `${id}-folded-${row.entry.id}`;
                  return (
                    <Fragment key={row.entry.id}>
                      <EntryRow
                        entry={row.entry}
                        admin={admin}
                        open={open}
                        fold={
                          row.folded.length > 0 && (
                            <Button
                              kind="ghost"
                              size="sm"
                              className="log-table__fold"
                              aria-expanded={isOpen}
                              aria-controls={isOpen ? row.folded.map((e) => `${folded}-${e.id}`).join(" ") : undefined}
                              onClick={() => toggle(row.entry.id)}
                            >
                              {isOpen ? t.logHideFolded : t.logFolded(row.folded.length)}
                            </Button>
                          )
                        }
                      />
                      {isOpen &&
                        row.folded.map((e) => <EntryRow key={e.id} id={`${folded}-${e.id}`} entry={e} admin={admin} open={open} nested />)}
                    </Fragment>
                  );
                })}
              </TableBody>
            </Table>
          </TableContainer>
        )}
        {cursor && (
          <div>
            <Button kind="tertiary" disabled={loading} onClick={() => void more()}>
              {t.logMore}
            </Button>
          </div>
        )}
        {loading && rows.length === 0 && (
          <TableSkeleton
            size="md"
            columns={[{ header: t.logTime }, { header: t.logAction }, ...(admin ? [{ header: t.logWho }] : []), { header: t.logTarget }, { header: t.logClient }]}
          />
        )}
      </div>
    </div>
  );
}

/** The link of a message, as the mail view's URL has it. */
const messageHref = (sha: string) => `/${formatState({ ...parseState(""), m: sha })}`;

function EntryRow({
  id,
  entry: e,
  admin,
  open,
  fold,
  nested = false,
}: {
  id?: string;
  entry: AuditEntry;
  admin: boolean;
  open: (sha: string) => void;
  fold?: ReactNode;
  nested?: boolean;
}) {
  const target = targetLabel(e);
  const cli = e.actor === "cli";
  // Without the Who column (not an admin) the command line goes into the detail line.
  const detail = [entryDetail(e), e.apiKey ? t.logViaKey(e.apiKey) : "", !admin && cli ? t.logCLI : ""].filter(Boolean);
  const failed = e.action === "auth.login_failed";
  const who = [e.user ?? "", cli ? t.logCLI : ""].filter(Boolean).join(" · ");
  const sha = e.target?.type === "message" && e.target.subject ? e.target.id : "";
  return (
    <TableRow id={id} className={nested ? "log-table__nested" : undefined}>
      <TableCell className="log-table__time app-tabular">{timestamp(e.at)}</TableCell>
      <TableCell>
        <span className="log-table__action">
          <span className={failed ? "app-text-error log-table__strong" : "log-table__strong"}>{t.auditAction(e.action)}</span>
          {fold}
        </span>
      </TableCell>
      {admin && <TableCell>{who}</TableCell>}
      <TableCell className="log-table__target">
        {target &&
          (sha ? (
            <Link
              href={messageHref(sha)}
              onClick={(ev: MouseEvent<HTMLAnchorElement>) => {
                if (!isPlainClick(ev)) return;
                ev.preventDefault();
                open(sha);
              }}
            >
              {target}
            </Link>
          ) : (
            <span className={e.target?.type === "message" ? "log-table__hash" : "app-break-words"}>{target}</span>
          ))}
        {detail.map((d) => (
          <span key={d} className="log-table__detail app-text-secondary app-break-words">
            {d}
          </span>
        ))}
      </TableCell>
      <TableCell className="log-table__client app-text-secondary app-text-small" title={e.userAgent ?? ""}>
        {[e.clientIp, e.userAgent ? shortUserAgent(e.userAgent) : ""].filter(Boolean).join(" · ")}
      </TableCell>
    </TableRow>
  );
}

