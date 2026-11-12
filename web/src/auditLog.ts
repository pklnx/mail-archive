import type { AuditEntry } from "./auditApi";
import { lang, type Lang } from "./i18n";

// The names of the audit actions in every language. They live here, in the
// lazy Log chunk, and not in i18n.ts, which the mail view loads (#288
// bundle budget).
const actionsEn = {
  "auth.login": "Signed in",
  "auth.login_failed": "Sign-in failed",
  "auth.logout": "Signed out",
  "message.search": "Searched",
  "message.open": "Opened message",
  "message.html": "Showed HTML",
  "message.raw": "Downloaded message",
  "message.part": "Downloaded attachment",
  "message.list": "Listed messages",
  "message.page": "Loaded more results",
  "message.conversation": "Showed conversation",
  "sync.start": "Started sync",
  "account.add": "Added account",
  "account.change": "Changed account",
  "account.remove": "Removed account",
  "account.move": "Moved account",
  "account.share": "Shared account",
  "account.unshare": "Ended account share",
  "account.purge": "Account deletion confirmed",
  "account.purge_batch": "Deleted messages (account deletion)",
  "account.purge_finish": "Finished deleting account",
  "account.purge_cancel": "Cancelled account deletion",
  "user.add": "Added user",
  "user.change": "Changed user",
  "user.remove": "Removed user",
  "user.password_reset": "Reset password",
  "user.password_set": "Set password",
  "user.2fa_reset": "Reset 2FA",
  "user.passkeys_removed": "Removed passkeys",
  "user.group_change": "Changed user's group",
  "group.add": "Added group",
  "group.rename": "Renamed group",
  "group.remove": "Removed group",
  "profile.password_change": "Changed own password",
  "profile.2fa_enable": "Turned on 2FA",
  "profile.2fa_disable": "Turned off 2FA",
  "profile.recovery_codes": "New recovery codes",
  "profile.passkey_add": "Added passkey",
  "profile.passkey_remove": "Removed passkey",
  "profile.change": "Changed settings",
  "api_key.create": "Created API key",
  "api_key.revoke": "Revoked API key",
  "smtp_credential.create": "Created SMTP secret",
  "smtp_credential.revoke": "Revoked SMTP secret",
  "retention.rule_add": "Added retention rule",
  "retention.rule_remove": "Removed retention rule",
  "hold.add": "Added legal hold",
  "hold.release": "Released legal hold",
  "retention.preview": "Proposed deletion run",
  "retention.confirm": "Confirmed deletion run",
  "retention.cancel": "Cancelled deletion run",
  "retention.batch": "Deleted messages (retention)",
  "retention.finish": "Finished deletion run",
  "retention.sweep": "Removed files of deleted messages",
  "jobs.retry": "Retried failed jobs",
  "data.import": "Imported mail",
  "data.export": "Exported mail",
  "data.export_done": "Finished export",
  "data.fill": "Started import from IMAP",
  "data.fill_resume": "Resumed import from IMAP",
  "data.fill_change": "Changed import from IMAP",
  "data.fill_cancel": "Stopped or cancelled import from IMAP",
  "data.fill_done": "Finished import from IMAP",
  "data.backup": "Backup",
  "audit.export": "Exported log",
  "auditor.grant": "Granted auditor access",
  "auditor.extend": "Extended auditor access",
  "auditor.revoke": "Ended auditor access",
  "auditor.grant_change": "Changed auditor access",
  "auditor.select": "Chose audit case",
  "auditor.denied": "Refused auditor request",
  "account.list": "Listed accounts",
};
const actionsDe: Record<keyof typeof actionsEn, string> = {
  "auth.login": "Angemeldet",
  "auth.login_failed": "Anmeldung fehlgeschlagen",
  "auth.logout": "Abgemeldet",
  "message.search": "Gesucht",
  "message.open": "Nachricht geöffnet",
  "message.html": "HTML angezeigt",
  "message.raw": "Nachricht heruntergeladen",
  "message.part": "Anhang heruntergeladen",
  "message.list": "Nachrichten aufgelistet",
  "message.page": "Weitere Ergebnisse geladen",
  "message.conversation": "Konversation angezeigt",
  "sync.start": "Synchronisierung gestartet",
  "account.add": "Konto hinzugefügt",
  "account.change": "Konto geändert",
  "account.remove": "Konto entfernt",
  "account.move": "Konto übertragen",
  "account.share": "Konto geteilt",
  "account.unshare": "Freigabe beendet",
  "account.purge": "Löschen des Kontos bestätigt",
  "account.purge_batch": "Nachrichten gelöscht (Kontolöschung)",
  "account.purge_finish": "Kontolöschung beendet",
  "account.purge_cancel": "Kontolöschung abgebrochen",
  "user.add": "Benutzer hinzugefügt",
  "user.change": "Benutzer geändert",
  "user.remove": "Benutzer entfernt",
  "user.password_reset": "Passwort zurückgesetzt",
  "user.password_set": "Passwort gesetzt",
  "user.2fa_reset": "2FA zurückgesetzt",
  "user.passkeys_removed": "Passkeys entfernt",
  "user.group_change": "Gruppe des Benutzers geändert",
  "group.add": "Gruppe hinzugefügt",
  "group.rename": "Gruppe umbenannt",
  "group.remove": "Gruppe entfernt",
  "profile.password_change": "Eigenes Passwort geändert",
  "profile.2fa_enable": "2FA eingeschaltet",
  "profile.2fa_disable": "2FA ausgeschaltet",
  "profile.recovery_codes": "Neue Wiederherstellungscodes",
  "profile.passkey_add": "Passkey hinzugefügt",
  "profile.passkey_remove": "Passkey entfernt",
  "profile.change": "Einstellungen geändert",
  "api_key.create": "API-Schlüssel erstellt",
  "api_key.revoke": "API-Schlüssel widerrufen",
  "smtp_credential.create": "SMTP-Geheimnis erstellt",
  "smtp_credential.revoke": "SMTP-Geheimnis widerrufen",
  "retention.rule_add": "Aufbewahrungsregel angelegt",
  "retention.rule_remove": "Aufbewahrungsregel entfernt",
  "hold.add": "Legal Hold angelegt",
  "hold.release": "Legal Hold aufgehoben",
  "retention.preview": "Löschlauf vorgeschlagen",
  "retention.confirm": "Löschlauf bestätigt",
  "retention.cancel": "Löschlauf abgebrochen",
  "retention.batch": "Nachrichten gelöscht (Aufbewahrung)",
  "retention.finish": "Löschlauf beendet",
  "retention.sweep": "Dateien gelöschter Nachrichten entfernt",
  "jobs.retry": "Fehlgeschlagene Aufgaben neu gestartet",
  "data.import": "Mails importiert",
  "data.export": "Mails exportiert",
  "data.export_done": "Export beendet",
  "data.fill": "Import aus IMAP gestartet",
  "data.fill_resume": "Import aus IMAP fortgesetzt",
  "data.fill_change": "Import aus IMAP geändert",
  "data.fill_cancel": "Import aus IMAP gestoppt oder abgebrochen",
  "data.fill_done": "Import aus IMAP beendet",
  "data.backup": "Backup",
  "audit.export": "Protokoll exportiert",
  "auditor.grant": "Prüferzugang gegeben",
  "auditor.extend": "Prüferzugang verlängert",
  "auditor.revoke": "Prüferzugang beendet",
  "auditor.grant_change": "Prüferzugang geändert",
  "auditor.select": "Prüfungsfall gewählt",
  "auditor.denied": "Anfrage des Prüfers abgelehnt",
  "account.list": "Konten aufgelistet",
};
const actionNames: Record<Lang, Record<string, string>> = { en: actionsEn, de: actionsDe };

// The words of auditor entries (#393): the name an owner sees for anyone
// but their auditor, and the reasons of refusals and of ended grants.
const auditorEn = {
  operator: "Operator",
  grant: (id: number) => `case ${id}`,
  reasons: {
    not_allowed: "not available to auditors",
    password_change_required: "before the password change",
    two_factor_required: "before the 2FA setup",
    grant_selection_required: "before a case was chosen",
    admin_only: "admins only",
    owner: "by the owner",
    operator: "by the operator",
    group_change: "group change",
    account_move: "account moved to another owner",
    account_purge: "account deleted",
    user_removed: "auditor removed",
  } as Record<string, string>,
};
const auditorDe: typeof auditorEn = {
  operator: "Betreiber",
  grant: (id) => `Fall ${id}`,
  reasons: {
    not_allowed: "für Prüfer nicht verfügbar",
    password_change_required: "vor der Passwortänderung",
    two_factor_required: "vor der Einrichtung der 2FA",
    grant_selection_required: "bevor ein Fall gewählt war",
    admin_only: "nur für Admins",
    owner: "durch den Besitzer",
    operator: "durch den Betreiber",
    group_change: "Gruppenwechsel",
    account_move: "Konto an anderen Besitzer übertragen",
    account_purge: "Konto gelöscht",
    user_removed: "Prüfer entfernt",
  },
};
const auditorWords: Record<Lang, typeof auditorEn> = { en: auditorEn, de: auditorDe };

/** The name in an entry's Who column: "operator" of an owner's auditor entries in words (#393). */
export function entryUser(e: AuditEntry, l: Lang = lang): string {
  return e.viaAuditor && e.user === "operator" ? auditorWords[l].operator : (e.user ?? "");
}

/**
 * The detail of an auditor's grant change, choice or refusal (#393): the
 * case's label or number, the refused method and route, and the reason, in
 * words.
 */
export function auditorDetail(e: AuditEntry, l: Lang = lang): string {
  const w = auditorWords[l];
  const d = e.details;
  const parts: string[] = [];
  if (typeof d.label === "string") parts.push(`“${d.label}”`);
  else if (typeof d.auditor_grant === "number") parts.push(w.grant(d.auditor_grant));
  if (typeof d.method === "string" && typeof d.route === "string") parts.push(`${d.method} ${d.route}`);
  if (typeof d.reason === "string") parts.push(w.reasons[d.reason] ?? d.reason);
  return parts.join(", ");
}

/** An audit action in words, in the current language or l; an unknown action as it is. */
export function auditAction(a: string, l: Lang = lang): string {
  return actionNames[l][a] ?? a;
}

/** A row of the Log page: an entry and the entries folded into it. */
export interface LogRow {
  entry: AuditEntry;
  folded: AuditEntry[];
}

/** Searches of one user this close together, each a prefix of the next, are one search typed. */
const typingWindow = 10_000;
/** Inline images load right after the HTML body that shows them. */
const inlineWindow = 60_000;

const who = (e: AuditEntry) => `${e.actor}:${e.user ?? ""}:${e.apiKey ?? ""}`;
const query = (e: AuditEntry) => (typeof e.details.q === "string" ? e.details.q : "");
const time = (e: AuditEntry) => new Date(e.at).getTime();

/**
 * Folds the rows a person does not read one by one, from entries newest
 * first: the inline images of a message into its message.html row, and the
 * prefixes a search box sends while someone types ("inv", "invoice") into
 * the last search. The log itself stays complete; this only groups rows.
 */
export function foldEntries(entries: AuditEntry[]): LogRow[] {
  const rows: LogRow[] = [];
  const lastOf = new Map<string, LogRow>(); // the newest row so far per user
  const htmlOf = new Map<string, LogRow>(); // the newest message.html row per user and message
  for (const e of [...entries].reverse()) {
    const key = who(e);
    const prev = lastOf.get(key);
    if (e.action === "message.part" && e.details.inline === true && e.target) {
      const html = htmlOf.get(`${key}|${e.target.id}`);
      if (html && time(e) - time(html.entry) <= inlineWindow) {
        html.folded.push(e);
        continue;
      }
    }
    if (
      e.action === "message.search" &&
      prev?.entry.action === "message.search" &&
      time(e) - time(prev.entry) <= typingWindow &&
      query(e).startsWith(query(prev.entry)) &&
      query(prev.entry) !== ""
    ) {
      const row: LogRow = { entry: e, folded: [...prev.folded, prev.entry] };
      rows[rows.indexOf(prev)] = row;
      lastOf.set(key, row);
      continue;
    }
    const row: LogRow = { entry: e, folded: [] };
    rows.push(row);
    lastOf.set(key, row);
    if (e.action === "message.html" && e.target) htmlOf.set(`${key}|${e.target.id}`, row);
  }
  return rows.reverse();
}

/** A short form of a user agent: the browser and its platform, like "Firefox 141 · Windows". */
export function shortUserAgent(ua: string): string {
  let browser = "";
  let m: RegExpExecArray | null;
  if ((m = /Edg\/(\d+)/.exec(ua))) browser = `Edge ${m[1]}`;
  else if ((m = /Firefox\/(\d+)/.exec(ua))) browser = `Firefox ${m[1]}`;
  else if ((m = /Chrome\/(\d+)/.exec(ua))) browser = `Chrome ${m[1]}`;
  else if ((m = /Version\/(\d+).*Safari/.exec(ua))) browser = `Safari ${m[1]}`;
  // In this order: Android user agents also say Linux, iPhones "like Mac OS X".
  const platform = ["iPhone", "iPad", "Android", "Windows", "Mac OS X", "Linux"].find((p) => ua.includes(p))?.replace("Mac OS X", "macOS") ?? "";
  const parts = [browser, platform].filter(Boolean);
  return parts.length > 0 ? parts.join(" · ") : ua.slice(0, 40);
}
