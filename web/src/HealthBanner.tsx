import { useEffect, useRef, useState } from "react";
import type { AccountsResponse } from "./api";
import { t } from "./i18n";

export interface BannerState {
  /** Own accounts whose syncs keep failing, and those not synced for a while. */
  failing: string[];
  stale: string[];
  /** Admins only: failing accounts of other users. */
  otherFailing: number;
}

/** What the banner shows: the user's own failing and stale accounts by name, and for admins a count of others. */
export function bannerState(data: AccountsResponse | null): BannerState {
  const own = (data?.accounts ?? []).filter((a) => a.kind === "imap" && !a.removed && a.enabled);
  return {
    failing: own.filter((a) => a.sync.health === "failing").map((a) => a.name),
    stale: own.filter((a) => a.sync.health === "stale").map((a) => a.name),
    otherFailing: data?.otherFailing ?? 0,
  };
}

interface Props {
  data: AccountsResponse | null;
  admin: boolean;
  openAccounts: () => void;
  openUsers: () => void;
}

const link = "font-medium underline underline-offset-2";

/** A banner above the main view when syncs keep failing, for people without a monitoring stack. */
export function HealthBanner({ data, admin, openAccounts, openUsers }: Props) {
  const b = bannerState(data);
  // Screen readers hear the failing accounts once per change, not on every poll.
  const failingText = b.failing.length ? t.bannerFailing(b.failing.join(", ")) : "";
  const shown = useRef("");
  const [announce, setAnnounce] = useState("");
  useEffect(() => {
    if (failingText === shown.current) return;
    shown.current = failingText;
    setAnnounce(failingText);
  }, [failingText]);

  const others = admin ? b.otherFailing : 0;
  if (!b.failing.length && !b.stale.length && !others) {
    return <span role="alert" className="sr-only">{announce}</span>;
  }
  return (
    <div
      className={`flex flex-col gap-1 border-b border-zinc-200 px-3 py-2 text-sm dark:border-zinc-800 ${
        b.failing.length ? "bg-red-50 dark:bg-red-950/40" : b.stale.length ? "bg-amber-50 dark:bg-amber-950/40" : ""
      }`}
    >
      <span role="alert" className="sr-only">{announce}</span>
      {b.failing.length > 0 && (
        <p className="text-red-700 dark:text-red-400">
          {failingText}{" "}
          <button type="button" className={link} onClick={openAccounts}>
            {t.bannerOpenAccounts}
          </button>
        </p>
      )}
      {b.stale.length > 0 && (
        <p className="text-amber-700 dark:text-amber-500">
          {t.bannerStale(b.stale.join(", "))}
          {!b.failing.length && (
            <>
              {" "}
              <button type="button" className={link} onClick={openAccounts}>
                {t.bannerOpenAccounts}
              </button>
            </>
          )}
        </p>
      )}
      {others > 0 && (
        <p className="text-zinc-600 dark:text-zinc-400">
          {t.bannerOthers(others)}{" "}
          <button type="button" className={link} onClick={openUsers}>
            {t.bannerOpenUsers}
          </button>
        </p>
      )}
    </div>
  );
}
