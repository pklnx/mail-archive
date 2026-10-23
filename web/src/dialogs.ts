/** A sheet or a Carbon Modal is open: the keyboard shortcuts are off while one is. */
export function dialogOpen(doc: Document = document): boolean {
  return doc.querySelector("dialog[open], .cds--modal.is-visible") !== null;
}
