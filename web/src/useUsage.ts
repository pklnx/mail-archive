import { useCallback, useEffect, useRef, useState } from "react";
import { usageApi, type Usage } from "./api";

// The storage figures and states (#397, part 4) for the banner, the account
// page and the Storage page. Polled slowly: the states change at most every
// 10 seconds, and the figures with the hourly count.
const POLL_MS = 60000;

export interface UsageState {
  data: Usage | null;
  error: string;
  /** Fetch now, e.g. after a quota change. */
  reload: () => void;
}

/** enabled false (an auditor, #393) fetches nothing. */
export function useUsage(enabled: boolean): UsageState {
  const [data, setData] = useState<Usage | null>(null);
  const [error, setError] = useState("");
  const [tick, setTick] = useState(0);
  const timer = useRef<number | undefined>(undefined);
  const reload = useCallback(() => setTick((n) => n + 1), []);

  useEffect(() => {
    if (!enabled) return;
    const ctrl = new AbortController();
    usageApi.get(ctrl.signal).then(
      (d) => {
        setData(d);
        setError("");
        timer.current = window.setTimeout(reload, POLL_MS);
      },
      (err: unknown) => {
        if (ctrl.signal.aborted) return;
        setError(err instanceof Error ? err.message : String(err));
        timer.current = window.setTimeout(reload, POLL_MS);
      },
    );
    return () => {
      ctrl.abort();
      window.clearTimeout(timer.current);
    };
  }, [tick, reload, enabled]);

  return { data, error, reload };
}
