// Fills: a copy of an IMAP mailbox into an smtp account (#89), into its
// folder JOURNAL, once or on a schedule (a recurring fill, #112).

import type { Account, AccountsResponse, Fill, FillFolder } from "./api";
import { durationMinutes, formatEvery, relativeTime } from "./format";
import { t } from "./i18n";

type Counts = Pick<FillFolder, "scanned" | "imported" | "new" | "afterCutoff" | "journaled" | "present" | "deleted" | "waiting">;

/** The counts of a fill's folders added up; for a recurring fill, of its last run. */
export function fillTotals(f: Fill): Counts {
  const sum: Counts = { scanned: 0, imported: 0, new: 0, afterCutoff: 0, journaled: 0, present: 0, deleted: 0, waiting: 0 };
  for (const ff of f.folders) {
    for (const k of Object.keys(sum) as (keyof Counts)[]) sum[k] += ff[k];
  }
  return sum;
}

/** A fill of an smtp account, for its card: what runs, or how it ended. */
export function fillStatus(a: Account, f: Fill | null, now = new Date()): { text: string; error?: string } | null {
  if (!f) return null;
  const c = fillTotals(f);
  if (f.status === "running") {
    if (a.sync.state === "queued") return { text: t.fillQueued };
    // A recurring run waits for the hold after the server's start.
    if (f.recurringSince && a.sync.state === "idle" && f.nextAt && Date.parse(f.nextAt) > now.getTime()) {
      return { text: t.fillWaitsUntil(relativeTime(f.nextAt, now)) };
    }
    if (f.recurringSince) return { text: t.recurringRunning(c.scanned, c.imported, c.waiting, c.journaled) };
    return { text: t.fillRunning(c.scanned, c.imported, c.afterCutoff, c.journaled) };
  }
  const when = relativeTime(f.finishedAt ?? f.createdAt, now);
  if (f.keepRunning) return { text: recurringStatus(f, now), error: f.error || undefined };
  switch (f.status) {
    case "ok":
      return { text: t.fillOk(when, c.imported, c.journaled) };
    case "partial":
      return { text: t.fillPartial(when, c.imported), error: f.folders.find((ff) => ff.error)?.error || f.error || undefined };
    case "cancelled":
      return { text: f.recurringSince ? t.fillStopped(when) : t.fillCancelled(when, c.imported) };
    default:
      return { text: t.fillFailed(when), error: f.error || undefined };
  }
}

/**
 * A recurring fill between runs: "Imports new mail every 6 hours · next
 * in 3 hours · last run 2 hours ago: ok · 1,234 imported, 12 waiting".
 */
export function recurringStatus(f: Fill, now = new Date()): string {
  const parts: string[] = [];
  if (f.every) {
    parts.push(t.recurringEvery(formatEvery(f.every)));
    if (f.nextAt) parts.push(Date.parse(f.nextAt) <= now.getTime() ? t.dueNow : t.nextSync(relativeTime(f.nextAt, now)));
  } else {
    parts.push(t.recurringOff);
  }
  if (f.status !== "running" && f.status !== "cancelled" && f.finishedAt) {
    parts.push(t.recurringLastRun(relativeTime(f.finishedAt, now), t.runOutcome(f.status)));
  }
  const c = fillTotals(f);
  parts.push(t.recurringCounts(c.imported, c.waiting));
  return parts.join(" · ");
}

/** A fill that "Resume" continues: it failed, partly failed or was cancelled, and does not keep running. */
export function resumable(f: Fill | null): f is Fill {
  return !!f && !f.keepRunning && (f.status === "failed" || f.status === "partial" || f.status === "cancelled");
}

/** An ended one-time fill that can be switched to keep importing new mail. */
export function canKeepRunning(f: Fill | null): f is Fill {
  return !!f && !f.recurringSince && f.status !== "running";
}

/** A fill that "Stop" or "Cancel" ends: it runs, or it keeps running. */
export function stoppable(f: Fill): boolean {
  return f.status === "running" || f.keepRunning;
}

/** Presets of the interval of a recurring fill, as Go durations. */
const fillIntervalPresets = ["1h0m0s", "6h0m0s", "12h0m0s", "24h0m0s"];

/**
 * The choices for a recurring fill's interval: the server's default, the
 * presets at or above the server's floor, and a stored value that is no
 * preset. A recurring fill is always on the schedule.
 */
export function fillIntervalOptions(current: string | null, data: Pick<AccountsResponse, "syncInterval" | "syncIntervalMin">): { value: string; label: string }[] {
  const floor = durationMinutes(data.syncIntervalMin) ?? 0;
  const options = [{ value: "default", label: t.intervalDefault(data.syncInterval ? formatEvery(data.syncInterval) : t.intervalManual) }];
  for (const p of fillIntervalPresets) {
    if ((durationMinutes(p) ?? 0) >= floor) options.push({ value: p, label: capitalize(formatEvery(p)) });
  }
  if (current && current !== "default" && !options.some((o) => o.value === current)) {
    options.push({ value: current, label: t.intervalOther(formatEvery(current)) });
  }
  return options;
}

function capitalize(s: string): string {
  return s.charAt(0).toLocaleUpperCase() + s.slice(1);
}

const pad = (n: number) => String(n).padStart(2, "0");

/**
 * Turns the value of a datetime-local input ("2026-10-01T08:30"), a time in
 * the browser's zone, into RFC 3339 with that zone's offset. Empty or
 * invalid input gives undefined: the server then takes now.
 */
export function cutoffFromInput(value: string): string | undefined {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2})?$/.test(value)) return undefined;
  const d = new Date(value); // without an offset: local time
  if (Number.isNaN(d.getTime())) return undefined;
  const off = -d.getTimezoneOffset();
  const sign = off >= 0 ? "+" : "-";
  const zone = `${sign}${pad(Math.floor(Math.abs(off) / 60))}:${pad(Math.abs(off) % 60)}`;
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}${zone}`;
}
