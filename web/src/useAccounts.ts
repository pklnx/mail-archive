import { useCallback, useEffect, useRef, useState } from "react";
import { listAccounts, type AccountsResponse } from "./api";

// Poll quickly while a sync is queued or running, slowly otherwise so that
// syncs started by the schedule or the CLI show up too.
const ACTIVE_MS = 2000;
const IDLE_MS = 30000;

export interface AccountsState {
  data: AccountsResponse | null;
  error: string;
  /** Fetch now, e.g. after a change; also restarts fast polling. */
  reload: () => void;
}

export function isActive(data: AccountsResponse | null): boolean {
  return !!data?.accounts.some((a) => a.sync.state !== "idle");
}

export function useAccounts(): AccountsState {
  const [data, setData] = useState<AccountsResponse | null>(null);
  const [error, setError] = useState("");
  const [tick, setTick] = useState(0);
  const timer = useRef<number | undefined>(undefined);

  const reload = useCallback(() => setTick((n) => n + 1), []);

  useEffect(() => {
    const ctrl = new AbortController();
    listAccounts(ctrl.signal).then(
      (d) => {
        setData(d);
        setError("");
        timer.current = window.setTimeout(reload, isActive(d) ? ACTIVE_MS : IDLE_MS);
      },
      (err: unknown) => {
        if (ctrl.signal.aborted) return;
        setError(err instanceof Error ? err.message : String(err));
        timer.current = window.setTimeout(reload, IDLE_MS);
      },
    );
    return () => {
      ctrl.abort();
      window.clearTimeout(timer.current);
    };
  }, [tick, reload]);

  return { data, error, reload };
}
