// A fill for the tests: running, two folders, one-time.

import type { Fill } from "./api";

export function fill(more: Partial<Fill> = {}): Fill {
  return {
    id: 3, status: "running", host: "imap.example.com", port: 993, tls: "tls", username: "alice@example.com",
    includedFolders: [], excludedFolders: ["Spam"], folderPrefix: "", before: "2026-10-13T08:00:00Z",
    keepRunning: false, recurringSince: null, runStartedAt: null, interval: null, every: null, nextAt: null, health: null,
    unconfirmedFolders: [], error: "", createdAt: "2026-10-13T08:00:00Z", finishedAt: null,
    folders: [
      { source: "INBOX", folder: "JOURNAL", done: true, error: "", scanned: 10, imported: 7, new: 7, afterCutoff: 1, journaled: 2, present: 0, deleted: 0, waiting: 0 },
      { source: "Sent", folder: "JOURNAL", done: false, error: "", scanned: 5, imported: 5, new: 4, afterCutoff: 0, journaled: 0, present: 0, deleted: 0, waiting: 0 },
    ],
    ...more,
  };
}
