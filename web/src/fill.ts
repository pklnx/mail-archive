// Fills: a one-time copy of an IMAP mailbox into an smtp account (#89).

import type { Account, Fill, FillFolder } from "./api";
import { relativeTime } from "./format";
import { t } from "./i18n";

type Counts = Pick<FillFolder, "scanned" | "imported" | "new" | "afterCutoff" | "journaled" | "present" | "deleted">;

/** The counts of a fill's folders added up. */
export function fillTotals(f: Fill): Counts {
  const sum: Counts = { scanned: 0, imported: 0, new: 0, afterCutoff: 0, journaled: 0, present: 0, deleted: 0 };
  for (const ff of f.folders) {
    for (const k of Object.keys(sum) as (keyof Counts)[]) sum[k] += ff[k];
  }
  return sum;
}

/** The latest fill of an smtp account, for its card. */
export function fillStatus(a: Account, now = new Date()): { text: string; error?: string } | null {
  const f = a.fill;
  if (!f) return null;
  const c = fillTotals(f);
  if (f.status === "running") {
    if (a.sync.state === "queued") return { text: t.fillQueued };
    return { text: t.fillRunning(c.scanned, c.imported, c.afterCutoff, c.journaled) };
  }
  const when = relativeTime(f.finishedAt ?? f.createdAt, now);
  switch (f.status) {
    case "ok":
      return { text: t.fillOk(when, c.imported, c.journaled) };
    case "partial":
      return { text: t.fillPartial(when, c.imported), error: f.folders.find((ff) => ff.error)?.error || f.error || undefined };
    case "cancelled":
      return { text: t.fillCancelled(when, c.imported) };
    default:
      return { text: t.fillFailed(when), error: f.error || undefined };
  }
}

/** A fill that "Resume" continues: it failed, partly failed or was cancelled. */
export function resumable(f: Fill | null): f is Fill {
  return !!f && (f.status === "failed" || f.status === "partial" || f.status === "cancelled");
}

/**
 * The folder prefix the server uses when none is given: the user name
 * without control characters, surrounding spaces and slashes.
 */
export function defaultFillPrefix(username: string): string {
  return username.trim().replace(/[\u0000-\u001f\u007f-\u009f]/g, "").replace(/^\/+|\/+$/g, "");
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
