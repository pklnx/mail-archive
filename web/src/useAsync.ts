import { useEffect, useState } from "react";

export type Async<T> = { status: "loading" } | { status: "error"; error: Error } | { status: "ok"; data: T };

/** Runs fn whenever deps change; aborts the previous request. */
export function useAsync<T>(fn: (signal: AbortSignal) => Promise<T>, deps: unknown[]): Async<T> {
  const [state, setState] = useState<Async<T>>({ status: "loading" });
  useEffect(() => {
    const ctrl = new AbortController();
    setState({ status: "loading" });
    fn(ctrl.signal).then(
      (data) => setState({ status: "ok", data }),
      (error: unknown) => {
        if (!ctrl.signal.aborted) setState({ status: "error", error: error instanceof Error ? error : new Error(String(error)) });
      },
    );
    return () => ctrl.abort();
  }, deps);
  return state;
}
