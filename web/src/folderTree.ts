// The folders of an account as a tree, split at the server's hierarchy
// delimiter. Pure functions, for the sidebar.
import type { Folder } from "./api";

export interface FolderNode {
  /** Full name up to this node with the folder's delimiter, e.g. "INBOX.Archive". */
  path: string;
  /** The last part, e.g. "Archive". */
  label: string;
  /** null: only a path part, not an archived folder. */
  folder: Folder | null;
  children: FolderNode[];
}

/**
 * The delimiter the account's folders agree on, for folders whose own is
 * not known yet (archived before the upgrade, or never examined): it is the
 * same server. null when none or several are known.
 */
function accountDelimiter(folders: Folder[]): string | null {
  const known = new Set(folders.map((f) => f.delimiter).filter((d): d is string => d !== null));
  return known.size === 1 ? [...known][0]! : null;
}

/** The parts of a folder name and the delimiter to join them; one part when it is not split. */
function split(f: Folder, fallback: string | null): { parts: string[]; delimiter: string } {
  const delimiter = f.delimiter ?? fallback;
  if (!delimiter) return { parts: [f.name], delimiter: "" };
  const parts = f.name.split(delimiter);
  // An empty part (leading, trailing or doubled delimiter) would lose a
  // character in the tree: keep such a name whole.
  if (parts.some((p) => p === "")) return { parts: [f.name], delimiter: "" };
  return { parts, delimiter };
}

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: "base" });

function compare(a: FolderNode, b: FolderNode): number {
  return collator.compare(a.label, b.label) || (a.label < b.label ? -1 : a.label > b.label ? 1 : 0);
}

function sortTree(nodes: FolderNode[], root: boolean) {
  nodes.sort((a, b) => {
    if (root) {
      const ai = a.label.toUpperCase() === "INBOX";
      const bi = b.label.toUpperCase() === "INBOX";
      if (ai !== bi) return ai ? -1 : 1;
    }
    return compare(a, b);
  });
  for (const n of nodes) sortTree(n.children, false);
}

/** The tree of an account's folders: INBOX first, then by name, numbers in numeric order. */
export function buildFolderTree(folders: Folder[]): FolderNode[] {
  const fallback = accountDelimiter(folders);
  const roots: FolderNode[] = [];
  const byPath = new Map<string, FolderNode>();
  for (const f of folders) {
    const { parts, delimiter } = split(f, fallback);
    let siblings = roots;
    let path = "";
    parts.forEach((label, i) => {
      path = i === 0 ? label : path + delimiter + label;
      let node = byPath.get(path);
      if (!node) {
        node = { path, label, folder: null, children: [] };
        byPath.set(path, node);
        siblings.push(node);
      }
      if (i === parts.length - 1) node.folder = f;
      siblings = node.children;
    });
  }
  sortTree(roots, true);
  return roots;
}

/** Own count plus all counts below: the sum of the numbers in the subtree. */
export function subtreeMessages(node: FolderNode): number {
  return node.children.reduce((n, c) => n + subtreeMessages(c), node.folder?.messages ?? 0);
}

/** Paths of the nodes above the folder, to expand when it is selected. */
export function ancestorPaths(folders: Folder[], name: string): string[] {
  const f = folders.find((x) => x.name === name);
  if (!f) return [];
  const { parts, delimiter } = split(f, accountDelimiter(folders));
  return parts.slice(0, -1).map((_, i) => parts.slice(0, i + 1).join(delimiter));
}

/** Every node path of the tree, path-only nodes included. */
export function nodePaths(nodes: FolderNode[]): string[] {
  return nodes.flatMap((n) => [n.path, ...nodePaths(n.children)]);
}

/** Paths of the nodes that have children: the ones that can be collapsed. */
export function parentPaths(nodes: FolderNode[]): string[] {
  return nodes.flatMap((n) => (n.children.length ? [n.path, ...parentPaths(n.children)] : []));
}
