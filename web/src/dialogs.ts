/** A Carbon Modal, a native dialog or the folder overlay is open: the keyboard shortcuts are off while one is. */
export function dialogOpen(doc: Document = document): boolean {
  return doc.querySelector("dialog[open], .cds--modal.is-visible, .folder-pane--overlay") !== null;
}

/** Fields that take typed keys. Carbon's Search, TextInput, Select and Checkbox are native fields too. */
function editable(el: Element): boolean {
  return (el instanceof HTMLElement && el.isContentEditable) || el.closest('[contenteditable]:not([contenteditable="false"])') !== null || ["INPUT", "TEXTAREA", "SELECT"].includes(el.tagName);
}

/**
 * The floating and composite parts of Carbon components that handle keys
 * themselves: the content of an open popover (Toggletip, menus), menus,
 * list boxes, combo boxes and dialogs.
 */
const widget = '.cds--popover-content, [role="menu"], [role="menubar"], [role="listbox"], [role="combobox"], [role="dialog"], [role="alertdialog"], .cds--modal';

/**
 * Whether a key press belongs to the element it happens in, so the app's
 * keyboard shortcuts leave it alone:
 * - every key in a field and inside an open popover, menu, list box or dialog;
 * - Escape while a Carbon popover or tooltip is open anywhere (Carbon closes
 *   it on Escape from the document, wherever the focus is) and inside the
 *   shell's SideNav (Carbon collapses the rail on it).
 *
 * The shortcut listener runs in the capture phase (so a focused node of
 * Carbon's TreeView, which stops its keys from bubbling, does not swallow
 * them). That is before any component sees the key, so defaultPrevented and
 * stopPropagation of the components cannot tell; this check does instead.
 */
export function ownedByWidget(e: KeyboardEvent, doc: Document = document): boolean {
  const el = e.target instanceof Element ? e.target : null;
  if (el && (editable(el) || el.closest(widget))) return true;
  if (e.key === "Escape") {
    if (doc.querySelector(".cds--popover--open")) return true;
    if (el?.closest(".cds--side-nav")) return true;
  }
  return false;
}
