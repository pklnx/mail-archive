import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState, type FormEvent } from "react";
import {
  Accordion,
  AccordionItem,
  Button,
  Checkbox,
  Form,
  InlineNotification,
  Layer,
  NumberInput,
  Select,
  SelectItem,
  Tab,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableHeader,
  TableRow,
  TabList,
  TabPanel,
  TabPanels,
  Tabs,
  Tag,
  TextArea,
  TextInput,
  Tile,
  Toggle,
} from "@carbon/react";
import { Add } from "@carbon/icons-react";
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
import { AppModal, DangerModal } from "./DangerModal";
import { day, fileSize } from "./format";
import { t, translateWithId } from "./i18n";
import { parseQuery } from "./searchSyntax";

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

/** Whether a run is still open: proposed, or confirmed and being deleted. */
export function runOpen(r: Pick<DeletionRun, "state">): boolean {
  return r.state === "proposed" || r.state === "confirmed";
}

/**
 * Whether the typed count confirms a deletion run: the exact number, so an
 * empty field is not "0" and "2.0" or "1e1" are not a count.
 */
export function countMatches(typed: string, candidates: number): boolean {
  return typed.trim() === String(candidates);
}

const views = ["rules", "holds", "runs"] as const;

/** Retention rules, legal holds and deletion runs, for admins. */
export function RetentionPage() {
  const [view, setView] = useState<(typeof views)[number]>("rules");
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

  const status = (
    <>
      {error && (
        <p role="alert" className="app-text-error">
          {error}
        </p>
      )}
      {!data && !error && <p className="app-text-secondary">{t.loading}</p>}
    </>
  );

  return (
    <div className="accounts-page">
      <div className="page">
        <header className="page-header">
          <h1 className="page-header__title type-heading-04">{t.retentionTitle}</h1>
        </header>
        {/* Only the open tab is mounted: each one loads its data when it is shown. */}
        <Tabs selectedIndex={views.indexOf(view)} onChange={({ selectedIndex }) => setView(views[selectedIndex] ?? "rules")}>
          <TabList
            aria-label={t.retentionTitle}
            contained
            leftOverflowButtonProps={{ "aria-label": t.carbon.scrollLeft }}
            rightOverflowButtonProps={{ "aria-label": t.carbon.scrollRight }}
          >
            <Tab>{t.retentionRulesTab}</Tab>
            <Tab>{t.retentionHoldsTab}</Tab>
            <Tab>{t.runsTab}</Tab>
          </TabList>
          {/* A contained tab's panel is a layer of its own: the tiles and
              fields in it sit on the next one. */}
          <TabPanels>
            <TabPanel className="retention-panel">
              {view === "rules" && (
                <Layer>
                  {status}
                  {data && <RulesTab data={data} reload={reload} />}
                </Layer>
              )}
            </TabPanel>
            <TabPanel className="retention-panel">
              {view === "holds" && (
                <Layer>
                  {status}
                  {data && <HoldsTab accounts={data.accounts} />}
                </Layer>
              )}
            </TabPanel>
            <TabPanel className="retention-panel">
              {view === "runs" && (
                <Layer>
                  <RunsTab />
                </Layer>
              )}
            </TabPanel>
          </TabPanels>
        </Tabs>
      </div>
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
  // A period shorter than the legal minimum waits for its confirmation.
  const [short, setShort] = useState<number | null>(null);
  const [removing, setRemoving] = useState<{ id: number; what: string } | null>(null);
  const id = useId();
  const account = data.accounts.find((a) => String(a.id) === accountId);
  const n = Number(years);
  const tooShort = years !== "" && Number.isFinite(n) && n >= 1 && n < data.minYears;

  const send = async (n: number, force: boolean) => {
    if (!account) return;
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

  const add = (e: FormEvent) => {
    e.preventDefault();
    if (!account || busy) return;
    const n = Number(years);
    if (n < data.minYears) setShort(n);
    else void send(n, false);
  };

  const remove = async (id: number) => {
    setError("");
    try {
      await retentionApi.removeRule(id);
    } catch (err) {
      setError(message(err));
    }
    reload();
  };

  return (
    <section className="retention-section">
      <p className="app-text-secondary">{t.retentionNote}</p>
      <p className="app-text-secondary">{t.retentionLegal}</p>
      {data.rules.length === 0 ? (
        <p>{t.ruleNone}</p>
      ) : (
        <ul className="retention-list">
          {data.rules.map((r) => {
            const what = `${accountLabel({ owner: r.owner, name: r.account, removed: r.accountRemoved })} / ${r.folder || t.ruleAllFolders}`;
            return (
              <li key={r.id}>
                <Tile className="retention-tile">
                  <div className="retention-tile__body">
                    <h2 className="retention-tile__name">{what}</h2>
                    <p>
                      {t.ruleYearsCount(r.years)} · {r.timeZone}
                    </p>
                    {r.note && <p className="app-text-secondary app-break-words">{r.note}</p>}
                    <p className="app-text-secondary app-text-small">{t.ruleCreated(r.createdBy, day(r.createdAt))}</p>
                  </div>
                  <div className="retention-tile__actions">
                    <Button kind="danger--tertiary" size="sm" onClick={() => setRemoving({ id: r.id, what })}>
                      {t.ruleRemove}
                    </Button>
                  </div>
                </Tile>
              </li>
            );
          })}
        </ul>
      )}
      {error && (
        <p role="alert" className="app-text-error">
          {error}
        </p>
      )}

      <section className="page__section retention-form">
        <h2 className="type-heading-03">{t.ruleAdd}</h2>
        <Form onSubmit={add} className="page-form">
          <div className="form-row form-row--two">
            <Select
              id={`${id}-account`}
              labelText={t.ruleAccount}
              value={accountId}
              required
              onChange={(e) => {
                setAccountId(e.target.value);
                setFolder("");
              }}
            >
              <SelectItem value="" text="" />
              {data.accounts.map((a) => (
                <SelectItem key={a.id} value={String(a.id)} text={accountLabel(a)} />
              ))}
            </Select>
            <Select id={`${id}-folder`} labelText={t.ruleFolder} value={folder} disabled={!account} onChange={(e) => setFolder(e.target.value)}>
              <SelectItem value="" text={t.ruleAllFolders} />
              {account?.folders.map((f) => (
                <SelectItem key={f} value={f} text={f} />
              ))}
            </Select>
          </div>
          <div className="form-row form-row--two">
            <NumberInput
              id={`${id}-years`}
              label={t.ruleYears}
              min={1}
              max={100}
              allowEmpty
              required
              value={years}
              invalidText={t.ruleYearsInvalid}
              warn={tooShort}
              warnText={t.ruleShortWarn(data.minYears)}
              translateWithId={translateWithId}
              onChange={(_, { value }) => setYears(String(value))}
            />
            <TextInput id={`${id}-tz`} labelText={t.ruleTimeZone} value={timeZone} required onChange={(e) => setTimeZone(e.target.value)} />
          </div>
          <TextInput
            id={`${id}-note`}
            labelText={t.ruleNote}
            value={note}
            maxLength={500}
            placeholder={t.ruleNotePlaceholder}
            onChange={(e) => setNote(e.target.value)}
          />
          <div className="form-actions">
            <Button type="submit" kind="primary" renderIcon={Add} disabled={busy || !account}>
              {t.ruleAdd}
            </Button>
          </div>
        </Form>
      </section>

      {short !== null && (
        <DangerModal
          size="sm"
          modalHeading={t.ruleShortTitle}
          primaryButtonText={t.ruleAddAnyway}
          secondaryButtonText={t.cancel}
          onRequestClose={() => setShort(null)}
          onRequestSubmit={() => {
            const n = short;
            setShort(null);
            void send(n, true);
          }}
        >
          <p>{t.ruleShort(short, data.minYears)}</p>
        </DangerModal>
      )}
      {removing && (
        <DangerModal
          size="sm"
          modalHeading={t.ruleRemoveTitle(removing.what)}
          primaryButtonText={t.ruleRemove}
          secondaryButtonText={t.cancel}
          onRequestClose={() => setRemoving(null)}
          onRequestSubmit={() => {
            const { id } = removing;
            setRemoving(null);
            void remove(id);
          }}
        >
          <p>{t.ruleRemoveText}</p>
        </DangerModal>
      )}
    </section>
  );
}

function HoldsTab({ accounts }: { accounts: RetentionAccount[] }) {
  const [holds, setHolds] = useState<LegalHold[] | null>(null);
  const [released, setReleased] = useState(false);
  const [users, setUsers] = useState<ManagedUser[]>([]);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [releasing, setReleasing] = useState<LegalHold | null>(null);
  const id = useId();

  const reload = useCallback(() => {
    retentionApi.holds(released).then(setHolds, (err: unknown) => setError(message(err)));
  }, [released]);
  useEffect(reload, [reload]);
  useEffect(() => {
    usersApi.list().then(setUsers, () => setUsers([]));
  }, []);

  const release = async (h: LegalHold, note: string) => {
    setError("");
    try {
      await retentionApi.releaseHold(h.id, note);
    } catch (err) {
      setError(message(err));
    }
    reload();
  };

  return (
    <section className="retention-section">
      <p className="app-text-secondary">{t.holdNote}</p>
      <Toggle
        id={`${id}-released`}
        size="sm"
        labelText={t.holdShowReleased}
        labelA={t.carbon.toggleOff}
        labelB={t.carbon.toggleOn}
        toggled={released}
        onToggle={setReleased}
      />
      {error && (
        <p role="alert" className="app-text-error">
          {error}
        </p>
      )}
      {notice && (
        <InlineNotification
          kind="success"
          lowContrast
          hideCloseButton
          role="status"
          className="notification--full"
          statusIconDescription={t.carbon.status.success}
          title={notice}
        />
      )}
      {holds && holds.length === 0 && <p>{t.holdNone}</p>}
      {holds && holds.length > 0 && (
        <ul className="retention-list">
          {holds.map((h) => (
            <li key={h.id}>
              <Tile className={`retention-tile${h.releasedAt ? " retention-tile--off" : ""}`}>
                <div className="retention-tile__body">
                  <div className="retention-tile__head">
                    <h2 className="retention-tile__name">{h.name}</h2>
                    {h.releasedAt && (
                      <Tag size="sm" type="gray">
                        {t.holdReleasedTag}
                      </Tag>
                    )}
                  </div>
                  <p className="app-break-words">{holdScope(h)}</p>
                  {h.reason && <p className="app-text-secondary app-break-words">{h.reason}</p>}
                  <p className="app-text-secondary app-text-small app-break-words">
                    {h.releasedAt ? t.holdReleased(h.releasedBy, day(h.releasedAt), h.releaseNote) : t.ruleCreated(h.createdBy, day(h.createdAt))}
                  </p>
                </div>
                {!h.releasedAt && (
                  <div className="retention-tile__actions">
                    <Button kind="danger--tertiary" size="sm" onClick={() => setReleasing(h)}>
                      {t.holdRelease}
                    </Button>
                  </div>
                )}
              </Tile>
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
      {releasing && (
        <ReleaseHoldModal
          hold={releasing}
          cancel={() => setReleasing(null)}
          confirm={(note) => {
            const h = releasing;
            setReleasing(null);
            void release(h, note);
          }}
        />
      )}
    </section>
  );
}

/** Releasing a hold asks for the note that is kept with it; an empty note sends nothing. */
export function ReleaseHoldModal({ hold, cancel, confirm }: { hold: LegalHold; cancel: () => void; confirm: (note: string) => void }) {
  const [note, setNote] = useState("");
  const id = useId();
  const ok = note.trim() !== "";
  const submit = () => {
    if (ok) confirm(note.trim());
  };
  return (
    <DangerModal
      size="sm"
      modalHeading={t.holdReleaseTitle(hold.name)}
      primaryButtonText={t.holdRelease}
      primaryButtonDisabled={!ok}
      secondaryButtonText={t.cancel}
      onRequestClose={cancel}
      onRequestSubmit={submit}
    >
      <div className="modal-stack">
        <p>{t.holdReleaseText}</p>
        <TextArea
          id={`${id}-note`}
          data-modal-primary-focus=""
          labelText={t.holdReleaseNote}
          rows={3}
          required
          value={note}
          onChange={(e) => setNote(e.target.value)}
        />
      </div>
    </DangerModal>
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
  const id = useId();
  const p = parseQuery(query);
  const filter = p.text || p.from || p.to || p.attachment || p.hasAttachment
    ? { q: p.text, from: p.from, to: p.to, attachment: p.attachment, hasAttachment: p.hasAttachment }
    : null;
  const unscoped = !user && !accountId && !filter && !p.after && !p.before;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (busy || (unscoped && !all)) return;
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
    <section className="page__section retention-form">
      <h2 className="type-heading-03">{t.holdAdd}</h2>
      <Form onSubmit={(e) => void submit(e)} className="page-form">
        <div className="form-row form-row--two">
          <TextInput id={`${id}-name`} labelText={t.holdName} value={name} maxLength={200} required onChange={(e) => setName(e.target.value)} />
          <TextInput id={`${id}-reason`} labelText={t.holdReason} value={reason} maxLength={2000} required onChange={(e) => setReason(e.target.value)} />
        </div>
        <div className="form-row form-row--two">
          <Select id={`${id}-user`} labelText={t.holdUser} value={user} onChange={(e) => setUser(e.target.value)}>
            <SelectItem value="" text={t.holdAnyUser} />
            {users.map((u) => (
              <SelectItem key={u.name} value={u.name} text={u.name} />
            ))}
          </Select>
          <Select id={`${id}-account`} labelText={t.holdAccount} value={accountId} onChange={(e) => setAccountId(e.target.value)}>
            <SelectItem value="" text={t.holdAnyAccount} />
            {accounts.map((a) => (
              <SelectItem key={a.id} value={String(a.id)} text={accountLabel(a)} />
            ))}
          </Select>
        </div>
        <TextInput id={`${id}-filter`} labelText={t.holdFilter} helperText={t.holdFilterHint} value={query} onChange={(e) => setQuery(e.target.value)} />
        {unscoped && (
          <Checkbox
            id={`${id}-all`}
            labelText={t.holdAll}
            helperText={t.holdAllNeeded}
            checked={all}
            onChange={(_, { checked }) => setAll(checked)}
          />
        )}
        {error && (
          <p role="alert" className="app-text-error">
            {error}
          </p>
        )}
        <div className="form-actions">
          <Button type="submit" kind="primary" renderIcon={Add} disabled={busy || (unscoped && !all)}>
            {t.holdAdd}
          </Button>
        </div>
      </Form>
    </section>
  );
}

const runTag = { proposed: "blue", confirmed: "magenta", done: "green", cancelled: "gray" } as const;

function RunsTab() {
  const [runs, setRuns] = useState<DeletionRun[] | null>(null);
  const [selected, setSelected] = useState<number | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const reviewButtons = useRef(new Map<number, HTMLButtonElement>());

  // The runs as loaded, or null when loading them failed.
  const reload = useCallback(
    () =>
      retentionApi.runs().then(
        (r) => {
          setRuns(r);
          setError("");
          return r;
        },
        (err: unknown) => {
          setError(message(err));
          return null;
        },
      ),
    [],
  );
  useEffect(() => {
    void reload();
  }, [reload]);
  // A confirmed run deletes in the background: poll until it is done.
  const deleting = runs?.some((r) => r.state === "confirmed") ?? false;
  useEffect(() => {
    if (!deleting) return;
    const id = setInterval(() => void reload(), 3000);
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
    void reload();
  };

  const open = runs?.find(runOpen);
  const shown = runs?.find((r) => r.id === selected);
  return (
    <section className="retention-section">
      <p className="app-text-secondary">{t.runsNote}</p>
      <p className="app-text-secondary">{t.runsCLI}</p>
      {!open && (
        <div>
          <Button kind="primary" renderIcon={Add} disabled={busy || !runs} onClick={() => void propose()}>
            {t.runPropose}
          </Button>
        </div>
      )}
      {error && (
        <p role="alert" className="app-text-error">
          {error}
        </p>
      )}
      {runs && runs.length === 0 && <p>{t.runNone}</p>}
      {runs && runs.length > 0 && (
        <TableContainer className="runs-table">
          <Table aria-label={t.runsTab} size="md">
            <TableHead>
              <TableRow>
                <TableHeader>{t.runColRun}</TableHeader>
                <TableHeader>{t.runColState}</TableHeader>
                <TableHeader>{t.runColResult}</TableHeader>
                <TableHeader>{t.runColProposed}</TableHeader>
                <TableHeader>
                  <span className="cds--visually-hidden">{t.colActions}</span>
                </TableHeader>
              </TableRow>
            </TableHead>
            <TableBody>
              {runs.map((r) => (
                <TableRow key={r.id}>
                  <TableCell>{t.runLine(r.id, r.candidates, fileSize(r.bytes))}</TableCell>
                  <TableCell>
                    <Tag size="sm" type={runTag[r.state as keyof typeof runTag] ?? "gray"}>
                      {t.runState(r.state)}
                    </Tag>
                  </TableCell>
                  <TableCell>{r.state === "done" || r.deleted + r.kept > 0 ? t.runResult(r.deleted, r.kept) : ""}</TableCell>
                  <TableCell className="app-text-secondary">{t.runProposedBy(r.createdBy, day(r.createdAt))}</TableCell>
                  <TableCell className="runs-table__action">
                    <Button
                      ref={(el: HTMLButtonElement | null) => {
                        if (el) reviewButtons.current.set(r.id, el);
                        else reviewButtons.current.delete(r.id);
                      }}
                      kind={r.state === "proposed" ? "tertiary" : "ghost"}
                      size="sm"
                      onClick={() => setSelected(r.id)}
                    >
                      {t.runShow}
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
      {selected !== null && (
        <RunReview
          key={selected}
          id={selected}
          state={shown?.state}
          title={shown ? t.runLine(shown.id, shown.candidates, fileSize(shown.bytes)) : t.runShow}
          returnFocus={() => reviewButtons.current.get(selected)}
          changed={reload}
          close={() => setSelected(null)}
        />
      )}
    </section>
  );
}

/** The states a run must reach for an action to have done what was asked. */
const reached = { confirm: ["confirmed", "done"], cancel: ["cancelled"] } as const;

/**
 * The review of a deletion run in a Modal: the groups, the hashes and, for a
 * proposed run, the typed-count confirmation and Cancel run. Delete is
 * enabled only by the exact count. Closing the Modal changes nothing.
 * state is the run's state in the polled list: the review loads again when
 * it changes, so a deletion that finishes shows its hashes' states.
 */
export function RunReview({
  id,
  state,
  title,
  returnFocus,
  changed,
  close,
}: {
  id: number;
  state?: string;
  title: string;
  returnFocus?: () => HTMLElement | null | undefined;
  changed: () => Promise<DeletionRun[] | null>;
  close: () => void;
}) {
  const [review, setReview] = useState<DeletionReview | null>(null);
  const [count, setCount] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  // After an action whose outcome could not be loaded: nothing more is sent.
  const [unknown, setUnknown] = useState(false);
  // The run's state after an action that did not reach it, when it changed elsewhere.
  const [nowState, setNowState] = useState<string | null>(null);
  const field = useRef<HTMLInputElement>(null);
  const fieldId = useId();

  useEffect(() => {
    let current = true;
    retentionApi.run(id).then(
      (r) => {
        if (current) setReview(r);
      },
      (err: unknown) => {
        if (current) setError((e) => e || message(err));
      },
    );
    return () => {
      current = false;
    };
  }, [id, state]);

  const run = review?.run;
  const proposed = run?.state === "proposed";
  const confirmable = proposed && run.candidates > 0;
  const matches = confirmable && !unknown && countMatches(count, run.candidates);
  useLayoutEffect(() => {
    if (confirmable) field.current?.focus();
  }, [confirmable]);

  // After Delete or Cancel run the list is loaded again, and the run's new
  // state decides: the server answers a confirm with 202 and no body, which
  // the client reads as an error (api.ts is unchanged in this part). The
  // review closes only when the run reached the state of the chosen action:
  // confirmed or done after Delete, cancelled after Cancel run. Otherwise it
  // stays open with the error and the state the run has now, for example
  // when another admin confirmed it while this one cancelled it. When the
  // state cannot be loaded, it says so and sends nothing more.
  const act = async (kind: keyof typeof reached, action: () => Promise<unknown>) => {
    if (busy || unknown || !run) return;
    setBusy(true);
    setError("");
    setNowState(null);
    let failed = "";
    try {
      await action();
    } catch (err) {
      failed = message(err);
    }
    const runs = await changed();
    const now = runs?.find((r) => r.id === run.id);
    setBusy(false);
    if (now && (reached[kind] as readonly string[]).includes(now.state)) {
      close();
      return;
    }
    if (!now) {
      setUnknown(true);
      setError(t.runStateUnknown);
      return;
    }
    setError(failed);
    if (now.state !== "proposed" || !failed) setNowState(now.state);
  };
  // The error, and the state the run has now if it changed elsewhere; that
  // line follows the polled state (confirmed, later done).
  // A run never goes back to proposed, so a polled "proposed" is older than nowState.
  const nowLine = nowState ? t.runNowState(t.runState(state && state !== "proposed" ? state : nowState)) : "";
  const shownError = [error, nowLine].filter(Boolean).join(" ");
  const confirm = () => {
    if (run && matches && !busy) void act("confirm", () => retentionApi.confirm(run.id, Number(count.trim())));
  };
  const cancelRun = () => {
    if (run && proposed && !busy && !unknown) void act("cancel", () => retentionApi.cancel(run.id));
  };
  const heading = run ? `${t.runLine(run.id, run.candidates, fileSize(run.bytes))} · ${t.runState(run.state)}` : title;

  const footer = !proposed
    ? { passiveModal: true }
    : confirmable
      ? {
          primaryButtonText: t.runConfirm,
          primaryButtonDisabled: !matches,
          secondaryButtons: [
            { buttonText: t.close, onClick: () => !busy && close() },
            { buttonText: t.runCancel, onClick: cancelRun },
          ],
          onRequestSubmit: confirm,
        }
      : {
          primaryButtonText: t.runCancel,
          primaryButtonDisabled: unknown,
          secondaryButtonText: t.close,
          onRequestSubmit: cancelRun,
        };

  return (
    <AppModal
      danger={confirmable}
      size="md"
      aria-label={heading}
      modalHeading={heading}
      hasScrollingContent
      preventCloseOnClickOutside
      returnFocus={returnFocus}
      loadingStatus={busy ? "active" : "inactive"}
      loadingDescription={t.loading}
      loadingIconDescription={t.loading}
      onRequestClose={() => {
        if (!busy) close();
      }}
      {...footer}
    >
      <div className="modal-stack">
        {!review && !error && <p className="app-text-secondary">{t.loading}</p>}
        {review && review.groups.length > 0 && (
          <TableContainer>
            <Table size="sm" aria-label={t.runGroupsHead}>
              <TableHead>
                <TableRow>
                  <TableHeader>{t.runGroupsHead}</TableHeader>
                  <TableHeader>{t.runYear}</TableHeader>
                  <TableHeader className="runs-table__number">{t.runMessages}</TableHeader>
                </TableRow>
              </TableHead>
              <TableBody>
                {review.groups.map((g) => (
                  <TableRow key={`${g.owner}/${g.account}/${g.folder}/${g.year}`}>
                    <TableCell className="app-break-all">
                      {g.owner || "-"} / {g.account} / {g.folder}
                    </TableCell>
                    <TableCell className="app-tabular">{g.year}</TableCell>
                    <TableCell className="app-tabular runs-table__number">{g.messages}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </TableContainer>
        )}
        {review && run && review.candidates.length > 0 && (
          <Accordion>
            <AccordionItem title={t.runHashes(review.listed, run.candidates)}>
              <ul className="run-hashes">
                {review.candidates.map((c) => (
                  <li key={c.sha256} className={c.state === "kept" ? "app-text-secondary" : undefined}>
                    {c.sha256} {c.state !== "pending" && `(${c.state})`}
                  </li>
                ))}
              </ul>
            </AccordionItem>
          </Accordion>
        )}
        {proposed && run.candidates === 0 && <p>{t.runEmpty}</p>}
        {confirmable && (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              confirm();
            }}
          >
            <TextInput
              id={`${fieldId}-count`}
              ref={field}
              data-modal-primary-focus=""
              className="purge__count"
              labelText={t.runConfirmPrompt(run.candidates)}
              inputMode="numeric"
              autoComplete="off"
              value={count}
              onChange={(e) => setCount(e.target.value)}
            />
          </form>
        )}
        {shownError && (
          <p role="alert" className="app-text-error">
            {shownError}
          </p>
        )}
      </div>
    </AppModal>
  );
}
