/**
 * The setup link of a new instance (#396): `<public URL>/#setup=<token>`.
 * The token sits in the fragment, so it never reaches the server, a proxy's
 * log or a Referer. The page reads it once, removes it from the address bar
 * at once (history.replaceState, no new history entry) and keeps it only in
 * memory until the setup is done; the setup form sends it in the request
 * body.
 */

let taken: string | null | undefined;

/** The token of the setup link the page was opened with, or null. */
export function takeSetupToken(): string | null {
  if (taken !== undefined) return taken;
  taken = null;
  const m = /^#setup=(.+)$/.exec(window.location.hash);
  if (m) {
    try {
      taken = decodeURIComponent(m[1]!);
    } catch {
      taken = m[1]!;
    }
    window.history.replaceState(window.history.state, "", window.location.pathname + window.location.search);
  }
  return taken;
}

/** Drops the token from memory once it is used or no longer of use. */
export function forgetSetupToken(): void {
  taken = null;
}

/** For tests: the next takeSetupToken reads the address again. */
export function resetSetupLinkForTests(): void {
  taken = undefined;
}
