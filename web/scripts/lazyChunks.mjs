// The chunks that load on demand (#288), by the module name Rolldown gives
// them (`AccountsPage-<hash>.js`). One list for the bundle check
// (check-dist.mjs) and the e2e suite (e2e/lazy.spec.ts). A new lazy view
// adds its module here.
export const lazyChunks = ["AccountsPage", "UsersPage", "RetentionPage", "LogPage", "Profile", "ShortcutsModal"];

// The most initial JS the mail view may load, in raw bytes: the entry chunk
// and every chunk it imports statically. 579 000 is the #282 "Done when"
// (the bundle size #186 measured). Raise it only in a PR that says why;
// above 579 000 the #282 "Done when" no longer holds.
export const initialBudget = 579_000;

/** Whether a file in dist/assets is the chunk of the lazy module `name`. */
export function isChunkOf(file, name) {
  return new RegExp(`^${name}-[A-Za-z0-9_-]+\\.js$`).test(file);
}
