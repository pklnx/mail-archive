import { useEffect, useRef } from "react";

/**
 * Closes a popover when the focus moves into an iframe (the mail frame,
 * #349). A click into a same-origin frame reaches no listener of this
 * window: Carbon's outside-click handler never fires, the blur has no
 * relatedTarget, and Escape goes to the frame's document. The window's
 * blur does fire then, with the frame as the active element. A tab switch
 * also blurs the window, but leaves the active element where it was, so
 * the popover stays. The listener exists only while the popover is open.
 */
export function useFrameFocusClose(open: boolean, close: () => void) {
  const closeRef = useRef(close);
  closeRef.current = close;
  useEffect(() => {
    if (!open) return;
    const onBlur = () => {
      if (document.activeElement instanceof HTMLIFrameElement) closeRef.current();
    };
    window.addEventListener("blur", onBlur);
    return () => window.removeEventListener("blur", onBlur);
  }, [open]);
}
