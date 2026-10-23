/**
 * The fold of the conversation in the message view (#200): which messages
 * show at once and which sit behind "Show n earlier messages" and "Show n
 * later messages", so a long conversation does not push the body away.
 *
 * - Up to 3 messages: all of them show, without toggles.
 * - From 4 on: the open message and its direct neighbours in time (one
 *   before, one after); the older ones are earlier, the newer ones later.
 * - The open message is not among them (a conversation longer than the
 *   server returns, with an old message open): the newest message shows,
 *   the rest is earlier.
 */
export interface Fold<T> {
  /** Older than the visible messages, oldest first. */
  earlier: T[];
  /** Shown at once, oldest first. */
  visible: T[];
  /** Newer than the visible messages, oldest first. */
  later: T[];
}

/** At most this many messages show without a fold. */
export const unfoldedMax = 3;

/** Folds messages (oldest first) around the open one. */
export function foldConversation<T extends { id: string }>(messages: readonly T[], openId: string): Fold<T> {
  if (messages.length <= unfoldedMax) return { earlier: [], visible: [...messages], later: [] };
  const i = messages.findIndex((m) => m.id === openId);
  if (i < 0) return { earlier: messages.slice(0, -1), visible: messages.slice(-1), later: [] };
  const from = Math.max(0, i - 1);
  const to = Math.min(messages.length, i + 2);
  return { earlier: messages.slice(0, from), visible: messages.slice(from, to), later: messages.slice(to) };
}
