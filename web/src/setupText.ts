import { lang, type Lang } from "./i18n";

// The texts of the setup of the first admin (#396): the setup link's page
// and the notice without a user. Only the lazy Setup chunk imports this
// module, so they are not part of the initial bundle, as auditorText.ts is
// for auditor access.

const en = {
  title: "Create the first admin",
  cliText: "No user exists yet. Run this command in the mail-archive folder, then check again:",
  checkAgain: "Check again",
  linkWaiting: "Open the setup link from your operator to create the first admin.",
  formText: "Choose the user name and password of this instance's first admin. Next you set up two-factor authentication.",
  create: "Create admin",
  creating: "Creating admin…",
  nameRules: "1 to 64 characters: a-z, 0-9, dot, hyphen, underscore.",
  nameInvalid: "User names have 1 to 64 characters: a-z, 0-9, dot, hyphen, underscore.",
  passwordShort: "The password needs at least 12 characters.",
  repeatPassword: "Repeat password",
  passwordsDiffer: "The passwords do not match.",
  linkInvalid: "This setup link is wrong or has expired. Ask your operator for a new one.",
  linkExpired: "This setup link has expired. Ask your operator for a new one.",
  linkUsed: "This instance is already set up: the setup link works only once. Log in with your user name and password.",
  linkUnknown: "This setup link does not work on this instance. Ask your operator for a new one.",
  toLogin: "Go to login",
};

const de: typeof en = {
  title: "Ersten Admin anlegen",
  cliText: "Es gibt noch keinen Benutzer. Führe diesen Befehl im Ordner von mail-archive aus und prüfe dann erneut:",
  checkAgain: "Erneut prüfen",
  linkWaiting: "Öffne den Einrichtungslink deines Betreibers, um den ersten Admin anzulegen.",
  formText: "Wähle Benutzername und Passwort des ersten Admins dieser Instanz. Danach richtest du die Zwei-Faktor-Authentifizierung ein.",
  create: "Admin anlegen",
  creating: "Admin wird angelegt…",
  nameRules: "1 bis 64 Zeichen: a-z, 0-9, Punkt, Bindestrich, Unterstrich.",
  nameInvalid: "Benutzernamen haben 1 bis 64 Zeichen: a-z, 0-9, Punkt, Bindestrich, Unterstrich.",
  passwordShort: "Das Passwort braucht mindestens 12 Zeichen.",
  repeatPassword: "Passwort wiederholen",
  passwordsDiffer: "Die Passwörter stimmen nicht überein.",
  linkInvalid: "Dieser Einrichtungslink ist falsch oder abgelaufen. Bitte deinen Betreiber um einen neuen.",
  linkExpired: "Dieser Einrichtungslink ist abgelaufen. Bitte deinen Betreiber um einen neuen.",
  linkUsed: "Diese Instanz ist schon eingerichtet: Der Einrichtungslink gilt nur einmal. Melde dich mit Benutzername und Passwort an.",
  linkUnknown: "Dieser Einrichtungslink funktioniert auf dieser Instanz nicht. Bitte deinen Betreiber um einen neuen.",
  toLogin: "Zur Anmeldung",
};

const dictionaries: Record<Lang, typeof en> = { en, de };

export type SetupText = typeof en;

/** The setup texts in the current language. */
export function setupText(): SetupText {
  return dictionaries[lang];
}

export function setupTextFor(l: Lang): SetupText {
  return dictionaries[l];
}
