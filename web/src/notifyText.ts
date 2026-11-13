import { lang, type Lang } from "./i18n";

// The texts of Profile → Notifications (#397, part 5). Only the Profile
// chunk imports this module, so they are not part of the initial bundle;
// the banner's line stays in i18n.ts.

const en = {
  tab: "Notifications",
  title: "Alert mail",
  intro:
    "Alert mail tells you when syncs or recurring imports keep failing or work again, when mail was deleted on an IMAP server, and when the storage of the instance or of a customer group reaches a limit. It names accounts, owners and groups, never error texts, folders or mail content. Only admins with a confirmed address get it.",
  email: "Address for alert mail",
  emailHelp: "A code is mailed to it; the address gets alert mail once you enter the code.",
  sendCode: "Send code",
  resend: "Send a new code",
  code: "Code from the mail",
  confirm: "Confirm address",
  remove: "Remove address",
  stateConfirmed: "Confirmed",
  stateUnconfirmed: "Not confirmed",
  confirmed: (email: string) => `Alert mail goes to ${email}.`,
  codeSent: (email: string, until: string) => `Enter the code mailed to ${email} by ${until}.`,
  codeSentNow: (email: string) => `A code was mailed to ${email}.`,
  confirmedNow: "Address confirmed. Alert mail goes to it from now on.",
  removed: "Address removed. You get no alert mail.",
  bounced: "The mail server refused this address. It gets no alert mail until you confirm it again.",
  wrongCode: "Wrong code. Check the mail and try again.",
  noCode: "This code expired or was entered wrong too often. Send a new code.",
  mailFailed: "The code could not be sent to this address. Check it, or ask the operator to check the mail relay.",
  invalidEmail: "Enter one mail address, such as admin@example.com.",
  tooManyCodes: (seconds: number) =>
    `Three codes were sent in the last hour. Try again in ${Math.ceil(seconds / 60)} ${Math.ceil(seconds / 60) === 1 ? "minute" : "minutes"}.`,
};

type NotifyText = typeof en;

const de: NotifyText = {
  tab: "Benachrichtigungen",
  title: "Alarm-Mails",
  intro:
    "Alarm-Mails melden, wenn Synchronisierungen oder laufende Importe wiederholt fehlschlagen oder wieder laufen, wenn Mail auf einem IMAP-Server gelöscht wurde und wenn der Speicher der Instanz oder einer Kundengruppe eine Grenze erreicht. Sie nennen Konten, Besitzer und Gruppen, nie Fehlermeldungen, Ordner oder Mailinhalte. Nur Admins mit bestätigter Adresse erhalten sie.",
  email: "Adresse für Alarm-Mails",
  emailHelp: "Ein Code wird an sie gesendet; sobald du ihn eingibst, erhält die Adresse Alarm-Mails.",
  sendCode: "Code senden",
  resend: "Neuen Code senden",
  code: "Code aus der Mail",
  confirm: "Adresse bestätigen",
  remove: "Adresse entfernen",
  stateConfirmed: "Bestätigt",
  stateUnconfirmed: "Nicht bestätigt",
  confirmed: (email) => `Alarm-Mails gehen an ${email}.`,
  codeSent: (email, until) => `Gib den an ${email} gesendeten Code bis ${until} ein.`,
  codeSentNow: (email) => `Ein Code wurde an ${email} gesendet.`,
  confirmedNow: "Adresse bestätigt. Alarm-Mails gehen ab jetzt an sie.",
  removed: "Adresse entfernt. Du erhältst keine Alarm-Mails.",
  bounced: "Der Mailserver hat diese Adresse abgelehnt. Sie erhält keine Alarm-Mails, bis du sie erneut bestätigst.",
  wrongCode: "Falscher Code. Prüfe die Mail und versuch es noch einmal.",
  noCode: "Dieser Code ist abgelaufen oder wurde zu oft falsch eingegeben. Sende einen neuen Code.",
  mailFailed: "Der Code konnte nicht an diese Adresse gesendet werden. Prüfe sie oder bitte den Betreiber, das Mail-Relay zu prüfen.",
  invalidEmail: "Gib eine Mailadresse ein, etwa admin@example.com.",
  tooManyCodes: (seconds) =>
    `In der letzten Stunde wurden drei Codes gesendet. Versuch es in ${Math.ceil(seconds / 60)} ${Math.ceil(seconds / 60) === 1 ? "Minute" : "Minuten"} erneut.`,
};

const dictionaries: Record<Lang, NotifyText> = { en, de };

export function notifyText(l: Lang = lang): NotifyText {
  return dictionaries[l];
}
