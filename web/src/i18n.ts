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
  noAccounts: "No accounts yet. Add one with ./ma account add.",
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
  noAccounts: "Noch keine Konten. Lege eines mit ./ma account add an.",
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
