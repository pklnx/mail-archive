// Vitest setup: browser APIs that Carbon components use and jsdom lacks.
// Tests that need a specific answer (theme.test.ts) stub them again. Tests
// in the node environment (build/) have no window and need none of it.

if (typeof window !== "undefined") {
  if (typeof window.ResizeObserver === "undefined") {
    window.ResizeObserver = class ResizeObserver {
      observe() {}
      unobserve() {}
      disconnect() {}
    };
  }
  if (typeof window.matchMedia === "undefined") {
    window.matchMedia = (query: string): MediaQueryList => ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    });
  }
  // jsdom has no layout: every box is 0 by 0, so the hide() step of an
  // autoAlign popover (Carbon on floating-ui) counts its trigger as clipped
  // and sets visibility: hidden on the content. A tooltip that names its
  // button (RowMenu's ⋮, #378) would then give the button no name in
  // getByRole. Browsers name it from the closed tooltip, whose content
  // Carbon hides with display: none, as before.
  const style = document.createElement("style");
  style.textContent = ".cds--autoalign .cds--popover-content { visibility: visible !important; }";
  document.head.append(style);
}
