import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState, type FormEvent, type ReactNode, type RefObject } from "react";
import {
  Button,
  Checkbox,
  Form,
  InlineNotification,
  Layer,
  Link,
  MenuItem,
  NumberInput,
  Select,
  SelectItem,
  SkeletonText,
  Tab,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableHeader,
  TableRow,
  TableToolbar,
  TableToolbarContent,
  TabList,
  TabPanel,
  TabPanels,
  Tabs,
  Tag,
  TextArea,
  TextInput,
  Toggle,
} from "@carbon/react";
import { Add } from "@carbon/icons-react";
import {
  retentionApi,
  usersApi,
  type DeletionRun,
  type LegalHold,
  type ManagedUser,
  type RetentionAccount,
  type RetentionRule,
  type RetentionRules,
} from "./api";
import { DangerModal } from "./DangerModal";
import { day, fileSize } from "./format";
import { t, translateWithId } from "./i18n";
import { Loading, ViewFallback } from "./lazyView";
import { RunPage, RunStateTag } from "./RetentionRun";
import { parseQuery } from "./searchSyntax";
import { AppLink, DetailHeader, RowMenu, SummaryTiles, useMenuButtons, usePageFocus } from "./settingsParts";
import { settingsText } from "./settingsText";
import { TableSkeleton } from "./tableSkeleton";
import { formatState, parseState, useViewState, type ViewState } from "./urlState";

export { countMatches } from "./RetentionRun";

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

/** What a rule covers, as its row shows it: owner / account / folder. */
const ruleWhat = (r: RetentionRule) => `${accountLabel({ owner: r.owner, name: r.account, removed: r.accountRemoved })} / ${r.folder || t.ruleAllFolders}`;

const tabs = ["rules", "holds", "runs"] as const;
type TabName = (typeof tabs)[number];

/**
 * The pages under Retention, from the URL: a tab's list (&tab=holds; empty
 * is rules), an entry's page (&tab=holds&id=8) and the add pages (&add=rule,
 * &add=hold).
 */
type Page = { kind: "list"; tab: TabName } | { kind: "add"; tab: "rules" | "holds" } | { kind: "detail"; tab: TabName; id: number | null };

function pageOf(s: ViewState): Page {
  if (s.add === "rule") return { kind: "add", tab: "rules" };
  if (s.add === "hold") return { kind: "add", tab: "holds" };
  const tab = (s.tab || "rules") as TabName;
  if (s.id) return { kind: "detail", tab, id: /^\d+$/.test(s.id) ? Number(s.id) : null };
  return { kind: "list", tab };
}

/** The URL key of a tab: rules is the default, so it has none. */
const tabKey = (tab: TabName) => (tab === "rules" ? "" : tab);

/** The URL of a page under Retention. */
const hrefOf = (patch: Partial<ViewState> = {}) => formatState({ ...parseState(""), view: "retention", ...patch });

const guide = "https://pklnx.github.io/mail-archive/guide/retention";

/**
 * Retention rules, legal holds and deletion runs, for admins (#285 part 3):
 * a tab each, in the URL, with a table and a page per entry; Add rule and
 * Add hold are pages of their own. Only the open tab loads its data.
 */
export function RetentionPage() {
  const s = settingsText();
  const [state, update] = useViewState();
  const page = pageOf(state);
  const tab = page.tab;
  const [data, setData] = useState<RetentionRules | null>(null);
  const [error, setError] = useState("");
  // The error of the last action, for the page it was started on.
  const [failure, setFailure] = useState<{ page: string; text: string } | null>(null);
  // "Hold added", from the add page, on the Holds tab until the tab changes.
  const [notice, setNotice] = useState("");
  const [showReleased, setShowReleased] = useState(false);
  const [asking, setAsking] = useState<{ kind: "rule"; rule: RetentionRule } | { kind: "hold"; hold: LegalHold } | null>(null);
  const menus = useMenuButtons();
  const heading = useRef<HTMLHeadingElement>(null);
  const tabsBox = useRef<HTMLDivElement>(null);
  // A row that had the focus went away (its rule removed, its hold released
  // while released holds are hidden): the focus goes to the selected tab.
  const lostRow = useRef(false);

  const reload = useCallback(
    () =>
      retentionApi.rules().then(
        (d) => {
          setData(d);
          setError("");
        },
        (err: unknown) => setError(message(err)),
      ),
    [],
  );
  useEffect(() => {
    void reload();
  }, [reload]);

  // The holds of the Holds tab, and of a hold's page, which needs the
  // released ones too. Kept with the choice they were loaded for.
  const holdsShown = tab === "holds" && page.kind !== "add";
  const released = showReleased || page.kind === "detail";
  const [holds, setHolds] = useState<{ released: boolean; list: LegalHold[] } | null>(null);
  const [holdsError, setHoldsError] = useState("");
  const reloadHolds = useCallback(
    () =>
      retentionApi.holds(released).then(
        (list) => {
          setHolds({ released, list });
          setHoldsError("");
        },
        (err: unknown) => setHoldsError(message(err)),
      ),
    [released],
  );
  useEffect(() => {
    if (holdsShown) void reloadHolds();
  }, [holdsShown, reloadHolds]);
  const holdList = holds?.released === released ? holds.list : null;

  // The runs, for the Runs tab and a run's page; null when loading them failed.
  const runsShown = tab === "runs";
  const [runs, setRuns] = useState<DeletionRun[] | null>(null);
  const [runsError, setRunsError] = useState("");
  const reloadRuns = useCallback(
    () =>
      retentionApi.runs().then(
        (r) => {
          setRuns(r);
          setRunsError("");
          return r;
        },
        (err: unknown) => {
          setRunsError(message(err));
          return null;
        },
      ),
    [],
  );
  useEffect(() => {
    if (runsShown) void reloadRuns();
  }, [runsShown, reloadRuns]);
  // A confirmed run deletes in the background: poll until it is done.
  const polling = runsShown && (runs?.some((r) => r.state === "confirmed") ?? false);
  useEffect(() => {
    if (!polling) return;
    const id = setInterval(() => void reloadRuns(), 3000);
    return () => clearInterval(id);
  }, [polling, reloadRuns]);

  // The entry of a detail page, once its list is loaded.
  let rule: RetentionRule | undefined;
  let hold: LegalHold | undefined;
  let run: DeletionRun | undefined;
  let loaded = true;
  if (page.kind === "detail") {
    if (tab === "rules") {
      loaded = data !== null;
      rule = data?.rules.find((r) => r.id === page.id);
    } else if (tab === "holds") {
      loaded = holdList !== null;
      hold = holdList?.find((h) => h.id === page.id);
    } else {
      loaded = runs !== null;
      run = runs?.find((r) => r.id === page.id);
    }
  }
  // An entry that does not exist (any more) returns to its tab's list.
  const unknown = page.kind === "detail" && loaded && !rule && !hold && !run;
  useEffect(() => {
    if (unknown) update({ id: "" }, { replace: true });
  }, [unknown, update]);
  const shown = page.kind === "detail" && !unknown ? `${tab}:${page.id}` : page.kind === "add" ? `add:${tab}` : "list";
  usePageFocus(page.kind === "detail" && !loaded ? null : shown, heading);

  useLayoutEffect(() => {
    if (!lostRow.current) return;
    lostRow.current = false;
    // Only the list has the tabs.
    tabsBox.current?.querySelector<HTMLElement>('[role="tab"][aria-selected="true"]')?.focus();
  });

  // Opening a page is a history entry; finishing one replaces it, so Back
  // does not lead into a form that is done.
  const go = (p: Partial<ViewState>, replace = false) => update({ view: "retention", tab: tabKey(tab), id: "", add: "", ...p }, { replace });
  const toTab = (replace = false) => go({}, replace);
  const lost = () => {
    lostRow.current = true;
  };

  // Runs an action and loads the list again; resolves to whether it worked.
  const act = async (action: () => Promise<unknown>, again: () => Promise<unknown>) => {
    setFailure(null);
    let text = "";
    try {
      await action();
    } catch (err) {
      text = message(err);
    }
    await again();
    if (text) setFailure({ page: shown, text });
    return !text;
  };
  const notices = failure?.page === shown && (
    <p role="alert" className="app-text-error">
      {failure.text}
    </p>
  );
  // The error of a load, or while it loads the skeleton in the shape of what comes.
  const status = (err: string, skeleton: ReactNode) => (
    <>
      {err && (
        <p role="alert" className="app-text-error">
          {err}
        </p>
      )}
      {!err && skeleton}
    </>
  );
  const formSkeleton = (
    <Loading>
      <SkeletonText paragraph lineCount={4} />
    </Loading>
  );
  const parent = { label: t.retentionTitle, href: hrefOf({ tab: tabKey(tab) }), go: () => toTab() };

  let content: ReactNode;
  if (page.kind === "add" && tab === "rules") {
    content = (
      <>
        <DetailHeader parent={parent} title={t.ruleAdd} heading={heading} />
        {status(error, !data && formSkeleton)}
        {data && (
          <RuleForm
            data={data}
            cancel={() => toTab(true)}
            added={() => {
              void reload();
              toTab(true);
            }}
          />
        )}
      </>
    );
  } else if (page.kind === "add") {
    content = (
      <>
        <DetailHeader parent={parent} title={t.holdAdd} heading={heading} />
        {status(error, !data && formSkeleton)}
        {data && (
          <HoldForm
            accounts={data.accounts}
            cancel={() => toTab(true)}
            added={(matched) => {
              setNotice(t.holdAdded(matched));
              toTab(true);
            }}
          />
        )}
      </>
    );
  } else if (page.kind === "detail" && !loaded) {
    content = status(tab === "rules" ? error : tab === "holds" ? holdsError : runsError, <ViewFallback inline />);
  } else if (rule) {
    const r = rule;
    content = (
      <RuleDetail
        rule={r}
        heading={heading}
        parent={parent}
        notices={notices}
        menu={
          <MenuSlot lost={() => heading.current?.focus()}>
            <RowMenu label={t.userActions(ruleWhat(r))} register={menus.register(`rule:${r.id}`)}>
              <MenuItem label={t.ruleRemove} kind="danger" onClick={() => setAsking({ kind: "rule", rule: r })} />
            </RowMenu>
          </MenuSlot>
        }
      />
    );
  } else if (hold) {
    const h = hold;
    content = (
      <HoldDetail
        hold={h}
        heading={heading}
        parent={parent}
        notices={notices}
        // Released, the hold has nothing left in its menu: the focus goes to the h1.
        menu={
          !h.releasedAt && (
            <MenuSlot lost={() => heading.current?.focus()}>
              <RowMenu label={t.userActions(h.name)} register={menus.register(`hold:${h.id}`)}>
                <MenuItem label={t.holdRelease} kind="danger" onClick={() => setAsking({ kind: "hold", hold: h })} />
              </RowMenu>
            </MenuSlot>
          )
        }
      />
    );
  } else if (run) {
    content = <RunPage key={run.id} id={run.id} state={run.state} listed={run} heading={heading} parent={parent} changed={reloadRuns} />;
  } else {
    content = (
      <>
        <header className="page-header">
          <h1 ref={heading} tabIndex={-1} className="page-header__title type-heading-04 app-page-title">
            {t.retentionTitle}
          </h1>
        </header>
        <div ref={tabsBox}>
          {/* Only the open tab is mounted: each one loads its data when it is shown. */}
          <Tabs
            selectedIndex={tabs.indexOf(tab)}
            onChange={({ selectedIndex }) => {
              // The notice and an action's error belong to the tab they were shown on.
              setNotice("");
              setFailure(null);
              go({ tab: tabKey(tabs[selectedIndex] ?? "rules") }, true);
            }}
          >
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
            {/* A contained tab's panel is a layer of its own: the table and
                fields in it sit on the next one. */}
            <TabPanels>
              <TabPanel className="retention-panel">
                {tab === "rules" && (
                  <Layer>
                    <Help text={s.rulesHelp} anchor="rules" />
                    {notices}
                    {status(
                      error,
                      !data && (
                        <TableSkeleton
                          columns={[
                            { header: s.colRule },
                            { header: s.colPeriod },
                            { header: t.ruleNote, wide: true },
                            { header: s.created, wide: true },
                            { header: "" },
                          ]}
                        />
                      ),
                    )}
                    {data && (
                      <RulesTable
                        rules={data.rules}
                        menus={menus}
                        open={(id) => go({ id: String(id) })}
                        add={() => go({ add: "rule" })}
                        remove={(r) => setAsking({ kind: "rule", rule: r })}
                        lost={lost}
                      />
                    )}
                  </Layer>
                )}
              </TabPanel>
              <TabPanel className="retention-panel">
                {tab === "holds" && (
                  <Layer>
                    <Help text={s.holdsHelp} anchor="legal-holds" />
                    {notices}
                    {status(error || holdsError, null)}
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
                    <HoldsTable
                      holds={holdList}
                      showReleased={showReleased}
                      setShowReleased={setShowReleased}
                      menus={menus}
                      open={(id) => go({ id: String(id) })}
                      add={() => {
                        setNotice("");
                        go({ add: "hold" });
                      }}
                      release={(h) => setAsking({ kind: "hold", hold: h })}
                      failed={holdsError !== ""}
                      lost={lost}
                    />
                  </Layer>
                )}
              </TabPanel>
              <TabPanel className="retention-panel">
                {tab === "runs" && (
                  <Layer>
                    <Help text={s.runsHelp} anchor="deletion-runs" />
                    <RunsTable
                      runs={runs}
                      error={runsError}
                      open={(id) => go({ id: String(id) })}
                      reload={reloadRuns}
                    />
                  </Layer>
                )}
              </TabPanel>
            </TabPanels>
          </Tabs>
        </div>
      </>
    );
  }

  return (
    <div className="accounts-page">
      <div className={page.kind === "add" ? "page" : "page page--wide"}>{content}</div>
      {asking?.kind === "rule" && (
        <DangerModal
          size="sm"
          modalHeading={t.ruleRemoveTitle(ruleWhat(asking.rule))}
          primaryButtonText={t.ruleRemove}
          secondaryButtonText={t.cancel}
          returnFocus={() => menus.button(`rule:${asking.rule.id}`) ?? heading.current}
          onRequestClose={() => setAsking(null)}
          onRequestSubmit={() => {
            const { id } = asking.rule;
            setAsking(null);
            // Removed from its page: the list replaces the page. From its
            // row: the row goes, and the focus with it to the Rules tab.
            const fromDetail = page.kind === "detail";
            void act(() => retentionApi.removeRule(id), reload).then((ok) => {
              if (ok && fromDetail) toTab(true);
            });
          }}
        >
          <p>{t.ruleRemoveText}</p>
        </DangerModal>
      )}
      {asking?.kind === "hold" && (
        <ReleaseHoldModal
          hold={asking.hold}
          returnFocus={() => menus.button(`hold:${asking.hold.id}`) ?? heading.current}
          cancel={() => setAsking(null)}
          confirm={(note) => {
            const { id } = asking.hold;
            setAsking(null);
            void act(() => retentionApi.releaseHold(id, note), reloadHolds);
          }}
        />
      )}
    </div>
  );
}

/** The helper line of a tab, with a link to the guide. */
function Help({ text, anchor }: { text: string; anchor: string }) {
  const s = settingsText();
  return (
    <p className="retention-help app-text-secondary app-text-small">
      {text}{" "}
      <Link href={`${guide}#${anchor}`} target="_blank" rel="noreferrer" inline size="sm">
        {s.guideLink}
      </Link>
    </p>
  );
}

/**
 * Calls lost when the element, about to go, has the focus. Checked in a
 * layout effect's cleanup, which runs while the element is still in the
 * document.
 */
function useFocusLoss(element: () => Element | null | undefined, lost: () => void) {
  const onLost = useRef(lost);
  const get = useRef(element);
  useLayoutEffect(() => {
    onLost.current = lost;
  });
  useLayoutEffect(() => {
    const el = get.current();
    return () => {
      if (el?.contains(document.activeElement)) onLost.current();
    };
  }, []);
}

/** A menu that hands the focus on (lost) when it goes while it has the focus: its last item is done. */
function MenuSlot({ lost, children }: { lost: () => void; children: ReactNode }) {
  const box = useRef<HTMLSpanElement>(null);
  useFocusLoss(() => box.current, lost);
  return <span ref={box}>{children}</span>;
}

/**
 * A row of the rules or holds: the name is the row header and links to the
 * entry's page. When its menu goes while it has the focus (a released hold
 * that stays listed), the name link takes the focus; when the whole row
 * goes, lost hands it on.
 */
function EntryRow({
  name,
  href,
  open,
  more,
  menu,
  lost,
  children,
}: {
  name: string;
  href: string;
  open: () => void;
  more: ReactNode;
  menu: ReactNode;
  lost: () => void;
  children: ReactNode;
}) {
  const nameBox = useRef<HTMLSpanElement>(null);
  const menuLost = useRef(false);
  useFocusLoss(() => nameBox.current?.closest("tr"), lost);
  useLayoutEffect(() => {
    if (!menuLost.current) return;
    menuLost.current = false;
    nameBox.current?.querySelector("a")?.focus();
  });
  return (
    <TableRow>
      <TableHeader scope="row">
        <span ref={nameBox}>
          <AppLink href={href} go={open} className="retention-table__name">
            {name}
          </AppLink>
        </span>
        <span className="retention-table__more app-narrow-only app-text-secondary app-text-small">{more}</span>
      </TableHeader>
      {children}
      <TableCell className="cds--table-column-menu">
        {menu && (
          <MenuSlot
            lost={() => {
              menuLost.current = true;
            }}
          >
            {menu}
          </MenuSlot>
        )}
      </TableCell>
    </TableRow>
  );
}

/** A tab's toolbar: its one primary action, and more controls before it. */
function Toolbar({ children }: { children: ReactNode }) {
  return (
    <TableToolbar aria-label={t.colActions}>
      <TableToolbarContent className="retention-toolbar">{children}</TableToolbarContent>
    </TableToolbar>
  );
}

function RulesTable({
  rules,
  menus,
  open,
  add,
  remove,
  lost,
}: {
  rules: RetentionRule[];
  menus: ReturnType<typeof useMenuButtons>;
  open: (id: number) => void;
  add: () => void;
  remove: (r: RetentionRule) => void;
  lost: () => void;
}) {
  const s = settingsText();
  return (
    <TableContainer className="retention-table app-table">
      <Toolbar>
        <Button kind="primary" renderIcon={Add} onClick={add}>
          {t.ruleAdd}
        </Button>
      </Toolbar>
      {rules.length === 0 ? (
        <p className="retention-table__empty">{t.ruleNone}</p>
      ) : (
        <Table aria-label={t.retentionRulesTab} size="lg" overflowMenuOnHover={false}>
          <TableHead>
            <TableRow>
              <TableHeader>{s.colRule}</TableHeader>
              <TableHeader>{s.colPeriod}</TableHeader>
              <TableHeader className="app-wide-only">{t.ruleNote}</TableHeader>
              <TableHeader className="app-wide-only">{s.created}</TableHeader>
              <TableHeader>
                <span className="cds--visually-hidden">{t.colActions}</span>
              </TableHeader>
            </TableRow>
          </TableHead>
          <TableBody>
            {rules.map((r) => {
              const what = ruleWhat(r);
              const created = t.ruleCreated(r.createdBy, day(r.createdAt));
              return (
                <EntryRow
                  key={r.id}
                  name={what}
                  href={hrefOf({ id: String(r.id) })}
                  open={() => open(r.id)}
                  more={
                    <>
                      {r.note && <span className="app-break-words">{r.note}</span>}
                      <span>{created}</span>
                    </>
                  }
                  menu={
                    <RowMenu label={t.userActions(what)} register={menus.register(`rule:${r.id}`)}>
                      <MenuItem label={t.ruleRemove} kind="danger" onClick={() => remove(r)} />
                    </RowMenu>
                  }
                  lost={lost}
                >
                  <TableCell>
                    {t.ruleYearsCount(r.years)} · {r.timeZone}
                  </TableCell>
                  <TableCell className="app-wide-only app-break-words">{r.note}</TableCell>
                  <TableCell className="app-wide-only app-text-secondary">{created}</TableCell>
                </EntryRow>
              );
            })}
          </TableBody>
        </Table>
      )}
    </TableContainer>
  );
}

const HoldTag = ({ hold }: { hold: LegalHold }) =>
  hold.releasedAt ? (
    <Tag size="sm" type="gray">
      {t.holdReleasedTag}
    </Tag>
  ) : (
    <Tag size="sm" type="green">
      {settingsText().holdActive}
    </Tag>
  );

function HoldsTable({
  holds,
  showReleased,
  setShowReleased,
  menus,
  open,
  add,
  release,
  failed,
  lost,
}: {
  holds: LegalHold[] | null;
  showReleased: boolean;
  setShowReleased: (on: boolean) => void;
  menus: ReturnType<typeof useMenuButtons>;
  open: (id: number) => void;
  add: () => void;
  release: (h: LegalHold) => void;
  /** The list did not load: the tab's alert says why, and no skeleton shows. */
  failed: boolean;
  lost: () => void;
}) {
  const s = settingsText();
  const id = useId();
  return (
    <TableContainer className="retention-table app-table">
      <Toolbar>
        <Toggle
          id={`${id}-released`}
          size="sm"
          className="retention-toolbar__toggle"
          labelText={t.holdShowReleased}
          labelA={t.carbon.toggleOff}
          labelB={t.carbon.toggleOn}
          toggled={showReleased}
          onToggle={setShowReleased}
        />
        <Button kind="primary" renderIcon={Add} onClick={add}>
          {t.holdAdd}
        </Button>
      </Toolbar>
      {!holds && !failed && (
        <TableSkeleton
          columns={[
            { header: s.colHold },
            { header: s.colCovers, wide: true },
            { header: s.colStatus },
            { header: s.created, wide: true },
            { header: "" },
          ]}
        />
      )}
      {holds && holds.length === 0 && <p className="retention-table__empty">{t.holdNone}</p>}
      {holds && holds.length > 0 && (
        <Table aria-label={t.retentionHoldsTab} size="lg" overflowMenuOnHover={false}>
          <TableHead>
            <TableRow>
              <TableHeader>{s.colHold}</TableHeader>
              <TableHeader className="app-wide-only">{s.colCovers}</TableHeader>
              <TableHeader>{s.colStatus}</TableHeader>
              <TableHeader className="app-wide-only">{s.created}</TableHeader>
              <TableHeader>
                <span className="cds--visually-hidden">{t.colActions}</span>
              </TableHeader>
            </TableRow>
          </TableHead>
          <TableBody>
            {holds.map((h) => (
              <EntryRow
                key={h.id}
                name={h.name}
                href={hrefOf({ tab: "holds", id: String(h.id) })}
                open={() => open(h.id)}
                more={<span className="app-break-words">{holdScope(h)}</span>}
                menu={
                  !h.releasedAt && (
                    <RowMenu label={t.userActions(h.name)} register={menus.register(`hold:${h.id}`)}>
                      <MenuItem label={t.holdRelease} kind="danger" onClick={() => release(h)} />
                    </RowMenu>
                  )
                }
                lost={lost}
              >
                <TableCell className="app-wide-only app-break-words">{holdScope(h)}</TableCell>
                <TableCell>
                  <HoldTag hold={h} />
                </TableCell>
                <TableCell className="app-wide-only app-text-secondary">{t.ruleCreated(h.createdBy, day(h.createdAt))}</TableCell>
              </EntryRow>
            ))}
          </TableBody>
        </Table>
      )}
    </TableContainer>
  );
}

/** The runs: the run is the row header and links to its page, which has its actions. */
function RunsTable({
  runs,
  error,
  open,
  reload,
}: {
  runs: DeletionRun[] | null;
  error: string;
  open: (id: number) => void;
  reload: () => Promise<DeletionRun[] | null>;
}) {
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState("");
  const propose = async () => {
    setBusy(true);
    setFailed("");
    try {
      const run = await retentionApi.propose();
      // The new run's page, once the list has it.
      await reload();
      open(run.id);
    } catch (err) {
      setFailed(message(err));
      void reload();
    } finally {
      setBusy(false);
    }
  };
  const anyOpen = runs?.some(runOpen);
  const shownError = failed || error;
  return (
    <TableContainer className="retention-table runs-table app-table">
      {/* A new run only while none is open. */}
      {!anyOpen && (
        <Toolbar>
          <Button kind="primary" renderIcon={Add} disabled={busy || !runs} onClick={() => void propose()}>
            {t.runPropose}
          </Button>
        </Toolbar>
      )}
      {shownError && (
        <p role="alert" className="retention-table__empty app-text-error">
          {shownError}
        </p>
      )}
      {!runs && !error && (
        <TableSkeleton
          columns={[
            { header: t.runColRun },
            { header: t.runColState },
            { header: t.runColResult, wide: true },
            { header: t.runColProposed, wide: true },
          ]}
        />
      )}
      {runs && runs.length === 0 && <p className="retention-table__empty">{t.runNone}</p>}
      {runs && runs.length > 0 && (
        <Table aria-label={t.runsTab} size="lg">
          <TableHead>
            <TableRow>
              <TableHeader>{t.runColRun}</TableHeader>
              <TableHeader>{t.runColState}</TableHeader>
              <TableHeader className="app-wide-only">{t.runColResult}</TableHeader>
              <TableHeader className="app-wide-only">{t.runColProposed}</TableHeader>
            </TableRow>
          </TableHead>
          <TableBody>
            {runs.map((r) => {
              const result = r.state === "done" || r.deleted + r.kept > 0 ? t.runResult(r.deleted, r.kept) : "";
              const proposedBy = t.runProposedBy(r.createdBy, day(r.createdAt));
              return (
                <TableRow key={r.id}>
                  <TableHeader scope="row">
                    <AppLink href={hrefOf({ tab: "runs", id: String(r.id) })} go={() => open(r.id)} className="retention-table__name">
                      {t.runLine(r.id, r.candidates, fileSize(r.bytes))}
                    </AppLink>
                    <span className="retention-table__more app-narrow-only app-text-secondary app-text-small">
                      {result && <span>{result}</span>}
                      <span>{proposedBy}</span>
                    </span>
                  </TableHeader>
                  <TableCell>
                    <RunStateTag state={r.state} />
                  </TableCell>
                  <TableCell className="app-wide-only">{result}</TableCell>
                  <TableCell className="app-wide-only app-text-secondary">{proposedBy}</TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      )}
    </TableContainer>
  );
}

/** A rule's page: what it covers, its period and note; Remove rule in the menu. Rules are not edited. */
function RuleDetail({
  rule: r,
  heading,
  parent,
  notices,
  menu,
}: {
  rule: RetentionRule;
  heading: RefObject<HTMLHeadingElement | null>;
  parent: { label: string; href: string; go: () => void };
  notices: ReactNode;
  menu: ReactNode;
}) {
  const s = settingsText();
  const id = useId();
  return (
    <>
      <DetailHeader
        parent={parent}
        title={`${r.owner || "-"} / ${r.account} / ${r.folder || t.ruleAllFolders}`}
        heading={heading}
        tags={
          r.accountRemoved && (
            <Tag size="sm" type="gray">
              {s.accountRemoved}
            </Tag>
          )
        }
        menu={menu}
      />
      {notices}
      <SummaryTiles
        items={[
          { label: s.colPeriod, value: t.ruleYearsCount(r.years) },
          { label: t.ruleTimeZone, value: r.timeZone },
          { label: s.created, value: <span className="app-summary__lines">{t.ruleCreated(r.createdBy, day(r.createdAt))}</span> },
        ]}
      />
      {r.note && (
        <section className="page__section" aria-labelledby={`${id}-note`}>
          <h2 id={`${id}-note`} className="type-heading-03">
            {t.ruleNote}
          </h2>
          <p className="app-break-words">{r.note}</p>
        </section>
      )}
    </>
  );
}

/** A hold's page: its scope, the reason and, once released, the note; Release in the menu while it is active. */
function HoldDetail({
  hold: h,
  heading,
  parent,
  notices,
  menu,
}: {
  hold: LegalHold;
  heading: RefObject<HTMLHeadingElement | null>;
  parent: { label: string; href: string; go: () => void };
  notices: ReactNode;
  menu: ReactNode;
}) {
  const s = settingsText();
  const id = useId();
  return (
    <>
      <DetailHeader parent={parent} title={h.name} heading={heading} tags={<HoldTag hold={h} />} menu={menu} />
      {notices}
      <SummaryTiles
        items={[
          { label: s.colCovers, value: <span className="app-summary__lines">{holdScope(h)}</span> },
          { label: s.created, value: <span className="app-summary__lines">{t.ruleCreated(h.createdBy, day(h.createdAt))}</span> },
          ...(h.releasedAt
            ? [{ label: s.released, value: <span className="app-summary__lines">{t.holdReleased(h.releasedBy, day(h.releasedAt), h.releaseNote)}</span> }]
            : []),
        ]}
      />
      {h.reason && (
        <section className="page__section" aria-labelledby={`${id}-reason`}>
          <h2 id={`${id}-reason`} className="type-heading-03">
            {t.holdReason}
          </h2>
          <p className="app-break-words">{h.reason}</p>
        </section>
      )}
    </>
  );
}

/** Releasing a hold asks for the note that is kept with it; an empty note sends nothing. */
export function ReleaseHoldModal({
  hold,
  returnFocus,
  cancel,
  confirm,
}: {
  hold: LegalHold;
  returnFocus?: () => HTMLElement | null | undefined;
  cancel: () => void;
  confirm: (note: string) => void;
}) {
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
      returnFocus={returnFocus}
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

/** The Add rule page's form, with the confirmation of a period shorter than the legal one. */
function RuleForm({ data, cancel, added }: { data: RetentionRules; cancel: () => void; added: () => void }) {
  const [error, setError] = useState("");
  const [accountId, setAccountId] = useState("");
  const [folder, setFolder] = useState("");
  const [years, setYears] = useState("10");
  const [timeZone, setTimeZone] = useState(data.defaultTimeZone);
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  // A period shorter than the legal minimum waits for its confirmation.
  const [short, setShort] = useState<number | null>(null);
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
      added();
    } catch (err) {
      setError(message(err));
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

  return (
    <>
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
        {error && (
          <p role="alert" className="app-text-error">
            {error}
          </p>
        )}
        <div className="form-actions">
          <Button kind="secondary" onClick={cancel} disabled={busy}>
            {t.cancel}
          </Button>
          <Button type="submit" kind="primary" renderIcon={Add} disabled={busy || !account}>
            {t.ruleAdd}
          </Button>
        </div>
      </Form>

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
    </>
  );
}

/** The Add hold page's form: a hold without user, account or filter needs the box for the whole archive. */
function HoldForm({ accounts, cancel, added }: { accounts: RetentionAccount[]; cancel: () => void; added: (matched?: number) => void }) {
  const [name, setName] = useState("");
  const [reason, setReason] = useState("");
  const [user, setUser] = useState("");
  const [accountId, setAccountId] = useState("");
  const [query, setQuery] = useState("");
  const [all, setAll] = useState(false);
  const [users, setUsers] = useState<ManagedUser[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const id = useId();
  useEffect(() => {
    usersApi.list().then(setUsers, () => setUsers([]));
  }, []);
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
      added(res.matched);
    } catch (err) {
      setError(message(err));
      setBusy(false);
    }
  };

  return (
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
        <Button kind="secondary" onClick={cancel} disabled={busy}>
          {t.cancel}
        </Button>
        <Button type="submit" kind="primary" renderIcon={Add} disabled={busy || (unscoped && !all)}>
          {t.holdAdd}
        </Button>
      </div>
    </Form>
  );
}
