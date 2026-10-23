/** A sheet, a Carbon Modal or the folder overlay is open: the keyboard shortcuts are off while one is. */
export function dialogOpen(doc: Document = document): boolean {
  return doc.querySelector("dialog[open], .cds--modal.is-visible, .folder-pane--overlay") !== null;
}
