// UI texts in English and German. The language follows the browser: German
// if the first supported preference is German, English otherwise.

export type Lang = "en" | "de";

const en = {
  showMenu: "Show accounts and folders",
  searchMail: "Search mail",
  searchAll: "Search all mail",
  searchIn: (where: string) => `Search in ${where}`,
  back: "← Back",
  selectMessage: "Select a message to read it.",
  switchTheme: (next: "light" | "dark"): string => (next === "dark" ? "Switch to dark mode" : "Switch to light mode"),

  accountsAndFolders: "Accounts and folders",
  allMail: "All mail",
  loading: "Loading…",
  loadAccountsFailed: (err: string) => `Could not load accounts: ${err}`,
  noAccounts: "No accounts yet.",
  disabled: "disabled",

  messages: "Messages",
  noSubject: "(no subject)",
  unknownSender: "(unknown sender)",
  loadMessagesFailed: (err: string) => `Could not load messages: ${err}`,
  retry: "Retry",
  noMatches: "No messages match your search.",
  noMessages: "No messages.",

  loadMessageFailed: (err: string) => `Could not load message: ${err}`,
  from: "From",
  to: "To",
  cc: "Cc",
  date: "Date",
  foundIn: "Found in",
  showText: "Show text",
  showHTML: "Show HTML",
  remoteImagesHint: "Remote images can tell the sender that you opened this message.",
  loadRemoteImages: "Load remote images",
  downloadEml: "Download .eml",
  attachments: "Attachments",
  part: (n: number) => `part ${n}`,
  truncated: "This message is very large; the preview is shortened.",
  messageContent: "Message content",

  manageAccounts: "Manage accounts",
  removed: "removed",
  syncing: "syncing",
  backToMail: "← Mail",
  accountsTitle: "Accounts",
  addAccount: "Add account",
  syncAll: "Sync all",
  syncNow: "Sync",
  edit: "Edit",
  enable: "Enable",
  disable: "Disable",
  remove: "Remove",
  schedule: (every: string) => `Enabled accounts are synced automatically every ${every}.`,
  scheduleOff: "Automatic sync is off; sync by hand.",
  manageOff: "Accounts cannot be changed here: the server has no MAIL_ARCHIVE_SECRET_KEY.",
  stateQueued: "Waiting to sync…",
  stateRunning: (fetched: number, added: number) => `Syncing: ${fetched} fetched, ${added} new`,
  lastOk: (when: string, added: number) => `Last sync ${when}: ${added} new`,
  lastPartial: (when: string) => `Last sync ${when} had errors`,
  lastFailed: (when: string) => `Last sync ${when} failed`,
  neverSynced: "Not synced yet",
  messageCount: (n: number, formatted: string) => (n === 1 ? "1 message" : `${formatted} messages`),
  disabledNote: "Disabled: not synced automatically.",
  removedNote: "Removed. Its mail stays in the archive.",
  removedAccounts: "Removed accounts",
  fieldName: "Name",
  nameHint: "Shown in the archive and used in commands like ./ma sync --account NAME.",
  fieldHost: "IMAP server",
  fieldPort: "Port",
  fieldTLS: "Connection security",
  tlsTLS: "TLS",
  tlsSTARTTLS: "STARTTLS",
  tlsNone: "None (insecure)",
  fieldUsername: "Username",
  fieldPassword: "Password",
  passwordKeep: "Leave empty to keep the current one",
  passwordHint: "Gmail and iCloud need an app password.",
  save: "Save",
  cancel: "Cancel",
  checkingLogin: "Checking login…",
  folders: "Folders",
  loadFolders: "Choose folders",
  foldersHint: "Checked folders are archived.",
  saveFolders: "Save folder selection",
  saved: "Saved.",
  confirmRemove: (name: string) =>
    `Remove "${name}"?\n\nIts archived mail stays searchable. The stored password is deleted and the account is no longer synced. This cannot be undone.`,
  failed: (err: string) => `Failed: ${err}`,
  trashSpamQuestion: (folders: string) =>
    `The server marks these folders as trash or spam: ${folders}. Archive them too? Mail archived from them stays in the archive even if you exclude them later.`,
  saveWithout: "Save without them",
  saveWithAll: "Save with all folders",
  appName: "Mail Archive",
  logIn: "Log in",
  loggingIn: "Logging in…",
  userName: "User name",
  wrongLogin: "Wrong user name or password.",
  userLocked: "This user is locked. Ask an admin to unlock it.",
  tooManyAttempts: (seconds: number) =>
    seconds < 60
      ? `Too many failed attempts. Try again in ${seconds} seconds.`
      : `Too many failed attempts. Try again in ${Math.ceil(seconds / 60)} ${Math.ceil(seconds / 60) === 1 ? "minute" : "minutes"}.`,
  setupTitle: "Create the first admin",
  setupText: "No user exists yet. Run this command in the mail-archive folder, then check again:",
  checkAgain: "Check again",
  logOut: "Log out",
  loggedInAs: (name: string) => `Logged in as ${name}`,
  role: (r: string): string =>
    ({ trash: "Trash", junk: "Spam", drafts: "Drafts", sent: "Sent", archive: "Archive", all: "All mail", flagged: "Flagged" })[r] ?? r,
};

export type Messages = typeof en;

const de: Messages = {
  showMenu: "Konten und Ordner anzeigen",
  searchMail: "E-Mails durchsuchen",
  searchAll: "Alle E-Mails durchsuchen",
  searchIn: (where) => `In ${where} suchen`,
  back: "← Zurück",
  selectMessage: "Wähle eine Nachricht aus, um sie zu lesen.",
  switchTheme: (next) => (next === "dark" ? "Zum dunklen Modus wechseln" : "Zum hellen Modus wechseln"),

  accountsAndFolders: "Konten und Ordner",
  allMail: "Alle E-Mails",
  loading: "Wird geladen…",
  loadAccountsFailed: (err) => `Konten konnten nicht geladen werden: ${err}`,
  noAccounts: "Noch keine Konten.",
  disabled: "deaktiviert",

  messages: "Nachrichten",
  noSubject: "(kein Betreff)",
  unknownSender: "(unbekannter Absender)",
  loadMessagesFailed: (err) => `Nachrichten konnten nicht geladen werden: ${err}`,
  retry: "Erneut versuchen",
  noMatches: "Keine Nachrichten passen zur Suche.",
  noMessages: "Keine Nachrichten.",

  loadMessageFailed: (err) => `Nachricht konnte nicht geladen werden: ${err}`,
  from: "Von",
  to: "An",
  cc: "Kopie",
  date: "Datum",
  foundIn: "Gefunden in",
  showText: "Text anzeigen",
  showHTML: "HTML anzeigen",
  remoteImagesHint: "Externe Bilder können dem Absender verraten, dass du die Nachricht geöffnet hast.",
  loadRemoteImages: "Externe Bilder laden",
  downloadEml: ".eml herunterladen",
  attachments: "Anhänge",
  part: (n) => `Teil ${n}`,
  truncated: "Diese Nachricht ist sehr groß, die Vorschau ist gekürzt.",
  messageContent: "Nachrichteninhalt",

  manageAccounts: "Konten verwalten",
  removed: "entfernt",
  syncing: "synchronisiert",
  backToMail: "← E-Mails",
  accountsTitle: "Konten",
  addAccount: "Konto hinzufügen",
  syncAll: "Alle synchronisieren",
  syncNow: "Synchronisieren",
  edit: "Bearbeiten",
  enable: "Aktivieren",
  disable: "Deaktivieren",
  remove: "Entfernen",
  schedule: (every) => `Aktive Konten werden automatisch alle ${every} synchronisiert.`,
  scheduleOff: "Der automatische Sync ist aus, synchronisiere von Hand.",
  manageOff: "Konten können hier nicht geändert werden: Dem Server fehlt MAIL_ARCHIVE_SECRET_KEY.",
  stateQueued: "Wartet auf den Sync…",
  stateRunning: (fetched, added) => `Sync läuft: ${fetched} abgerufen, ${added} neu`,
  lastOk: (when, added) => `Letzter Sync ${when}: ${added} neu`,
  lastPartial: (when) => `Letzter Sync ${when} mit Fehlern`,
  lastFailed: (when) => `Letzter Sync ${when} fehlgeschlagen`,
  neverSynced: "Noch nicht synchronisiert",
  messageCount: (n, formatted) => (n === 1 ? "1 Nachricht" : `${formatted} Nachrichten`),
  disabledNote: "Deaktiviert: wird nicht automatisch synchronisiert.",
  removedNote: "Entfernt. Die Mails bleiben im Archiv.",
  removedAccounts: "Entfernte Konten",
  fieldName: "Name",
  nameHint: "Wird im Archiv angezeigt und in Befehlen wie ./ma sync --account NAME verwendet.",
  fieldHost: "IMAP-Server",
  fieldPort: "Port",
  fieldTLS: "Verbindungssicherheit",
  tlsTLS: "TLS",
  tlsSTARTTLS: "STARTTLS",
  tlsNone: "Keine (unsicher)",
  fieldUsername: "Benutzername",
  fieldPassword: "Passwort",
  passwordKeep: "Leer lassen, um das bisherige zu behalten",
  passwordHint: "Gmail und iCloud brauchen ein App-Passwort.",
  save: "Speichern",
  cancel: "Abbrechen",
  checkingLogin: "Anmeldung wird geprüft…",
  folders: "Ordner",
  loadFolders: "Ordner auswählen",
  foldersHint: "Angehakte Ordner werden archiviert.",
  saveFolders: "Ordnerauswahl speichern",
  saved: "Gespeichert.",
  confirmRemove: (name) =>
    `„${name}“ entfernen?\n\nDie archivierten Mails bleiben durchsuchbar. Das gespeicherte Passwort wird gelöscht und das Konto nicht mehr synchronisiert. Das lässt sich nicht rückgängig machen.`,
  failed: (err) => `Fehlgeschlagen: ${err}`,
  trashSpamQuestion: (folders) =>
    `Der Server kennzeichnet diese Ordner als Papierkorb oder Spam: ${folders}. Sollen sie mitarchiviert werden? Was einmal archiviert ist, bleibt im Archiv, auch wenn du die Ordner später abwählst.`,
  saveWithout: "Ohne diese speichern",
  saveWithAll: "Mit allen Ordnern speichern",
  appName: "Mail Archive",
  logIn: "Anmelden",
  loggingIn: "Anmeldung läuft…",
  userName: "Benutzername",
  wrongLogin: "Benutzername oder Passwort falsch.",
  userLocked: "Dieser Benutzer ist gesperrt. Bitte einen Admin, ihn zu entsperren.",
  tooManyAttempts: (seconds) =>
    seconds < 60
      ? `Zu viele Fehlversuche. Versuche es in ${seconds} Sekunden erneut.`
      : `Zu viele Fehlversuche. Versuche es in ${Math.ceil(seconds / 60)} ${Math.ceil(seconds / 60) === 1 ? "Minute" : "Minuten"} erneut.`,
  setupTitle: "Ersten Admin anlegen",
  setupText: "Es gibt noch keinen Benutzer. Führe diesen Befehl im Ordner von mail-archive aus und prüfe dann erneut:",
  checkAgain: "Erneut prüfen",
  logOut: "Abmelden",
  loggedInAs: (name) => `Angemeldet als ${name}`,
  role: (r) =>
    ({ trash: "Papierkorb", junk: "Spam", drafts: "Entwürfe", sent: "Gesendet", archive: "Archiv", all: "Alle Nachrichten", flagged: "Markiert" })[r] ?? r,
};

const dictionaries: Record<Lang, Messages> = { en, de };

/**
 * The first supported language in the browser's preferences, with the full
 * tag as locale for dates and numbers (en-GB keeps day-month order).
 */
export function detectLocale(prefs: readonly string[] = navigator.languages ?? [navigator.language]): { lang: Lang; locale: string } {
  for (const p of prefs) {
    const base = p.toLowerCase().split("-")[0];
    if (base !== "de" && base !== "en") continue;
    try {
      return { lang: base, locale: Intl.getCanonicalLocales(p)[0] ?? base };
    } catch {
      return { lang: base, locale: base };
    }
  }
  return { lang: "en", locale: "en" };
}

export const { lang, locale } = detectLocale();

/** The UI texts in the browser's language. */
export const t: Messages = dictionaries[lang];

export function messagesFor(l: Lang): Messages {
  return dictionaries[l];
}
