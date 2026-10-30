// A model of how Safari (WebKit) and Firefox on macOS treat focus on a mouse
// click, for Chromium (#335, S1 and S2). In those browsers a clicked button,
// link, checkbox or radio does not take the focus: the element that had it
// blurs on mousedown with no relatedTarget, and the focus goes to the body.
// Chromium focuses the clicked control instead, so a close-on-blur that
// forgets the null relatedTarget passes every plain Chromium test.
// App.test.tsx models the same in jsdom ("keeps the panel open when a click
// blurs the focus first (WebKit, Firefox on macOS)"); here it runs with the
// real order of the app's, Carbon's and flatpickr's listeners.
//
// What it models: the engines move the focus as the mousedown's default
// action, after every listener has seen the event. So a capture-phase
// mousedown listener, the first to run, cancels the browser's focus move for
// those controls (and for a label of a checkbox or radio), and a bubble-phase
// listener on window, the last to run, blurs the focused element (at the
// latest on mouseup, if a listener stopped the mousedown). The click itself
// still happens. What it does not model: the engines themselves (their event
// order, their focus rings, text fields, which do take the focus, or
// elements with a tabindex), so each check under it keeps a manual remainder
// in real Safari and Firefox.
import type { Page } from "@playwright/test";

/** Installs the model in every document the page loads from now on. */
export async function useClickModel(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const controls = 'button, a[href], input[type="checkbox"], input[type="radio"]';
    let pending = false;
    const blur = () => {
      if (!pending) return;
      pending = false;
      if (document.activeElement instanceof HTMLElement && document.activeElement !== document.body) document.activeElement.blur();
    };
    window.addEventListener(
      "mousedown",
      (e) => {
        const target = e.target instanceof Element ? e.target : null;
        const label = target?.closest("label");
        const control = target?.closest(controls) ?? (label?.control?.matches('input[type="checkbox"], input[type="radio"]') ? label.control : null);
        if (!control) return;
        // No focus move on this mousedown, as in WebKit.
        e.preventDefault();
        pending = true;
      },
      { capture: true },
    );
    // After the listeners: the focused element blurs, with no relatedTarget.
    window.addEventListener("mousedown", blur);
    window.addEventListener("mouseup", blur, { capture: true });
  });
}

/** Whether nothing has the focus (the body has it), as the model leaves it after a mousedown on a control. */
export function focusOnBody(page: Page): Promise<boolean> {
  return page.evaluate(() => document.activeElement === document.body || document.activeElement === null);
}
