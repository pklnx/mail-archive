import { ApiError } from "./api";
import { lang, type Lang } from "./i18n";

// The texts of failed and refused IMAP login checks: adding or changing an
// account, an account's folder list, a fill's folders, start and resume.
// The server answers a fixed English sentence and a code; these are the
// texts of the codes. Only the account views import this module, so it is
// part of their lazy chunks and not of the initial bundle.

const minutes = (seconds: number) => Math.ceil(seconds / 60);

const en = {
  imap_connect: "The IMAP server cannot be reached. Check the server name and port.",
  imap_tls: "The secure connection to the IMAP server failed. Check the TLS setting and the server's certificate.",
  imap_login: "The IMAP server refused the login. Check the user name and password.",
  imap_timeout: "The IMAP server did not answer in time. Try again later.",
  imap_server: "The IMAP server refused a command. Try again later.",
  imap_protocol: "The IMAP server sent an answer the archive cannot read.",
  imap_limit: "The IMAP server sent more than the archive accepts, for example too many folders. Ask an admin.",
  tooManyChecks: (seconds: number) =>
    seconds < 60
      ? `Too many server checks. Try again in ${seconds} seconds.`
      : `Too many server checks. Try again in ${minutes(seconds)} ${minutes(seconds) === 1 ? "minute" : "minutes"}.`,
};

const de: typeof en = {
  imap_connect: "Der IMAP-Server ist nicht erreichbar. Prüfe Servername und Port.",
  imap_tls: "Die sichere Verbindung zum IMAP-Server ist fehlgeschlagen. Prüfe die TLS-Einstellung und das Zertifikat des Servers.",
  imap_login: "Der IMAP-Server hat die Anmeldung abgelehnt. Prüfe Benutzername und Passwort.",
  imap_timeout: "Der IMAP-Server hat nicht rechtzeitig geantwortet. Versuche es später erneut.",
  imap_server: "Der IMAP-Server hat einen Befehl abgelehnt. Versuche es später erneut.",
  imap_protocol: "Der IMAP-Server hat eine Antwort geschickt, die das Archiv nicht lesen kann.",
  imap_limit: "Der IMAP-Server hat mehr geschickt, als das Archiv annimmt, zum Beispiel zu viele Ordner. Wende dich an einen Admin.",
  tooManyChecks: (seconds) =>
    seconds < 60
      ? `Zu viele Serverprüfungen. Versuche es in ${seconds} Sekunden erneut.`
      : `Zu viele Serverprüfungen. Versuche es in ${minutes(seconds)} ${minutes(seconds) === 1 ? "Minute" : "Minuten"} erneut.`,
};

const dictionaries = { en, de } satisfies Record<Lang, typeof en>;

/** The codes of a failed check: imap_ and the error's category. */
export const imapCodes = ["imap_connect", "imap_tls", "imap_login", "imap_timeout", "imap_server", "imap_protocol", "imap_limit"] as const;
type ImapCode = (typeof imapCodes)[number];

export function imapTextFor(l: Lang): typeof en {
  return dictionaries[l];
}

/**
 * The text of a failed or refused IMAP login check in the UI's language;
 * null for any other error.
 */
export function imapCheckText(err: unknown): string | null {
  if (!(err instanceof ApiError)) return null;
  const code = err.body.code;
  const texts = dictionaries[lang];
  if (err.status === 429 && code === "too_many_checks") {
    const s = err.body.retryAfter;
    return texts.tooManyChecks(typeof s === "number" && s > 0 ? s : 60);
  }
  return typeof code === "string" && (imapCodes as readonly string[]).includes(code) ? texts[code as ImapCode] : null;
}

/** The text of a failed request: a login check's in the UI's language, otherwise the server's. */
export function errorMessage(err: unknown): string {
  return imapCheckText(err) ?? (err instanceof Error ? err.message : String(err));
}
