import { useEffect, useRef, useState } from "react";
import { ActionableNotification, FeatureFlags } from "@carbon/react";
import { isAdminUsage, type AccountsResponse, type StorageState, type Usage } from "./api";
import { t } from "./i18n";

export interface BannerState {
  /** Own accounts whose syncs (or recurring fills) keep failing, and those not synced for a while. */
  failing: string[];
  stale: string[];
  /** Admins only: failing accounts of other users. */
  otherFailing: number;
}

/**
 * What the banner shows: the user's own failing and stale accounts by name,
 * smtp accounts with a failing or stale recurring fill as "journal
 * (ak@example.com)", and for admins a count of others.
 */
export function bannerState(data: AccountsResponse | null): BannerState {
  const live = (data?.accounts ?? []).filter((a) => !a.removed && a.enabled);
  const own = live.filter((a) => a.kind === "imap");
  const fills = live.flatMap((a) => (a.kind === "smtp" ? a.fills.map((f) => ({ a, f })) : []));
  const fillNames = (state: "failing" | "stale") =>
    fills.filter(({ f }) => f.keepRunning && f.health?.state === state).map(({ a, f }) => t.bannerFill(a.name, f.username));
  return {
    failing: [...own.filter((a) => a.sync.health === "failing").map((a) => a.name), ...fillNames("failing")],
    stale: [...own.filter((a) => a.sync.health === "stale").map((a) => a.name), ...fillNames("stale")],
    otherFailing: data?.otherFailing ?? 0,
  };
}

/** The storage lines of the banner (#397, part 4): what is full and what nearly is. */
export interface StorageBanner {
  /** The text of the error line, "" without one. */
  full: string;
  /** The text of the warning line (admins only), "" without one. */
  warn: string;
}

const stops = (s: StorageState) => s === "full" || s === "overflow" || s === "disk_low";

/**
 * The storage lines. A member learns only that new mail of their group is
 * not archived (Q7), never which limit was reached. An admin sees every
 * scope in warn, full, overflow or disk_low by name: the instance, the disk
 * reserve, and the groups; while only one group exists, its name is not
 * shown (#470 decision 10) and its limit counts as the instance's.
 */
export function storageBanner(usage: Usage | null): StorageBanner {
  if (!usage) return { full: "", warn: "" };
  if (!isAdminUsage(usage)) return { full: usage.state === "full" || usage.state === "overflow" ? t.bannerStorageFull : "", warn: "" };
  const full = new Set<string>();
  const warn = new Set<string>();
  const add = (state: StorageState, name: string) => {
    if (stops(state)) full.add(name);
    else if (state === "warn") warn.add(name);
  };
  const inst = usage.instance.state;
  add(inst === "disk_low" ? "full" : inst, inst === "disk_low" ? t.bannerStorageDisk : t.bannerStorageInstance);
  const several = usage.groups.length > 1;
  for (const g of usage.groups) add(g.state, several ? g.name : t.bannerStorageInstance);
  for (const n of full) warn.delete(n);
  return {
    full: full.size ? t.bannerStorageFullAdmin([...full].join(", ")) : "",
    warn: warn.size ? t.bannerStorageWarn([...warn].join(", ")) : "",
  };
}

interface Props {
  data: AccountsResponse | null;
  usage?: Usage | null;
  admin: boolean;
  openAccounts: () => void;
  openUsers: () => void;
  openStorage?: () => void;
}

/**
 * One line of the banner. Not a dialog: it appears and changes with the
 * account polls, so it takes no focus and Escape leaves it alone; the
 * announcement comes from the separate live region.
 */
function Line({ kind, text, action, onAction }: { kind: "error" | "warning" | "info"; text: string; action?: string; onAction?: () => void }) {
  return (
    <ActionableNotification
      inline
      lowContrast
      hideCloseButton
      hasFocus={false}
      closeOnEscape={false}
      role="group"
      kind={kind}
      statusIconDescription={t.carbon.status[kind]}
      subtitle={text}
      actionButtonLabel={action}
      onActionButtonClick={onAction}
    />
  );
}

/** A banner above the main view when syncs keep failing, for people without a monitoring stack. */
export function HealthBanner({ data, usage = null, admin, openAccounts, openUsers, openStorage }: Props) {
  const b = bannerState(data);
  const storage = storageBanner(usage);
  // Screen readers hear the failing accounts and a full storage once per
  // change, not on every poll.
  const failingText = b.failing.length ? t.bannerFailing(b.failing.join(", ")) : "";
  const urgent = [storage.full, failingText].filter(Boolean).join(" ");
  const shown = useRef("");
  const [announce, setAnnounce] = useState("");
  useEffect(() => {
    if (urgent === shown.current) return;
    shown.current = urgent;
    setAnnounce(urgent);
  }, [urgent]);

  const others = admin ? b.otherFailing : 0;
  const live = (
    <span role="alert" className="cds--visually-hidden">
      {announce}
    </span>
  );
  // Members have no Storage page: their line has no action.
  const toStorage = admin ? { action: t.bannerOpenStorage, onAction: openStorage } : {};
  if (!b.failing.length && !b.stale.length && !others && !storage.full && !storage.warn) return live;
  return (
    // Without the flag, each notification adds two hidden "Focus sentinel" tab stops.
    <FeatureFlags enableFocusWrapWithoutSentinels>
      <div className="health-banner">
        {live}
        {storage.full && <Line kind="error" text={storage.full} {...toStorage} />}
        {storage.warn && <Line kind="warning" text={storage.warn} {...toStorage} />}
        {b.failing.length > 0 && <Line kind="error" text={failingText} action={t.bannerOpenAccounts} onAction={openAccounts} />}
        {b.stale.length > 0 && (
          <Line
            kind="warning"
            text={t.bannerStale(b.stale.join(", "))}
            // One way to the accounts is enough.
            action={b.failing.length ? undefined : t.bannerOpenAccounts}
            onAction={b.failing.length ? undefined : openAccounts}
          />
        )}
        {others > 0 && <Line kind="info" text={t.bannerOthers(others)} action={t.bannerOpenUsers} onAction={openUsers} />}
      </div>
    </FeatureFlags>
  );
}
