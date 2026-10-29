import { Fragment, useEffect, useId, useLayoutEffect, useRef, useState, type ReactNode, type RefObject } from "react";
import {
  Accordion,
  AccordionItem,
  Button,
  InlineLoading,
  MenuItem,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableHeader,
  TableRow,
  Tag,
  TextInput,
} from "@carbon/react";
import { retentionApi, type DeletionReview, type DeletionRun } from "./api";
import { DangerModal } from "./DangerModal";
import { day, fileSize } from "./format";
import { t } from "./i18n";
import { DetailHeader, RowMenu, SummaryTiles } from "./settingsParts";
import { settingsText } from "./settingsText";

const message = (err: unknown) => t.failed(err instanceof Error ? err.message : String(err));

/**
 * Whether the typed count confirms a deletion run: the exact number, so an
 * empty field is not "0" and "2.0" or "1e1" are not a count.
 */
export function countMatches(typed: string, candidates: number): boolean {
  return typed.trim() === String(candidates);
}

export const runTag = { proposed: "blue", confirmed: "magenta", done: "green", cancelled: "gray" } as const;

export function RunStateTag({ state }: { state: string }) {
  return (
    <Tag size="sm" type={runTag[state as keyof typeof runTag] ?? "gray"}>
      {t.runState(state)}
    </Tag>
  );
}

/**
 * A path of a group (owner / account / folder) that wraps at each "/": a
 * <wbr> after every slash, and no break inside a name (#310).
 */
export function PathText({ text }: { text: string }) {
  return (
    <>
      {text.split("/").map((part, i) => (
        <Fragment key={i}>
          {i > 0 && (
            <>
              /<wbr />
            </>
          )}
          {part}
        </Fragment>
      ))}
    </>
  );
}

/** The states a run must reach for an action to have done what was asked. */
const reached = { confirm: ["confirmed", "done"], cancel: ["cancelled"] } as const;

export interface RunPageProps {
  id: number;
  /** The run's state in the polled list: the review loads again when it changes. */
  state?: string;
  /** The run as listed, for the title until the review is loaded. */
  listed?: DeletionRun;
  heading: RefObject<HTMLHeadingElement | null>;
  parent: { label: string; href: string; go: () => void };
  /** Loads the runs again; null when that failed. */
  changed: () => Promise<DeletionRun[] | null>;
}

/**
 * A deletion run's page (#285 part 3): the title with the state, the counts,
 * the groups and the hashes. A proposed run with candidates has Delete,
 * which asks for the typed count in a danger Modal; Cancel run is in the
 * overflow menu and asks nothing more. Loading the page moves no focus.
 */
export function RunPage({ id, state, listed, heading, parent, changed }: RunPageProps) {
  const s = settingsText();
  const [review, setReview] = useState<DeletionReview | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState<keyof typeof reached | null>(null);
  // After an action whose outcome could not be loaded: nothing more is sent.
  const [unknown, setUnknown] = useState(false);
  // The run's state after an action that did not reach it, when it changed elsewhere.
  const [nowState, setNowState] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);
  // Counts the actions that ended outside the dialog: the focus goes to the h1.
  const [ended, setEnded] = useState(0);
  const deleteButton = useRef<HTMLButtonElement>(null);

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
  // After the dialog closed (its cleanup has run in this commit), or after
  // Cancel run: the h1, as the control that started it may be gone.
  useLayoutEffect(() => {
    if (ended > 0) heading.current?.focus();
  }, [ended, heading]);

  const run = review?.run;
  const proposed = run?.state === "proposed";
  const confirmable = proposed && run.candidates > 0;

  // After Delete or Cancel run the list is loaded again, and the run's new
  // state decides: the server answers a confirm with 202 and no body, which
  // the client reads as an error (api.ts is unchanged in this part). The
  // action is done only when the run reached the state of the chosen action:
  // confirmed or done after Delete, cancelled after Cancel run. Otherwise
  // the error shows with the state the run has now, for example when
  // another admin confirmed it while this one cancelled it. When the state
  // cannot be loaded, it says so and sends nothing more.
  const act = async (kind: keyof typeof reached, action: () => Promise<unknown>) => {
    if (busy || unknown || !run) return;
    setBusy(kind);
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
    setBusy(null);
    if (kind === "cancel") setEnded((n) => n + 1);
    if (now && (reached[kind] as readonly string[]).includes(now.state)) {
      if (kind === "confirm") {
        setDeleting(false);
        setEnded((n) => n + 1);
      }
      return;
    }
    if (!now) {
      setUnknown(true);
      setError(s.runStateUnknown);
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
  const cancelRun = () => {
    if (run && proposed && !busy && !unknown) void act("cancel", () => retentionApi.cancel(run.id));
  };

  const shown = run ?? listed;
  const title = shown ? t.runLine(shown.id, shown.candidates, fileSize(shown.bytes)) : t.runsTab;
  const alert = (text: string) => (
    <p role="alert" className="app-text-error">
      {text}
    </p>
  );

  return (
    <>
      <DetailHeader
        parent={parent}
        title={title}
        heading={heading}
        tags={shown && <RunStateTag state={shown.state} />}
        actions={
          confirmable && (
            <Button ref={deleteButton} kind="danger" disabled={!!busy || unknown} onClick={() => setDeleting(true)}>
              {s.deleteCount(run.candidates)}
            </Button>
          )
        }
        menu={
          proposed && (
            <RowMenu label={t.userActions(title)}>
              <MenuItem label={t.runCancel} disabled={!!busy || unknown} onClick={cancelRun} />
            </RowMenu>
          )
        }
      />
      {busy === "cancel" && <InlineLoading className="inline-loading--auto" status="active" description={t.runCancel} iconDescription={t.loading} />}
      {/* Under the title row, while no dialog shows it. */}
      {!deleting && shownError && alert(shownError)}
      {shown && (
        <SummaryTiles
          items={[
            { label: s.candidates, value: <span className="app-tabular">{shown.candidates}</span> },
            { label: s.size, value: <span className="app-tabular">{fileSize(shown.bytes)}</span> },
            { label: s.deleted, value: <span className="app-tabular">{shown.deleted}</span> },
            { label: s.kept, value: <span className="app-tabular">{shown.kept}</span> },
            { label: t.runColProposed, value: <span className="app-summary__lines">{t.runProposedBy(shown.createdBy, day(shown.createdAt))}</span> },
          ]}
        />
      )}
      {!review && !error && <p className="app-text-secondary">{t.loading}</p>}
      {proposed && run.candidates === 0 && <p>{t.runEmpty}</p>}
      {review && review.groups.length > 0 && <RunGroups review={review} title={s.groups} />}
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
      {/* It stays open when the run changed elsewhere, to say so. */}
      {deleting && run && (
        <DeleteRunModal
          run={run}
          confirmable={!!confirmable}
          busy={busy === "confirm"}
          unknown={unknown}
          error={shownError ? alert(shownError) : null}
          returnFocus={() => deleteButton.current ?? heading.current}
          confirm={(count) => void act("confirm", () => retentionApi.confirm(run.id, count))}
          close={() => setDeleting(false)}
        />
      )}
    </>
  );
}

/** The groups of a run: messages by owner / account / folder and the year their period ended. */
function RunGroups({ review, title }: { review: DeletionReview; title: string }) {
  const id = useId();
  return (
    <section className="page__section" aria-labelledby={`${id}-groups`}>
      <h2 id={`${id}-groups`} className="type-heading-03">
        {title}
      </h2>
      <TableContainer className="runs-table app-table">
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
                <TableCell className="runs-table__path">
                  <PathText text={`${g.owner || "-"} / ${g.account} / ${g.folder}`} />
                </TableCell>
                <TableCell className="app-tabular">{g.year}</TableCell>
                <TableCell className="app-tabular runs-table__number">{g.messages}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableContainer>
    </section>
  );
}

/**
 * Delete asks for the number of messages, typed in. Delete is enabled only
 * by the exact count, and nothing more is sent once the run's state is
 * unknown. The count field has the focus when the dialog opens.
 */
function DeleteRunModal({
  run,
  confirmable,
  busy,
  unknown,
  error,
  returnFocus,
  confirm,
  close,
}: {
  run: DeletionRun;
  /** The run is still proposed: once it is not, only the error shows. */
  confirmable: boolean;
  busy: boolean;
  unknown: boolean;
  error: ReactNode;
  /** Where the focus goes when the dialog closes, if the button that opened it is not focused. */
  returnFocus: () => HTMLElement | null;
  confirm: (count: number) => void;
  close: () => void;
}) {
  const s = settingsText();
  const [count, setCount] = useState("");
  const fieldId = useId();
  const matches = confirmable && !unknown && countMatches(count, run.candidates);
  const submit = () => {
    if (matches && !busy) confirm(Number(count.trim()));
  };
  return (
    <DangerModal
      size="sm"
      modalHeading={s.deleteCount(run.candidates)}
      primaryButtonText={t.runConfirm}
      primaryButtonDisabled={!matches}
      secondaryButtonText={t.cancel}
      preventCloseOnClickOutside
      returnFocus={returnFocus}
      // The action that runs is Delete (Cancel run is on the page).
      loadingStatus={busy ? "active" : "inactive"}
      loadingDescription={t.runConfirm}
      loadingIconDescription={t.loading}
      onRequestClose={() => {
        if (!busy) close();
      }}
      onRequestSubmit={submit}
    >
      <div className="modal-stack">
        {confirmable && (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              submit();
            }}
          >
            <TextInput
              id={`${fieldId}-count`}
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
        {error}
      </div>
    </DangerModal>
  );
}
