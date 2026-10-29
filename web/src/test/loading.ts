import { expect } from "vitest";
import { t } from "../i18n";

/** The skeleton classes of Carbon: SkeletonText and DataTableSkeleton. */
const skeleton = ".cds--skeleton__text, .cds--data-table.cds--skeleton";

/**
 * Checks a loading state (#287) inside root: Carbon skeletons, the text
 * "Loading…" only in a visually hidden status, and no visible loading text.
 * With announce: false the skeleton has no status (another part of the
 * screen announces). Returns the status, or null.
 */
export function expectLoading(root: ParentNode = document.body, { announce = true }: { announce?: boolean } = {}): HTMLElement | null {
  expect(root.querySelector(skeleton), "a skeleton").not.toBeNull();
  // The skeletons are hidden from screen readers.
  for (const s of root.querySelectorAll(skeleton)) expect(s.closest("[aria-hidden]"), "skeleton in aria-hidden").not.toBeNull();
  const statuses = [...root.querySelectorAll<HTMLElement>('[role="status"]')].filter((s) => s.textContent?.includes(t.loading));
  // No loading text that is not visually hidden.
  for (const el of root.querySelectorAll<HTMLElement>("*")) {
    for (const n of el.childNodes) {
      if (n.nodeType === Node.TEXT_NODE && n.textContent?.includes(t.loading)) expect(el.closest(".cds--visually-hidden"), "visible Loading…").not.toBeNull();
    }
  }
  if (!announce) {
    expect(statuses).toHaveLength(0);
    return null;
  }
  expect(statuses).toHaveLength(1);
  const status = statuses[0]!;
  expect(status.querySelector(".cds--visually-hidden")?.textContent ?? (status.classList.contains("cds--visually-hidden") ? status.textContent : null)).toBe(t.loading);
  expect(status.getAttribute("aria-busy")).toBeNull();
  return status;
}

/** No loading state is left inside root: no skeleton, no "Loading…" status. */
export function expectLoaded(root: ParentNode = document.body) {
  expect(root.querySelector(skeleton), "a skeleton").toBeNull();
  expect([...root.querySelectorAll('[role="status"]')].filter((s) => s.textContent?.includes(t.loading))).toHaveLength(0);
}
