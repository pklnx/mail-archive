import type { RevokeReason } from "./auditorApi";
import { day } from "./format";
import { lang, type Lang } from "./i18n";

// The texts of auditor access (#393 part 3): the owner's Auditor access page
// and the auditor's shell (grant choice, banner). Only lazy chunks import
// this module, so it is not part of the initial bundle, as usersText.ts is
// for the Users page.

const en = {
  // The auditor's grant choice at login and the banner.
  chooseTitle: "Choose an audit case",
  chooseText: "You have access to more than one case. A session reads one case at a time; you can switch cases later.",
  chooseAction: "Open",
  caseOf: (label: string, owner: string) => `${label} (${owner})`,
  bannerTitle: "Auditor access",
  bannerText: (label: string, owner: string, until: string) =>
    `Read-only access to “${label}” of ${owner} until ${until}. Every access is recorded in your log and in ${owner}'s.`,
  period: (from: string, until: string) =>
    from && until ? `Messages from ${from} to ${until}.` : from ? `Messages from ${from}.` : until ? `Messages up to ${until}.` : "",
  switchCase: "Audit case",
  auditorTwoFactor: "Auditors keep two-factor authentication on. If you lose your authenticator, an administrator resets it.",

  // The owner's Auditor access page.
  title: "Auditor access",
  intro:
    "Give a tax advisor or tax auditor read-only access to some of your accounts for a limited time. The operator creates their login in your customer group; you grant it access here. Every access shows in your Log.",
  holdHint: "For a tax audit, ask the operator for a legal hold over the period first, so that no deletion run removes mail the audit needs.",
  grant: "Grant access",
  none: "No auditor access granted yet.",
  colCase: "Case",
  colAuditor: "Auditor",
  colAccounts: "Accounts",
  colMessages: "Messages",
  colValid: "Valid",
  colState: "State",
  colLastActivity: "Last access",
  allFolders: "all folders",
  allDates: "All dates",
  validRange: (from: string, until: string) => `${from} – ${until}`,
  state: (s: string) =>
    ({ active: "Active", scheduled: "Not started", expired: "Expired", revoked: "Ended", suspended: "Suspended" })[s] ?? s,
  suspendedHint: "Suspended while you or the auditor are locked.",
  reason: (r: RevokeReason | null): string =>
    r === null
      ? ""
      : {
          owner: "Revoked by you.",
          operator: "Revoked by the operator.",
          group_change: "Ended: you or the auditor moved to another customer group.",
          account_move: "Ended: its accounts moved to another owner.",
          account_purge: "Ended: its accounts were deleted.",
          user_removed: "Ended: the auditor's login was removed.",
        }[r],
  auditorGone: (r: RevokeReason | null): string => (r === "user_removed" ? "(removed)" : "(not in your group)"),
  neverAccessed: "No access yet",
  activityFailed: "Unknown",
  actions: (label: string) => `Actions for ${label}`,
  extend: "Extend",
  revoke: "Revoke",
  revokeTitle: (label: string) => `Revoke “${label}”?`,
  revokeText: (auditor: string) =>
    `${auditor} loses access at once, and open sessions end. This cannot be undone; to give access again, grant it anew.`,
  revoked: (label: string) => `“${label}” is revoked.`,
  created: (label: string, auditor: string) => `${auditor} can now read “${label}”.`,
  extended: (label: string, until: string) => `“${label}” is valid until ${until}.`,

  // The grant dialog.
  auditorName: "Auditor's login name",
  auditorNameHelp: "The exact name the operator gave the auditor's login.",
  auditorRequired: "Enter the auditor's login name.",
  label: "Case",
  labelHelp: "For example “Betriebsprüfung 2021–2023”. You and the auditor see it.",
  labelRequired: "Name the case.",
  accounts: "Accounts",
  accountsRequired: "Choose at least one account.",
  noAccounts: "You have no accounts that can be granted. Journal accounts cannot be granted.",
  allFoldersLater: "All folders, also later ones",
  folders: (account: string) => `Folders of ${account}`,
  foldersRequired: "Choose at least one folder.",
  periodFrom: "Messages from",
  periodUntil: "Messages to",
  periodHelp: "Optional: only messages of these days. Empty is all dates.",
  validFrom: "Access from",
  validUntil: "Access until",
  validHelp: "At most 365 days. The access ends after the last day.",
  validRequired: "Choose the last day of access.",
  stepUp: "Confirm with your password",
  currentPassword: "Current password",
  code: "Two-factor code",
  noAuditor: "There is no auditor with this name in your customer group.",
  noAccount: "One of the accounts is not yours any more.",
  smtpAccount: "Journal accounts cannot be granted.",
  revokedAlready: "The grant was revoked; grant access anew.",
  cannotExtend: "The grant can no longer be extended; grant access anew.",
  tooLong: "Access lasts at most 365 days from its start.",
  extendTitle: (label: string) => `Extend “${label}”`,
  extendText: (until: string) => `Access ends after ${until} now. At most 365 days after today.`,
  newLastDay: "New last day of access",
};

const de: typeof en = {
  chooseTitle: "Prüfungsfall wählen",
  chooseText: "Du hast Zugang zu mehr als einem Fall. Eine Sitzung liest jeweils einen Fall; du kannst später wechseln.",
  chooseAction: "Öffnen",
  caseOf: (label, owner) => `${label} (${owner})`,
  bannerTitle: "Prüferzugang",
  bannerText: (label, owner, until) =>
    `Lesezugriff auf „${label}“ von ${owner} bis ${until}. Jeder Zugriff steht in deinem Protokoll und in dem von ${owner}.`,
  period: (from, until) =>
    from && until ? `Nachrichten vom ${from} bis ${until}.` : from ? `Nachrichten ab ${from}.` : until ? `Nachrichten bis ${until}.` : "",
  switchCase: "Prüfungsfall",
  auditorTwoFactor:
    "Prüfer lassen die Zwei-Faktor-Authentifizierung eingeschaltet. Wenn du deinen Authenticator verlierst, setzt ein Administrator sie zurück.",

  title: "Prüferzugang",
  intro:
    "Gib einer Steuerberatung oder einem Betriebsprüfer für begrenzte Zeit Lesezugriff auf einige deiner Konten. Der Betreiber legt die Anmeldung in deiner Kundengruppe an; hier gibst du ihr Zugriff. Jeder Zugriff steht in deinem Protokoll.",
  holdHint:
    "Bitte vor einer Betriebsprüfung den Betreiber um einen Legal Hold über den Zeitraum, damit kein Löschlauf Mails entfernt, die die Prüfung braucht.",
  grant: "Zugriff geben",
  none: "Noch kein Prüferzugang vergeben.",
  colCase: "Fall",
  colAuditor: "Prüfer",
  colAccounts: "Konten",
  colMessages: "Nachrichten",
  colValid: "Gültig",
  colState: "Status",
  colLastActivity: "Letzter Zugriff",
  allFolders: "alle Ordner",
  allDates: "Alle Daten",
  validRange: (from, until) => `${from} – ${until}`,
  state: (s) => ({ active: "Aktiv", scheduled: "Noch nicht begonnen", expired: "Abgelaufen", revoked: "Beendet", suspended: "Ausgesetzt" })[s] ?? s,
  suspendedHint: "Ausgesetzt, solange du oder der Prüfer gesperrt seid.",
  reason: (r) =>
    r === null
      ? ""
      : {
          owner: "Von dir widerrufen.",
          operator: "Vom Betreiber widerrufen.",
          group_change: "Beendet: du oder der Prüfer seid in eine andere Kundengruppe gewechselt.",
          account_move: "Beendet: die Konten gehören jetzt einem anderen Besitzer.",
          account_purge: "Beendet: die Konten wurden gelöscht.",
          user_removed: "Beendet: die Anmeldung des Prüfers wurde entfernt.",
        }[r],
  auditorGone: (r) => (r === "user_removed" ? "(entfernt)" : "(nicht in deiner Gruppe)"),
  neverAccessed: "Noch kein Zugriff",
  activityFailed: "Unbekannt",
  actions: (label) => `Aktionen für ${label}`,
  extend: "Verlängern",
  revoke: "Widerrufen",
  revokeTitle: (label) => `„${label}“ widerrufen?`,
  revokeText: (auditor) =>
    `${auditor} verliert den Zugriff sofort, offene Sitzungen enden. Das lässt sich nicht rückgängig machen; für neuen Zugriff gibst du ihn neu.`,
  revoked: (label) => `„${label}“ ist widerrufen.`,
  created: (label, auditor) => `${auditor} kann jetzt „${label}“ lesen.`,
  extended: (label, until) => `„${label}“ gilt bis ${until}.`,

  auditorName: "Anmeldename des Prüfers",
  auditorNameHelp: "Der genaue Name, den der Betreiber der Anmeldung des Prüfers gegeben hat.",
  auditorRequired: "Gib den Anmeldenamen des Prüfers ein.",
  label: "Fall",
  labelHelp: "Zum Beispiel „Betriebsprüfung 2021–2023“. Du und der Prüfer seht ihn.",
  labelRequired: "Benenne den Fall.",
  accounts: "Konten",
  accountsRequired: "Wähle mindestens ein Konto.",
  noAccounts: "Du hast keine Konten, für die du Zugriff geben kannst. Journal-Konten lassen sich nicht freigeben.",
  allFoldersLater: "Alle Ordner, auch spätere",
  folders: (account) => `Ordner von ${account}`,
  foldersRequired: "Wähle mindestens einen Ordner.",
  periodFrom: "Nachrichten vom",
  periodUntil: "Nachrichten bis",
  periodHelp: "Optional: nur Nachrichten dieser Tage. Leer heißt alle Daten.",
  validFrom: "Zugriff ab",
  validUntil: "Zugriff bis",
  validHelp: "Höchstens 365 Tage. Der Zugriff endet nach dem letzten Tag.",
  validRequired: "Wähle den letzten Tag des Zugriffs.",
  stepUp: "Mit deinem Passwort bestätigen",
  currentPassword: "Aktuelles Passwort",
  code: "Zwei-Faktor-Code",
  noAuditor: "In deiner Kundengruppe gibt es keinen Prüfer mit diesem Namen.",
  noAccount: "Eines der Konten gehört dir nicht mehr.",
  smtpAccount: "Journal-Konten lassen sich nicht freigeben.",
  revokedAlready: "Der Zugriff wurde widerrufen; gib ihn neu.",
  cannotExtend: "Der Zugriff lässt sich nicht mehr verlängern; gib ihn neu.",
  tooLong: "Der Zugriff dauert höchstens 365 Tage ab seinem Beginn.",
  extendTitle: (label) => `„${label}“ verlängern`,
  extendText: (until) => `Der Zugriff endet jetzt nach dem ${until}. Höchstens 365 Tage ab heute.`,
  newLastDay: "Neuer letzter Tag des Zugriffs",
};

const dictionaries = { en, de } satisfies Record<Lang, typeof en>;

export type AuditorText = typeof en;

/** The texts in the current UI language (the live lang binding of i18n.ts). */
export function auditorText(): AuditorText {
  return dictionaries[lang];
}

export function auditorTextFor(l: Lang): AuditorText {
  return dictionaries[l];
}

/** The last day of a half-open range [.., end): the day of the instant just before end. */
export function lastDay(end: string): string {
  const ms = Date.parse(end);
  return Number.isNaN(ms) ? "" : day(new Date(ms - 1).toISOString());
}

/** The message period of a grant as a sentence; "" without one. */
export function periodText(g: { periodFrom: string | null; periodUntil: string | null }): string {
  return auditorText().period(g.periodFrom ? day(g.periodFrom) : "", g.periodUntil ? lastDay(g.periodUntil) : "");
}
