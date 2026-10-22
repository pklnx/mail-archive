/**
 * Whether a click on a link is a plain left click, which the app may handle
 * itself. A click with a modifier key (new tab, new window, download) or
 * another button is left to the browser.
 */
export function isPlainClick(e: { button: number; metaKey: boolean; ctrlKey: boolean; shiftKey: boolean; altKey: boolean }): boolean {
  return e.button === 0 && !e.metaKey && !e.ctrlKey && !e.shiftKey && !e.altKey;
}
