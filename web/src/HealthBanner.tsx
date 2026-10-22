import { useEffect, useRef, useState } from "react";
import { ActionableNotification, FeatureFlags } from "@carbon/react";
import type { AccountsResponse } from "./api";
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

interface Props {
  data: AccountsResponse | null;
  admin: boolean;
  openAccounts: () => void;
  openUsers: () => void;
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
  const live = (
    <span role="alert" className="cds--visually-hidden">
      {announce}
    </span>
  );
  if (!b.failing.length && !b.stale.length && !others) return live;
  return (
    // Without the flag, each notification adds two hidden "Focus sentinel" tab stops.
    <FeatureFlags enableFocusWrapWithoutSentinels>
      <div className="health-banner">
        {live}
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
