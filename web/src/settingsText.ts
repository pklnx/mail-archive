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
  // Retention (#285 part 3).
  guideLink: "More in the guide",
  rulesHelp:
    "A rule keeps mail for a number of years, counted from the end of the year it was received or sent in. A rule alone deletes nothing; a deletion run does. Without a rule, mail is kept for ever. The period is a legal question (GoBD).",
  holdsHelp: "A legal hold stops the deletion of the mail it covers, whatever the rules say. Holds are released with a note, never deleted.",
  runsHelp: "A run lists what may be deleted now; nothing is deleted until you confirm. Subjects and senders are shown on the command line only (retention show ID). Files are removed after the grace period.",
  colRule: "Rule",
  colPeriod: "Period",
  colHold: "Hold",
  colCovers: "Covers",
  accountRemoved: "Account removed",
  holdActive: "active",
  released: "Released",
  candidates: "Candidates",
  size: "Size",
  deleted: "Deleted",
  kept: "Kept",
  groups: "Groups",
  deleteCount: (n: number) => (n === 1 ? "Delete 1 message" : `Delete ${n} messages`),
  runStateUnknown: "The run's state could not be loaded, so nothing more is sent from here. Load the page again.",
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
  guideLink: "Mehr im Handbuch",
  rulesHelp:
    "Eine Regel bewahrt Mails eine Anzahl Jahre auf, gezählt ab dem Ende des Jahres, in dem sie empfangen oder gesendet wurden. Eine Regel allein löscht nichts; das tut ein Löschlauf. Ohne Regel bleiben Mails für immer. Die Frist ist eine Rechtsfrage (GoBD).",
  holdsHelp: "Ein Legal Hold verhindert das Löschen der Mails, die er umfasst, egal was die Regeln sagen. Holds werden mit einer Notiz aufgehoben, nie gelöscht.",
  runsHelp: "Ein Lauf listet, was jetzt gelöscht werden darf; gelöscht wird erst nach Ihrer Bestätigung. Betreff und Absender zeigt nur die Kommandozeile (retention show ID). Die Dateien werden nach der Karenzzeit entfernt.",
  colRule: "Regel",
  colPeriod: "Frist",
  colHold: "Hold",
  colCovers: "Umfasst",
  accountRemoved: "Konto entfernt",
  holdActive: "aktiv",
  released: "Aufgehoben",
  candidates: "Kandidaten",
  size: "Größe",
  deleted: "Gelöscht",
  kept: "Behalten",
  groups: "Gruppen",
  deleteCount: (n) => (n === 1 ? "1 Nachricht löschen" : `${n} Nachrichten löschen`),
  runStateUnknown: "Der Stand des Laufs konnte nicht geladen werden; von hier wird nichts mehr gesendet. Laden Sie die Seite neu.",
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
