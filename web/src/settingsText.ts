import { lang, type Lang } from "./i18n";

// The texts of the settings pages (Accounts, Users, Retention) that i18n.ts
// does not have. Only these pages import this module, so it is part of their
// lazy chunks and not of the initial bundle (#285).

const en = {
  created: "Created",
  openProfile: "Open your profile",
  // Accounts (#285 part 2).
  colType: "Type",
  colStatus: "Status",
  colLastActivity: "Last activity",
  connection: "Connection",
  foldersArchived: "Folders archived",
  lastSync: "Last sync",
  lastReceived: "Last received",
  lastImport: "Last import",
  notArchived: (folders: string) => `Not archived: ${folders}`,
  imports: "Imports from IMAP",
};

const de: typeof en = {
  created: "Erstellt",
  openProfile: "Dein Profil öffnen",
  colType: "Typ",
  colStatus: "Status",
  colLastActivity: "Letzte Aktivität",
  connection: "Verbindung",
  foldersArchived: "Archivierte Ordner",
  lastSync: "Letzter Sync",
  lastReceived: "Zuletzt empfangen",
  lastImport: "Letzter Import",
  notArchived: (folders) => `Nicht archiviert: ${folders}`,
  imports: "Importe aus IMAP",
};

const dictionaries = { en, de } satisfies Record<Lang, typeof en>;

export type SettingsText = typeof en;

/** The texts in the current UI language (the live lang binding of i18n.ts). */
export function settingsText(): SettingsText {
  return dictionaries[lang];
}

export function settingsTextFor(l: Lang): SettingsText {
  return dictionaries[l];
}
