import { lang, type Lang } from "./i18n";

// The texts of SMTP secrets (#395 part 3) and of the mail server setup:
// the section on an smtp account's page, the admins' list on the Users
// page and the hint after a lock. Only the lazy Accounts and Users chunks
// import this module, so none of it is in the initial bundle, as
// usersText.ts and settingsText.ts are not.

const secrets = (n: number) => (n === 1 ? "1 SMTP secret" : `${n} SMTP secrets`);
const geheimnisse = (n: number) => (n === 1 ? "1 SMTP-Secret" : `${n} SMTP-Secrets`);

const en = {
  // Mail server setup.
  setupIntro: "Postfix, recipient_bcc_maps (the guide's variant B):",
  setupIntroLogin: "Postfix with a login over TLS, recipient_bcc_maps (the guide's variant B):",
  setupTarget:
    "[127.0.0.1]:2525 is the guide's Docker Compose default (SMTP_BIND, SMTP_PORT): put the address and port at which Postfix reaches the archive.",
  setupTargetLogin:
    "Put a secret of this account (SMTP secrets on its page) in place of mas_…, and the name and port of the archive's SMTP server in place of [archive.customer.example]:587. Postfix needs libsasl2-modules. Several smtp accounts on one mail server need a transport and a password map each:",
  setupNotDedicated: (address: string) =>
    `No address of this account is in a domain used only for the archive. The transport line routes a whole domain to the archive, so these lines use ${address}: add it to the account, or another address in such a domain.`,
  // The section on the account's page.
  title: "SMTP secrets",
  intro: "The mail server logs in with the user name and a secret to hand over this account's copies. A secret is shown only once, when it is created.",
  authOff: "The SMTP server runs without logins (MAIL_ARCHIVE_SMTP_AUTH=off): mail servers do not need a secret now.",
  username: "User name",
  none: "No secret yet: a mail server cannot log in for this account.",
  colSecret: "Secret",
  colCreated: "Created",
  colValidUntil: "Valid until",
  noEnd: "until replaced",
  state: (s: string): string => ({ active: "active", expiring: "expiring", expired: "expired", revoked: "revoked" })[s] ?? s,
  newSecret: "New secret",
  newSecretTitle: (account: string) => `New SMTP secret for “${account}”`,
  grace: "The current secret stays valid",
  graceDays: (n: number) => (n === 0 ? "No, revoke it at once" : n === 1 ? "For 1 day" : `For ${n} days`),
  graceHelp:
    "Time to put the new secret into the mail server. At most two secrets work at a time: a new one revokes a secret that is already expiring.",
  create: "Create secret",
  created: "Copy the secret now: it is shown only once. Closing this dialog removes it from the page.",
  secret: "Secret",
  mapLine: "Line for /etc/postfix/archive_sasl",
  previousExpires: (when: string) => `The previous secret works until ${when}.`,
  previousRevoked: "The previous secret is revoked.",
  revoke: "Revoke",
  revokeLabel: (hint: string) => `Revoke mas_…${hint}`,
  revokeTitle: (hint: string) => `Revoke the SMTP secret mas_…${hint}?`,
  revokeText:
    "A mail server that logs in with it can no longer hand over mail. Its copies wait in its queue until it logs in with another secret.",
  revoked: (hint: string) => `The SMTP secret mas_…${hint} is revoked.`,
  // The admins' list on the Users page.
  adminIntro:
    "The secrets mail servers log in with, of every smtp account. Locking a user does not stop them; revoke them here. New secrets are created by the account's owner on the account's page.",
  colAccount: "Account",
  colOwner: "Owner",
  noOwner: "none",
  accountRemoved: "removed",
  lockHint: (user: string, n: number) =>
    `${secrets(n)} of smtp accounts that ${user} owns still work: a lock does not stop mail servers from handing over journal copies. Revoke them if they may be misused.`,
  lockHintLink: "Go to SMTP secrets",
};

const de: typeof en = {
  setupIntro: "Postfix, recipient_bcc_maps (Variante B der Anleitung):",
  setupIntroLogin: "Postfix mit Anmeldung über TLS, recipient_bcc_maps (Variante B der Anleitung):",
  setupTarget:
    "[127.0.0.1]:2525 ist der Docker-Compose-Standard der Anleitung (SMTP_BIND, SMTP_PORT): Trage Adresse und Port ein, unter denen Postfix das Archiv erreicht.",
  setupTargetLogin:
    "Setze statt mas_… ein Secret dieses Kontos ein (SMTP-Secrets auf seiner Seite) und statt [archive.customer.example]:587 Name und Port des SMTP-Servers des Archivs. Postfix braucht libsasl2-modules. Mehrere Journaling-Konten auf einem Mailserver brauchen je einen eigenen Transport und eine eigene Passwort-Tabelle:",
  setupNotDedicated: (address) =>
    `Keine Adresse dieses Kontos liegt in einer Domain, die nur das Archiv empfängt. Die Transport-Zeile leitet eine ganze Domain zum Archiv, deshalb verwenden diese Zeilen ${address}: Füge sie dem Konto hinzu oder eine andere Adresse in einer solchen Domain.`,
  title: "SMTP-Secrets",
  intro:
    "Der Mailserver meldet sich mit dem Benutzernamen und einem Secret an, um die Kopien dieses Kontos abzugeben. Ein Secret wird nur einmal angezeigt, beim Erstellen.",
  authOff: "Der SMTP-Server läuft ohne Anmeldung (MAIL_ARCHIVE_SMTP_AUTH=off): Mailserver brauchen zurzeit kein Secret.",
  username: "Benutzername",
  none: "Noch kein Secret: Kein Mailserver kann sich für dieses Konto anmelden.",
  colSecret: "Secret",
  colCreated: "Erstellt",
  colValidUntil: "Gültig bis",
  noEnd: "bis es ersetzt wird",
  state: (s) => ({ active: "aktiv", expiring: "läuft ab", expired: "abgelaufen", revoked: "widerrufen" })[s] ?? s,
  newSecret: "Neues Secret",
  newSecretTitle: (account) => `Neues SMTP-Secret für „${account}“`,
  grace: "Das aktuelle Secret bleibt gültig",
  graceDays: (n) => (n === 0 ? "Nein, sofort widerrufen" : n === 1 ? "1 Tag lang" : `${n} Tage lang`),
  graceHelp:
    "Zeit, das neue Secret im Mailserver einzutragen. Höchstens zwei Secrets gelten zugleich: Ein neues widerruft ein Secret, das schon abläuft.",
  create: "Secret erstellen",
  created: "Kopiere das Secret jetzt: Es wird nur einmal angezeigt. Wenn du diesen Dialog schließt, verschwindet es von der Seite.",
  secret: "Secret",
  mapLine: "Zeile für /etc/postfix/archive_sasl",
  previousExpires: (when) => `Das bisherige Secret gilt bis ${when}.`,
  previousRevoked: "Das bisherige Secret ist widerrufen.",
  revoke: "Widerrufen",
  revokeLabel: (hint) => `mas_…${hint} widerrufen`,
  revokeTitle: (hint) => `SMTP-Secret mas_…${hint} widerrufen?`,
  revokeText:
    "Ein Mailserver, der sich damit anmeldet, kann keine Mails mehr abgeben. Seine Kopien warten in seiner Queue, bis er sich mit einem anderen Secret anmeldet.",
  revoked: (hint) => `Das SMTP-Secret mas_…${hint} ist widerrufen.`,
  adminIntro:
    "Die Secrets, mit denen sich Mailserver anmelden, von allen Journaling-Konten. Eine Sperre stoppt sie nicht; widerrufe sie hier. Neue Secrets erstellt der Besitzer des Kontos auf dessen Seite.",
  colAccount: "Konto",
  colOwner: "Besitzer",
  noOwner: "keiner",
  accountRemoved: "entfernt",
  lockHint: (user, n) =>
    `${geheimnisse(n)} von Journaling-Konten, die ${user} besitzt, gelten weiter: Eine Sperre hindert Mailserver nicht daran, Journal-Kopien abzugeben. Widerrufe sie, wenn sie missbraucht werden könnten.`,
  lockHintLink: "Zu den SMTP-Secrets",
};

const dictionaries = { en, de } satisfies Record<Lang, typeof en>;

export type SmtpText = typeof en;

/** The texts in the current UI language (the live lang binding of i18n.ts). */
export function smtpText(): SmtpText {
  return dictionaries[lang];
}

export function smtpTextFor(l: Lang): SmtpText {
  return dictionaries[l];
}
