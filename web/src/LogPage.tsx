import { Fragment, useEffect, useId, useMemo, useState, type KeyboardEvent, type MouseEvent, type ReactNode } from "react";
import {
  Button,
  Link,
  MultiSelect,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableHeader,
  TableRow,
} from "@carbon/react";
import { DateRange } from "./DateRange";
import { auditApi, auditQuery, usersApi, type AuditEntry, type AuditFilter, type ManagedUser } from "./api";
import { foldEntries, shortUserAgent } from "./auditLog";
import { isPlainClick } from "./clicks";
import { dayOfDate, shiftDay, timestamp } from "./format";
import { t, translateWithId } from "./i18n";
import { TableSkeleton } from "./tableSkeleton";
import { auditActors, auditGroups, formatState, parseState, useViewState, type ViewState } from "./urlState";

export { auditGroups };

/** The default period: the last 30 local days. */
export function defaultFilter(now = new Date()): AuditFilter {
  return { since: shiftDay(dayOfDate(now), -30), until: "", actions: [], users: [], actors: [] };
}

/**
 * The filter of the page address (#376): without since= the default period;
 * users and actors only for admins, the others ignore them.
 */
export function filterOf(s: ViewState, admin: boolean, now = new Date()): AuditFilter {
  return {
    since: s.since ?? defaultFilter(now).since,
    until: s.until,
    actions: s.action,
    users: admin ? s.user : [],
    actors: admin ? s.actor : [],
  };
}

/**
 * An entry of a filter's list: an action group, an actor or a user.
 *
 * The shown text must not be in a field called `text` (#478). Carbon's
 * MultiSelect (1.119.0, es/components/MultiSelect/MultiSelect.js:292,318-331)
 * writes `item.text` of every chosen item, without separators, into a visually
 * hidden span in the field's <label>, which names the combobox button. The
 * button's content is our comma-separated `label` with the same values, so a
 * screen reader read each value twice. LogPage.test.tsx and e2e/log.spec.ts
 * fail if Carbon starts reading another field.
 */
interface Choice {
  kind: "group" | "actor" | "user";
  id: string;
  name: string;
}
/** The lists keep their order: groups as in the API, then actors, then users. */
const keepOrder = (items: Choice[]) => items;
const choiceText = (c: Choice | null) => c?.name ?? "";

/** A filter of several values: the chosen names joined, or allText when none is chosen (#376). */
function ChoiceFilter({
  id,
  label,
  allText,
  items,
  chosen,
  onChange,
}: {
  id: string;
  label: string;
  allText: string;
  items: Choice[];
  chosen: Choice[];
  onChange: (chosen: Choice[]) => void;
}) {
  // Escape on the closed field leaves it and keeps the choice, as everywhere
  // else in the app (#311, #411). Carbon's MultiSelect clears the selection on
  // Escape with the menu closed (as on Delete/Backspace), and App.tsx's global
  // listener leaves the trigger alone: it is a role="combobox" button, which
  // ownedByWidget (dialogs.ts) counts as a widget, and it blurs only inputs.
  // The capture phase runs before Carbon's handler on the button. With the
  // menu open, Escape is Carbon's and only closes it; Delete, Backspace and
  // the clear button still clear the choice.
  const leaveOnEscape = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key !== "Escape" || !(e.target instanceof HTMLElement)) return;
    const trigger = e.target.closest<HTMLElement>('[role="combobox"]');
    if (!trigger || trigger.getAttribute("aria-expanded") === "true") return;
    e.stopPropagation();
    e.preventDefault();
    trigger.blur();
  };
  return (
    // display: contents keeps the field the grid cell of .log-filters.
    <div style={{ display: "contents" }} onKeyDownCapture={leaveOnEscape}>
      <MultiSelect<Choice>
        id={id}
        titleText={label}
        label={chosen.length ? chosen.map((c) => c.name).join(", ") : allText}
        items={items}
        itemToString={choiceText}
        selectedItems={chosen}
        sortItems={keepOrder}
        selectionFeedback="fixed"
        translateWithId={translateWithId}
        clearSelectionDescription={t.carbon.selection.description}
        clearSelectionText={t.carbon.selection.clear}
        clearAnnouncement={t.carbon.selection.cleared}
        onChange={({ selectedItems }) => {
          const picked = selectedItems ?? [];
          // In the list's order, so the address does not follow the order of the clicks.
          onChange(items.filter((i) => picked.some((p) => p.kind === i.kind && p.id === i.id)));
        }}
      />
    </div>
  );
}

/** The target of an entry in words: the subject of a visible message, otherwise a name or the start of the hash. */
export function targetLabel(e: AuditEntry): string {
  if (!e.target) return "";
  if (e.target.subject) return e.target.subject;
  if (e.target.name) return e.target.name;
  return e.target.type === "message" ? `${e.target.id.slice(0, 12)}…` : e.target.id;
}

/** The list entries: a search, a list without search parameters, a further page (#389). */
const listActions = ["message.search", "message.list", "message.page"];

/** A list's parameters: the search text quoted, then the others as key:value. */
function listParams(d: Record<string, unknown>): string {
  const q = typeof d.q === "string" ? d.q : "";
  const rest = Object.entries(d)
    .filter(([k, v]) => k !== "q" && typeof v === "string" && v !== "")
    .map(([k, v]) => `${k}:${String(v)}`);
  return [q ? `“${q}”` : "", ...rest].filter(Boolean).join(" ");
}

/** The viewer's accounts an entry of a shared read is about, by name ("demo/info" is the viewer's "info"), with n. */
function viaAccounts(d: Record<string, unknown>): { name: string; n: number }[] {
  const list = Array.isArray(d.via_accounts) ? (d.via_accounts as unknown[]) : [];
  return list.flatMap((v) => {
    if (!v || typeof v !== "object") return [];
    const { key, n } = v as { key?: unknown; n?: unknown };
    if (typeof key !== "string") return [];
    return [{ name: key.slice(key.indexOf("/") + 1), n: typeof n === "number" ? n : 0 }];
  });
}

/**
 * The detail of another user's read of the viewer's shared account (#389,
 * revision 2, R6): "pat's search showed 2 messages of info", from search
 * and via_accounts. Search terms only on a named first page, the one entry
 * that keeps them (decision 13); never on other lists, also if they came.
 */
function sharedDetail(e: AuditEntry): string {
  const d = e.details;
  const user = e.user ?? "";
  const via = viaAccounts(d);
  if (listActions.includes(e.action)) {
    const named = e.action !== "message.page" && typeof d.account === "string";
    // A first page is a search or a list by its action; a further page says so in search.
    const kind = e.action === "message.search" || (e.action === "message.page" && d.search === true) ? "search" : "list";
    return [named ? listParams(d) : "", ...via.map((a) => t.logShared(user, kind, a.n, a.name))].filter(Boolean).join("; ");
  }
  if (e.action === "message.conversation") return via.map((a) => t.logShared(user, "conversation", a.n, a.name)).join("; ");
  return via.map((a) => t.logSharedIn(a.name)).join("; ");
}

/** The detail worth a glance: the search text, a login's method and reason, changed settings. */
export function entryDetail(e: AuditEntry): string {
  const d = e.details;
  if (e.viaShare) return sharedDetail(e);
  if (listActions.includes(e.action)) return listParams(d);
  if ((e.action === "account.share" || e.action === "account.unshare") && typeof d.user === "string") return t.logShareWith(d.user);
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
  const [state, update] = useViewState();
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [cursor, setCursor] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  // null while the list loads; "failed" if it could not be loaded.
  const [users, setUsers] = useState<ManagedUser[] | "failed" | null>(null);
  const [expanded, setExpanded] = useState<Set<number>>(new Set());
  const id = useId();

  useEffect(() => {
    if (admin) usersApi.list().then(setUsers, () => setUsers("failed"));
  }, [admin]);

  // The filter as sent. Once the user list is there, names of users that are
  // gone are dropped, so an old bookmark shows the others and not a 404; if
  // the list failed, the address is sent as it is (#376 plan review). An
  // admin's request with users waits for the list: one request, not two.
  const known = Array.isArray(users) ? new Set(users.map((u) => u.name)) : null;
  const wanted = filterOf(state, admin);
  const filter = known ? { ...wanted, users: wanted.users.filter((u) => known.has(u)) } : wanted;
  const waiting = admin && users === null && wanted.users.length > 0;
  const query = auditQuery(filter);

  // The address follows: dropped users and values the page ignores go, as a replace.
  const pruned = JSON.stringify(filter.users);
  useEffect(() => {
    if (Array.isArray(users)) update({ user: JSON.parse(pruned) as string[] }, { replace: true });
  }, [users, pruned, update]);

  useEffect(() => {
    if (waiting) return;
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
    // The query string, not the filter object, which is new on every render.
  }, [query, waiting]);

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
  // Filter changes replace the history entry, like the search filters.
  const set = (patch: Partial<ViewState>) => update(patch, { replace: true });
  const groupItems: Choice[] = auditGroups.map((g) => ({ kind: "group", id: g, name: t.logGroup(g) }));
  // Until the list is there (or if it failed), the chosen names stand for themselves.
  const names = known ? [...known] : filter.users;
  const whoItems: Choice[] = [
    ...auditActors.map((a): Choice => ({ kind: "actor", id: a, name: a === "cli" ? t.logCommandLine : t.logApiKeys })),
    ...names.map((n): Choice => ({ kind: "user", id: n, name: n })),
  ];
  const chosenWho = whoItems.filter((i) => (i.kind === "actor" ? filter.actors : filter.users).includes(i.id));
  // Who: admins see everyone's entries; other users when the page has
  // other users' reads of their shared accounts (#389).
  const who = admin || entries.some((e) => e.viaShare);

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
            // The default From is not written, so a bookmark keeps the last 30 days.
            onChange={(since, until) => set({ since: since === defaultFilter().since ? null : since, until })}
          />
          <ChoiceFilter
            id={`${id}-action`}
            label={t.logAction}
            allText={t.logAllActions}
            items={groupItems}
            chosen={groupItems.filter((i) => filter.actions.includes(i.id))}
            onChange={(c) => set({ action: c.map((i) => i.id) })}
          />
          {admin && (
            <ChoiceFilter
              id={`${id}-who`}
              label={t.logWho}
              allText={t.logEveryone}
              items={whoItems}
              chosen={chosenWho}
              onChange={(c) => set({ actor: c.filter((i) => i.kind === "actor").map((i) => i.id), user: c.filter((i) => i.kind === "user").map((i) => i.id) })}
            />
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
                  {/* Without other users' entries the column would be empty (#310). */}
                  {who && <TableHeader>{t.logWho}</TableHeader>}
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
                        who={who}
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
                        row.folded.map((e) => <EntryRow key={e.id} id={`${folded}-${e.id}`} entry={e} admin={admin} who={who} open={open} nested />)}
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
  who: showWho,
  open,
  fold,
  nested = false,
}: {
  id?: string;
  entry: AuditEntry;
  admin: boolean;
  /** The Who column is shown. */
  who: boolean;
  open: (sha: string) => void;
  fold?: ReactNode;
  nested?: boolean;
}) {
  const target = targetLabel(e);
  const cli = e.actor === "cli";
  // Without the Who column (not an admin) the command line goes into the detail line.
  // A shared read keeps the actor, not the key's name (decision 16).
  const key = e.apiKey ? t.logViaKey(e.apiKey) : e.viaShare && e.actor === "api_key" ? t.logSharedViaKey : "";
  const detail = [entryDetail(e), key, !admin && cli ? t.logCLI : ""].filter(Boolean);
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
      {showWho && <TableCell>{who}</TableCell>}
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

