// Previous and next message for the reading pane, in the order of the list.

export interface ListNav {
  /** The messages the list has loaded, top to bottom. */
  ids: string[];
  /** The list has more pages. */
  more: boolean;
  /** Loads the next page; the list calls back with the longer ids. */
  loadMore: () => void;
}

export const noList: ListNav = { ids: [], more: false, loadMore: () => {} };

export interface Pager {
  /** Position in the list, from 0; -1 if the message is not in it. */
  index: number;
  prev: string | null;
  next: string | null;
  /** The message is the last one loaded, and the list has more. */
  nextNeedsMore: boolean;
}

export function pager(nav: ListNav, current: string): Pager {
  const index = current ? nav.ids.indexOf(current) : -1;
  if (index < 0) return { index, prev: null, next: null, nextNeedsMore: false };
  const next = nav.ids[index + 1] ?? null;
  return { index, prev: nav.ids[index - 1] ?? null, next, nextNeedsMore: !next && nav.more };
}
