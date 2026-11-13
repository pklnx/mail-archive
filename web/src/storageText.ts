import type { StorageState } from "./api";
import { lang, type Lang } from "./i18n";

// The texts of the Storage page (#397, part 4). Only StoragePage imports
// this module, so it is part of the lazy Storage chunk and not of the
// initial bundle.

const en = {
  intro:
    "What counts against a quota is the size of the archived messages, each message once: once in the instance, and once in each customer group that holds it. Attachment texts and the database do not count.",
  refresh: "Count again",
  refreshing: "Counting…",
  refreshed: "The figures are counted again. This takes up to a few minutes on a large archive.",
  computedAt: (when: string, ago: string) => `Accounts and groups counted ${ago} (${when}); the count runs every hour.`,
  neverComputed: "The accounts and groups have not been counted yet; the first count runs within an hour of the start.",
  upperBound: "Between counts, a group's figure includes what was added since, and may be slightly too high until the next count.",
  instanceTitle: "Instance",
  used: (used: string, limit: string, percent: number) => `${used} of ${limit} (${percent} %)`,
  usedNoQuota: (used: string) => `${used}, no quota`,
  usage: "Usage",
  quota: "Quota",
  noQuota: "None",
  messages: "Messages",
  state: "State",
  since: (when: string) => `since ${when}`,
  disk: "Disk",
  diskFree: (free: string, total: string) => `${free} free of ${total}`,
  diskReserve: (reserve: string) => `New mail pauses below ${reserve} free.`,
  database: "Database",
  lastBackup: "Last backup",
  noBackup: "None in 30 days",
  encryption: "Encryption at rest",
  encryptionOn: "On",
  encryptionOff: "Off",
  groupQuotaFirst: (quota: string) =>
    `The group's quota of ${quota} applies before the instance quota. It was set at the command line (./ma group set-quota).`,
  instanceQuotaHelp: "The instance quota is set by the operator (MAIL_ARCHIVE_STORAGE_QUOTA).",
  groupsTitle: "Customer groups",
  groupsHelp: (warn: number, overflow: number) =>
    `At ${warn} % of its quota a group is nearly full; at 100 % its syncs and imports pause; from ${100 + overflow} % the SMTP server defers its journal mail, which the sending server retries. A group's quota cannot be above the instance quota.`,
  quotaSum: (sum: string, instance: string) => `The group quotas add up to ${sum}, more than the instance quota of ${instance}.`,
  colGroup: "Group",
  colUsed: "Used",
  colQuota: "Quota",
  colState: "State",
  colActions: "Actions",
  withoutGroup: "Without group",
  editQuota: "Edit quota",
  groupActions: (group: string) => `Actions for group ${group}`,
  quotaTitle: (group: string) => `Storage quota of “${group}”`,
  quotaSize: "Quota",
  quotaUnit: "Unit",
  quotaNone: "No quota (only the instance quota applies)",
  quotaInvalid: "Enter a size above 0.",
  quotaAboveInstance: (instance: string) => `At most the instance quota of ${instance}.`,
  quotaNow: (group: string, used: string) => `${group} uses ${used}.`,
  quotaThen: (percent: number) => `With this quota it is at ${percent} %.`,
  quotaStops: "Its syncs and imports pause at once, and new mail is not archived until space is freed or the quota is raised.",
  quotaWarn: "It is nearly full with this quota.",
  quotaSave: "Save quota",
  quotaSet: (group: string, quota: string) => `The quota of ${group} is now ${quota}.`,
  quotaRemoved: (group: string) => `${group} has no quota now.`,
  accountsTitle: "Accounts",
  accountsHelp:
    "An account's size counts each of its messages once. A message in several accounts counts for each of them, so the sizes add up to more than the instance uses; “Only here” is what removing the account completely would free.",
  colAccount: "Account",
  colOwner: "Owner",
  colMessages: "Messages",
  colSize: "Size",
  colExclusive: "Only here",
  notCounted: "Not counted yet",
  noOwner: "No owner",
  removed: "removed",
  states: {
    ok: "OK",
    warn: "Nearly full",
    full: "Full: new mail paused",
    overflow: "Full: journal mail deferred",
    disk_low: "Disk reserve reached",
  } satisfies Record<StorageState, string>,
};

const de: typeof en = {
  intro:
    "Auf ein Kontingent zählt die Größe der archivierten Nachrichten, jede Nachricht einmal: einmal in der Instanz und einmal in jeder Kundengruppe, die sie hat. Anhangtexte und die Datenbank zählen nicht.",
  refresh: "Neu zählen",
  refreshing: "Zählt…",
  refreshed: "Die Zahlen werden neu gezählt. Bei einem großen Archiv dauert das bis zu einigen Minuten.",
  computedAt: (when, ago) => `Konten und Gruppen gezählt ${ago} (${when}); gezählt wird stündlich.`,
  neverComputed: "Konten und Gruppen sind noch nicht gezählt; die erste Zählung läuft innerhalb einer Stunde nach dem Start.",
  upperBound: "Zwischen zwei Zählungen enthält die Zahl einer Gruppe, was seitdem dazukam, und kann bis zur nächsten Zählung etwas zu hoch sein.",
  instanceTitle: "Instanz",
  used: (used, limit, percent) => `${used} von ${limit} (${percent} %)`,
  usedNoQuota: (used) => `${used}, kein Kontingent`,
  usage: "Belegt",
  quota: "Kontingent",
  noQuota: "Keins",
  messages: "Nachrichten",
  state: "Zustand",
  since: (when) => `seit ${when}`,
  disk: "Festplatte",
  diskFree: (free, total) => `${free} frei von ${total}`,
  diskReserve: (reserve) => `Unter ${reserve} frei pausieren neue Mails.`,
  database: "Datenbank",
  lastBackup: "Letzte Sicherung",
  noBackup: "Keine in 30 Tagen",
  encryption: "Verschlüsselung",
  encryptionOn: "An",
  encryptionOff: "Aus",
  groupQuotaFirst: (quota) =>
    `Das Kontingent der Gruppe von ${quota} greift vor dem der Instanz. Es wurde auf der Kommandozeile gesetzt (./ma group set-quota).`,
  instanceQuotaHelp: "Das Kontingent der Instanz legt der Betreiber fest (MAIL_ARCHIVE_STORAGE_QUOTA).",
  groupsTitle: "Kundengruppen",
  groupsHelp: (warn, overflow) =>
    `Ab ${warn} % ihres Kontingents ist eine Gruppe fast voll; bei 100 % pausieren ihre Synchronisierungen und Importe; ab ${100 + overflow} % stellt der SMTP-Server ihre Journal-Mails zurück, und der sendende Server versucht es erneut. Das Kontingent einer Gruppe kann nicht über dem der Instanz liegen.`,
  quotaSum: (sum, instance) => `Die Kontingente der Gruppen ergeben zusammen ${sum}, mehr als das Kontingent der Instanz von ${instance}.`,
  colGroup: "Gruppe",
  colUsed: "Belegt",
  colQuota: "Kontingent",
  colState: "Zustand",
  colActions: "Aktionen",
  withoutGroup: "Ohne Gruppe",
  editQuota: "Kontingent ändern",
  groupActions: (group) => `Aktionen für Gruppe ${group}`,
  quotaTitle: (group) => `Speicherkontingent von „${group}“`,
  quotaSize: "Kontingent",
  quotaUnit: "Einheit",
  quotaNone: "Kein Kontingent (nur das der Instanz gilt)",
  quotaInvalid: "Gib eine Größe über 0 ein.",
  quotaAboveInstance: (instance) => `Höchstens das Kontingent der Instanz von ${instance}.`,
  quotaNow: (group, used) => `${group} belegt ${used}.`,
  quotaThen: (percent) => `Mit diesem Kontingent sind das ${percent} %.`,
  quotaStops:
    "Ihre Synchronisierungen und Importe pausieren sofort, und neue Mails werden nicht archiviert, bis Platz frei wird oder das Kontingent steigt.",
  quotaWarn: "Mit diesem Kontingent ist sie fast voll.",
  quotaSave: "Kontingent speichern",
  quotaSet: (group, quota) => `Das Kontingent von ${group} ist jetzt ${quota}.`,
  quotaRemoved: (group) => `${group} hat jetzt kein Kontingent.`,
  accountsTitle: "Konten",
  accountsHelp:
    "Die Größe eines Kontos zählt jede seiner Nachrichten einmal. Eine Nachricht in mehreren Konten zählt für jedes, daher ergeben die Größen zusammen mehr, als die Instanz belegt; „Nur hier“ ist, was das vollständige Löschen des Kontos freigäbe.",
  colAccount: "Konto",
  colOwner: "Besitzer",
  colMessages: "Nachrichten",
  colSize: "Größe",
  colExclusive: "Nur hier",
  notCounted: "Noch nicht gezählt",
  noOwner: "Kein Besitzer",
  removed: "entfernt",
  states: {
    ok: "OK",
    warn: "Fast voll",
    full: "Voll: neue Mails pausiert",
    overflow: "Voll: Journal-Mails zurückgestellt",
    disk_low: "Reserve auf der Festplatte erreicht",
  },
};

const dictionaries = { en, de } satisfies Record<Lang, typeof en>;

export type StorageText = typeof en;

/** The texts in the current UI language (the live lang binding of i18n.ts). */
export function storageText(): StorageText {
  return dictionaries[lang];
}

export function storageTextFor(l: Lang): StorageText {
  return dictionaries[l];
}
