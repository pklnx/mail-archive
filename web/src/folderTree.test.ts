import { describe, expect, it } from "vitest";
import type { Folder } from "./api";
import { ancestorPaths, buildFolderTree, nodePaths, parentPaths, subtreeMessages, type FolderNode } from "./folderTree";

const f = (name: string, delimiter: string | null = "/", messages = 1): Folder => ({
  name, delimiter, messages, lastSyncedAt: null, lastReconciledAt: null,
});

/** The tree as indented labels, "*" for path-only nodes. */
function shape(nodes: FolderNode[], depth = 0): string[] {
  return nodes.flatMap((n) => [`${"  ".repeat(depth)}${n.label}${n.folder ? "" : "*"}`, ...shape(n.children, depth + 1)]);
}

describe("buildFolderTree", () => {
  it("splits at the folder's delimiter", () => {
    const tree = buildFolderTree([f("INBOX"), f("Archive/2024"), f("Archive/2025"), f("Archive"), f("Sent")]);
    expect(shape(tree)).toEqual(["INBOX", "Archive", "  2024", "  2025", "Sent"]);
    expect(tree[1]!.path).toBe("Archive");
    expect(tree[1]!.children[0]!.path).toBe("Archive/2024");
    expect(tree[1]!.children[0]!.folder?.name).toBe("Archive/2024");
  });
  it("uses '.' servers' hierarchy, INBOX included", () => {
    const tree = buildFolderTree([f("INBOX", "."), f("INBOX.Sent", "."), f("INBOX.Archive.2024", ".")]);
    expect(shape(tree)).toEqual(["INBOX", "  Archive*", "    2024", "  Sent"]);
    expect(tree[0]!.children[0]!.path).toBe("INBOX.Archive");
  });
  it("shows missing parents as path-only nodes", () => {
    expect(shape(buildFolderTree([f("Lists/dev/go")]))).toEqual(["Lists*", "  dev*", "    go"]);
  });
  it("keeps unknown and empty delimiters flat", () => {
    expect(shape(buildFolderTree([f("a/b", null), f("c/d", null)]))).toEqual(["a/b", "c/d"]);
    expect(shape(buildFolderTree([f("a/b", ""), f("c.d", "")]))).toEqual(["a/b", "c.d"]);
  });
  it("falls back to the account's delimiter for unknown folders", () => {
    expect(shape(buildFolderTree([f("INBOX", "."), f("Old.2019", null)]))).toEqual(["INBOX", "Old*", "  2019"]);
    // Several known delimiters: no guess.
    expect(shape(buildFolderTree([f("a", "."), f("b", "/"), f("c/d", null)]))).toEqual(["a", "b", "c/d"]);
  });
  it("does not split names with empty parts or at other characters", () => {
    expect(shape(buildFolderTree([f("/lead"), f("trail/"), f("a//b")]))).toEqual(["/lead", "a//b", "trail/"]);
    expect(shape(buildFolderTree([f("Mr. Smith")]))).toEqual(["Mr. Smith"]);
  });
  it("sorts INBOX first at the root, then by name with numbers in order", () => {
    const tree = buildFolderTree([f("b"), f("Inbox"), f("A"), f("x/10"), f("x/9"), f("x/INBOX"), f("x/a")]);
    expect(shape(tree)).toEqual(["Inbox", "A", "b", "x*", "  9", "  10", "  a", "  INBOX"]);
  });
  it("sorts JOURNAL first in smtp accounts, before the folders of older fills", () => {
    const folders = [f("alice/INBOX"), f("JOURNAL"), f("INBOX")];
    expect(shape(buildFolderTree(folders, "smtp"))).toEqual(["JOURNAL", "alice*", "  INBOX", "INBOX"]);
    expect(shape(buildFolderTree(folders, "imap"))).toEqual(["INBOX", "alice*", "  INBOX", "JOURNAL"]);
  });
});

describe("tree helpers", () => {
  const folders = [f("Archive", "/", 2), f("Archive/2024", "/", 3), f("Archive/Tax/2023", "/", 4), f("INBOX", "/", 5)];
  const tree = buildFolderTree(folders);
  it("sums the subtree", () => {
    expect(subtreeMessages(tree[1]!)).toBe(9);
    expect(subtreeMessages(tree[1]!.children[1]!)).toBe(4);
  });
  it("lists the ancestors of a folder", () => {
    expect(ancestorPaths(folders, "Archive/Tax/2023")).toEqual(["Archive", "Archive/Tax"]);
    expect(ancestorPaths(folders, "INBOX")).toEqual([]);
    expect(ancestorPaths(folders, "missing")).toEqual([]);
  });
  it("lists every node and every parent", () => {
    expect(nodePaths(tree)).toEqual(["INBOX", "Archive", "Archive/2024", "Archive/Tax", "Archive/Tax/2023"]);
    expect(parentPaths(tree)).toEqual(["Archive", "Archive/Tax"]);
  });
});
