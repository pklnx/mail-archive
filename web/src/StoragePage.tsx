import { useEffect, useId, useRef, useState, type FormEvent, type ReactNode } from "react";
import {
  Button,
  Checkbox,
  MenuItem,
  ProgressBar,
  Select,
  SelectItem,
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
import { Renew } from "@carbon/icons-react";
import { ApiError, isAdminUsage, usageApi, type StorageState, type UsageAdmin } from "./api";
import { AppModal } from "./DangerModal";
import { formatCount, relativeTime, storageSize, timestamp } from "./format";
import { t } from "./i18n";
import { RowMenu, SummaryTiles, useMenuButtons } from "./settingsParts";
import { storageText } from "./storageText";
import { TableSkeleton } from "./tableSkeleton";
import type { UsageState } from "./useUsage";
import { groupsApi } from "./userAdminApi";

type Group = UsageAdmin["groups"][number];

const percentOf = (bytes: number, quota: number) => Math.floor((bytes / quota) * 100);
const stops = (s: StorageState) => s === "full" || s === "overflow" || s === "disk_low";
const rank: Record<StorageState, number> = { ok: 0, warn: 1, full: 2, overflow: 3, disk_low: 4 };
const worst = (a: StorageState, b: StorageState) => (rank[b] > rank[a] ? b : a);

/** The state as a tag: green when ok, red when new mail stops. */
function StateTag({ state }: { state: StorageState }) {
  const st = storageText();
  return (
    <Tag size="sm" type={state === "ok" ? "green" : stops(state) ? "red" : "warm-gray"}>
      {st.states[state]}
    </Tag>
  );
}

/** A usage bar: no helperText, which would add an untranslatable "Loading". */
function UsageBar({ label, bytes, quota, state, hideLabel = false }: { label: string; bytes: number; quota: number; state: StorageState; hideLabel?: boolean }) {
  return (
    <ProgressBar
      label={label}
      hideLabel={hideLabel}
      size={hideLabel ? "small" : "big"}
      value={Math.min(bytes, quota)}
      max={quota}
      status={stops(state) ? "error" : undefined}
    />
  );
}

/** The quota a group's quota still has to fit under: the instance quota, null without one. */
const instanceLimit = (u: UsageAdmin) => u.instance.quotaBytes;

/**
 * The admin's Storage page (#397, part 4): the instance with its quota,
 * disk and database; the customer groups with their quotas, each with Edit
 * quota, while more than one group exists (#470 decision 10); and every
 * account's size. Refresh counts the accounts and groups again.
 */
export function StoragePage({ usage }: { usage: UsageState }) {
  const st = storageText();
  const { data, error, reload } = usage;
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState("");
  const [failure, setFailure] = useState("");
  const [editing, setEditing] = useState<Group | null>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const menus = useMenuButtons();
  // After a refresh, the figures are fetched every few seconds until the
  // count's time changes, for at most two minutes.
  const [waiting, setWaiting] = useState<string | null | undefined>(undefined);
  const polls = useRef(0);
  const u = data && isAdminUsage(data) ? data : null;
  const computedAt = u?.computedAt ?? null;
  useEffect(() => {
    if (waiting === undefined) return;
    if (computedAt !== waiting || polls.current >= 40) {
      setWaiting(undefined);
      setBusy(false);
      return;
    }
    const timer = window.setTimeout(() => {
      polls.current++;
      reload();
    }, 3000);
    return () => window.clearTimeout(timer);
  }, [waiting, computedAt, reload, data]);

  const refresh = async () => {
    setBusy(true);
    setDone("");
    setFailure("");
    try {
      await usageApi.refresh();
      setDone(st.refreshed);
      polls.current = 0;
      setWaiting(computedAt);
      reload();
    } catch (err) {
      setFailure(t.failed(err instanceof Error ? err.message : String(err)));
      setBusy(false);
    }
  };

  const groups = u?.groups ?? [];
  const several = groups.length > 1;
  const groupNames = new Map(groups.map((g) => [g.id, g.name]));

  return (
    <div className="accounts-page">
      <div className="page page--wide">
        <header className="page-header">
          <div className="page-header__row">
            <h1 ref={heading} tabIndex={-1} className="page-header__title type-heading-04 app-page-title">
              {t.storage}
            </h1>
            <div className="page-header__actions">
              <Button kind="tertiary" renderIcon={Renew} disabled={busy || !u} onClick={() => void refresh()}>
                {busy ? st.refreshing : st.refresh}
              </Button>
            </div>
          </div>
          <p className="page-header__intro app-text-secondary">{st.intro}</p>
          {u && (
            <p className="page-header__intro app-text-secondary">
              {computedAt ? st.computedAt(timestamp(computedAt), relativeTime(computedAt)) : st.neverComputed} {several && st.upperBound}
            </p>
          )}
        </header>
        {done && <p role="status">{done}</p>}
        {(failure || (error && !u)) && (
          <p role="alert" className="app-text-error">
            {failure || t.failed(error)}
          </p>
        )}
        {!u && !error && <TableSkeleton columns={[{ header: st.colAccount }, { header: st.colSize }]} />}
        {u && <InstanceSection usage={u} />}
        {u && several && (
          <GroupsSection
            usage={u}
            edit={(g) => setEditing(g)}
            register={menus.register}
          />
        )}
        {u && <AccountsSection usage={u} groupNames={several ? groupNames : null} />}
      </div>
      {editing && u && (
        <QuotaModal
          group={editing}
          usage={u}
          returnFocus={() => menus.button(`group:${editing.id}`) ?? heading.current}
          cancel={() => setEditing(null)}
          saved={(text) => {
            setEditing(null);
            setFailure("");
            setDone(text);
            reload();
          }}
        />
      )}
    </div>
  );
}

function Section({ title, help, children }: { title: string; help?: ReactNode; children: ReactNode }) {
  const id = useId();
  return (
    <section className="page__section" aria-labelledby={id}>
      <h2 id={id} className="type-heading-03 app-page-title">
        {title}
      </h2>
      {help && <p className="app-text-secondary">{help}</p>}
      {children}
    </section>
  );
}

/**
 * The instance: its bar against the quota, or, while one group exists whose
 * own quota is lower (set at the command line), against that quota, so a
 * limit the page does not offer to edit never pauses mail unseen (#397
 * revision 4, point 11).
 */
function InstanceSection({ usage: u }: { usage: UsageAdmin }) {
  const st = storageText();
  const inst = u.instance;
  const only = u.groups.length === 1 ? u.groups[0] : null;
  const groupFirst = only?.quotaBytes != null && (inst.quotaBytes === null || only.quotaBytes < inst.quotaBytes) ? only : null;
  const limit = groupFirst?.quotaBytes ?? inst.quotaBytes;
  const used = groupFirst ? groupFirst.bytes : inst.bytes;
  const state = only ? worst(inst.state, only.state) : inst.state;
  const usedText = limit ? st.used(storageSize(used), storageSize(limit), percentOf(used, limit)) : st.usedNoQuota(storageSize(used));
  const tiles = [
    { label: st.messages, value: <span className="app-tabular">{formatCount(inst.messages)}</span> },
    { label: st.quota, value: <span className="app-tabular">{inst.quotaBytes === null ? st.noQuota : storageSize(inst.quotaBytes)}</span> },
    {
      label: st.state,
      value: (
        <span className="app-summary__lines">
          <StateTag state={state} />
          {state === inst.state && inst.state !== "ok" && <span className="app-text-secondary app-text-small">{st.since(timestamp(inst.stateSince))}</span>}
        </span>
      ),
    },
    ...(inst.disk
      ? [
          {
            label: st.disk,
            value: (
              <span className="app-summary__lines">
                <span className="app-tabular">{st.diskFree(storageSize(inst.disk.freeBytes), storageSize(inst.disk.totalBytes))}</span>
                {inst.minFreeDiskBytes > 0 && <span className="app-text-secondary app-text-small">{st.diskReserve(storageSize(inst.minFreeDiskBytes))}</span>}
              </span>
            ),
          },
        ]
      : []),
    { label: st.database, value: <span className="app-tabular">{inst.databaseBytes > 0 ? storageSize(inst.databaseBytes) : st.notCounted}</span> },
    { label: st.lastBackup, value: inst.lastBackupAt ? timestamp(inst.lastBackupAt) : st.noBackup },
    { label: st.encryption, value: inst.encryption === "off" ? st.encryptionOff : inst.encryption === "on" ? st.encryptionOn : inst.encryption },
  ];
  return (
    <Section title={st.instanceTitle} help={st.instanceQuotaHelp}>
      {limit ? <UsageBar label={`${st.usage}: ${usedText}`} bytes={used} quota={limit} state={state} /> : <p className="app-tabular">{usedText}</p>}
      {groupFirst?.quotaBytes != null && <p className="app-text-secondary">{st.groupQuotaFirst(storageSize(groupFirst.quotaBytes))}</p>}
      <SummaryTiles items={tiles} />
    </Section>
  );
}

/** The groups with their usage bars and Edit quota (more than one group only). */
function GroupsSection({ usage: u, edit, register }: { usage: UsageAdmin; edit: (g: Group) => void; register: (key: string) => (el: HTMLElement | null) => void }) {
  const st = storageText();
  const sum = u.groups.reduce((n, g) => n + (g.quotaBytes ?? 0), 0);
  const limit = instanceLimit(u);
  return (
    <Section
      title={st.groupsTitle}
      help={
        <>
          {st.groupsHelp(u.instance.warnPercent, u.instance.overflowPercent)}
          {limit !== null && sum > limit && <> {st.quotaSum(storageSize(sum), storageSize(limit))}</>}
        </>
      }
    >
      <TableContainer className="users-table app-table">
        <Table aria-label={st.groupsTitle} size="lg">
          <TableHead>
            <TableRow>
              <TableHeader>{st.colGroup}</TableHeader>
              <TableHeader>{st.colUsed}</TableHeader>
              <TableHeader>{st.colQuota}</TableHeader>
              <TableHeader className="app-wide-only">{st.colState}</TableHeader>
              <TableHeader>
                <span className="cds--visually-hidden">{st.colActions}</span>
              </TableHeader>
            </TableRow>
          </TableHead>
          <TableBody>
            {u.groups.map((g) => (
              <TableRow key={g.id}>
                <TableHeader scope="row">
                  <span className="users-table__name">{g.name}</span>
                </TableHeader>
                <TableCell>
                  <span className="app-tabular">
                    {g.quotaBytes ? st.used(storageSize(g.bytes), storageSize(g.quotaBytes), percentOf(g.bytes, g.quotaBytes)) : storageSize(g.bytes)}
                  </span>
                  {g.quotaBytes !== null && <UsageBar hideLabel label={`${g.name}: ${st.colUsed}`} bytes={g.bytes} quota={g.quotaBytes} state={g.state} />}
                </TableCell>
                <TableCell className="app-tabular">{g.quotaBytes === null ? st.noQuota : storageSize(g.quotaBytes)}</TableCell>
                <TableCell className="app-wide-only">
                  <StateTag state={g.state} />
                </TableCell>
                <TableCell className="cds--table-column-menu">
                  <RowMenu label={st.groupActions(g.name)} register={register(`group:${g.id}`)}>
                    <MenuItem label={st.editQuota} onClick={() => edit(g)} />
                  </RowMenu>
                </TableCell>
              </TableRow>
            ))}
            {u.ungrouped.messages > 0 && (
              <TableRow>
                <TableHeader scope="row">{st.withoutGroup}</TableHeader>
                <TableCell className="app-tabular">{storageSize(u.ungrouped.bytes)}</TableCell>
                <TableCell>{st.noQuota}</TableCell>
                <TableCell className="app-wide-only" />
                <TableCell />
              </TableRow>
            )}
          </TableBody>
        </Table>
      </TableContainer>
    </Section>
  );
}

/** Every account with its size; the group column while several groups exist. */
function AccountsSection({ usage: u, groupNames }: { usage: UsageAdmin; groupNames: Map<number, string> | null }) {
  const st = storageText();
  const sorted = [...u.accounts].sort((a, b) => b.bytes - a.bytes || a.name.localeCompare(b.name));
  return (
    <Section title={st.accountsTitle} help={st.accountsHelp}>
      <TableContainer className="users-table app-table">
        <Table aria-label={st.accountsTitle} size="lg">
          <TableHead>
            <TableRow>
              <TableHeader>{st.colAccount}</TableHeader>
              <TableHeader className="app-wide-only">{st.colOwner}</TableHeader>
              {groupNames && <TableHeader className="app-wide-only">{st.colGroup}</TableHeader>}
              <TableHeader className="app-wide-only">{st.colMessages}</TableHeader>
              <TableHeader>{st.colSize}</TableHeader>
              <TableHeader className="app-wide-only">{st.colExclusive}</TableHeader>
            </TableRow>
          </TableHead>
          <TableBody>
            {sorted.map((a) => (
              <TableRow key={a.id}>
                <TableHeader scope="row">
                  <span className="users-table__name">{a.name}</span>{" "}
                  {a.removed && (
                    <Tag size="sm" type="gray">
                      {st.removed}
                    </Tag>
                  )}
                </TableHeader>
                <TableCell className="app-wide-only">{a.owner || st.noOwner}</TableCell>
                {groupNames && <TableCell className="app-wide-only">{a.groupId === null ? st.withoutGroup : (groupNames.get(a.groupId) ?? "")}</TableCell>}
                <TableCell className="app-wide-only app-tabular">{a.counted ? formatCount(a.messages) : ""}</TableCell>
                <TableCell className="app-tabular">{a.counted ? storageSize(a.bytes) : st.notCounted}</TableCell>
                <TableCell className="app-wide-only app-tabular">{a.counted ? storageSize(a.exclusiveBytes) : ""}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableContainer>
    </Section>
  );
}

const units = [
  { key: "MiB", bytes: 1024 ** 2 },
  { key: "GiB", bytes: 1024 ** 3 },
  { key: "TiB", bytes: 1024 ** 4 },
] as const;
type Unit = (typeof units)[number]["key"];

/** The largest unit that shows a quota as a whole number, else GiB. */
function initialSize(quota: number | null): { value: string; unit: Unit } {
  if (quota === null) return { value: "", unit: "GiB" };
  for (const u of [...units].reverse()) if (quota % u.bytes === 0) return { value: String(quota / u.bytes), unit: u.key };
  return { value: String(Math.round((quota / 1024 ** 3) * 100) / 100), unit: "GiB" };
}

/**
 * The largest quota the server takes: 1 EiB, the bound of the CLI and of
 * PATCH /api/groups/{id} (`config.MaxByteSize`); above it the server's
 * quota thresholds would overflow.
 */
export const maxQuotaBytes = 2 ** 60;

/** A size as typed, with a decimal point or comma; NaN when it is not a number. */
export function parseSize(value: string, unit: Unit): number {
  const v = value.trim().replace(",", ".");
  if (!/^\d+(\.\d+)?$/.test(v)) return NaN;
  return Math.round(Number(v) * units.find((u) => u.key === unit)!.bytes);
}

/**
 * Edit quota: a size, or none. It says what the quota means for the group
 * now (its share, whether it pauses at once) and refuses a quota above the
 * instance quota or above 1 EiB before sending.
 */
function QuotaModal({ group, usage, returnFocus, cancel, saved }: {
  group: Group;
  usage: UsageAdmin;
  returnFocus: () => HTMLElement | null;
  cancel: () => void;
  saved: (text: string) => void;
}) {
  const st = storageText();
  const id = useId();
  const initial = initialSize(group.quotaBytes);
  const [none, setNone] = useState(group.quotaBytes === null);
  const [value, setValue] = useState(initial.value);
  const [unit, setUnit] = useState<Unit>(initial.unit);
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const field = useRef<HTMLInputElement>(null);
  const limit = instanceLimit(usage);
  const bytes = none ? null : parseSize(value, unit);
  const invalid = bytes !== null && !(bytes > 0);
  const above = bytes !== null && !invalid && limit !== null && bytes > limit;
  const aboveMax = bytes !== null && !invalid && !above && bytes > maxQuotaBytes;
  const percent = bytes && !invalid ? percentOf(group.bytes, bytes) : null;
  const pauses = percent !== null && percent >= 100;
  const warns = percent !== null && !pauses && percent >= usage.instance.warnPercent;

  const submit = async (e?: FormEvent) => {
    e?.preventDefault();
    if (busy) return;
    setTried(true);
    if (invalid || above || aboveMax) {
      field.current?.focus();
      return;
    }
    setBusy(true);
    setError("");
    try {
      await groupsApi.setQuota(group.id, bytes);
      saved(bytes === null ? st.quotaRemoved(group.name) : st.quotaSet(group.name, storageSize(bytes)));
    } catch (err) {
      setError(t.failed(err instanceof ApiError || err instanceof Error ? err.message : String(err)));
      setBusy(false);
    }
  };

  return (
    <AppModal
      size="sm"
      danger={pauses}
      modalHeading={st.quotaTitle(group.name)}
      primaryButtonText={st.quotaSave}
      secondaryButtonText={t.cancel}
      loadingStatus={busy ? "active" : "inactive"}
      loadingDescription={st.quotaSave}
      loadingIconDescription={t.loading}
      preventCloseOnClickOutside
      returnFocus={returnFocus}
      onRequestClose={() => {
        if (!busy) cancel();
      }}
      onRequestSubmit={() => void submit()}
    >
      <form className="modal-stack" onSubmit={(e) => void submit(e)} noValidate>
        <p>{st.quotaNow(group.name, storageSize(group.bytes))}</p>
        <Checkbox id={`${id}-none`} labelText={st.quotaNone} checked={none} onChange={(_, { checked }) => setNone(checked)} />
        {!none && (
          <div className="storage-quota-size">
            <TextInput
              id={`${id}-size`}
              ref={field}
              data-modal-primary-focus=""
              labelText={st.quotaSize}
              inputMode="decimal"
              autoComplete="off"
              value={value}
              invalid={(tried || value !== "") && (invalid || above || aboveMax)}
              invalidText={
                above && limit !== null ? st.quotaAboveInstance(storageSize(limit)) : aboveMax ? st.quotaAboveMax(storageSize(maxQuotaBytes)) : st.quotaInvalid
              }
              onChange={(e) => setValue(e.target.value)}
            />
            <Select id={`${id}-unit`} labelText={st.quotaUnit} value={unit} onChange={(e) => setUnit(e.target.value as Unit)}>
              {units.map((u) => (
                <SelectItem key={u.key} value={u.key} text={u.key} />
              ))}
            </Select>
          </div>
        )}
        {percent !== null && !above && !aboveMax && (
          <p className={pauses ? "app-text-error" : warns ? "app-text-warning" : undefined}>
            {st.quotaThen(percent)} {pauses ? st.quotaStops : warns ? st.quotaWarn : ""}
          </p>
        )}
        {error && (
          <p role="alert" className="app-text-error">
            {error}
          </p>
        )}
      </form>
    </AppModal>
  );
}
