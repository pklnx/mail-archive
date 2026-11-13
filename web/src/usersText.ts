import { lang, type Lang } from "./i18n";

// The texts of customer groups on the Users page (#470 part 2). Only
// UsersPage imports this module, so it is part of the lazy Users chunk and
// not of the initial bundle, as settingsText.ts is for the settings pages.

const sharesEnded = (n: number) => (n === 0 ? "No shares ended." : n === 1 ? "1 share ended." : `${n} shares ended.`);
const grantsEnded = (n: number) => (n === 0 ? "" : n === 1 ? " 1 auditor grant ended." : ` ${n} auditor grants ended.`);
/** The grants a removal ended, as "“Audit 2023” (pat), …". */
const grantList = (grants: { label: string; owner: string }[]) => grants.map((g) => `“${g.label}” (${g.owner})`).join(", ");

const en = {
  colGroup: "Group",
  allGroups: "All groups",
  chooseGroup: "Choose a group",
  groupRequired: "Choose a group.",
  changeGroup: "Change group",
  changeGroupTitle: (user: string) => `Change the group of “${user}”`,
  newGroup: "New group",
  changeGroupText: (user: string, group: string, auditor = false) =>
    `Shares and auditor grants between ${user} and users of ${group} end.${auditor ? "" : ` The accounts ${user} owns move along.`}`,
  groupChanged: (user: string, group: string, n: number, grants = 0) => `${user} is now in ${group}. ${sharesEnded(n)}${grantsEnded(grants)}`,
  // Auditor logins (#393 part 3).
  role: "Role",
  roleMember: "User",
  roleAdmin: "Admin",
  roleAuditor: "Auditor",
  roleAuditorTag: "auditor",
  auditorHelp:
    "An auditor reads only what owners of its group grant it, for a limited time and read-only. It must set up two-factor authentication at first sign-in and never owns accounts. Give a tax advisor one login per customer group.",
  removeAuditorText: (user: string) => `The auditor grants of ${user} end at once; they stay in the owners' lists as history.`,
  auditorRemoved: (user: string, grants: { label: string; owner: string }[]) =>
    grants.length === 0 ? `${user} is removed.` : `${user} is removed. Auditor grants ended: ${grantList(grants)}.`,
  groupsTitle: "Groups",
  groupsHelp:
    "Every user belongs to one customer group, every account to its owner's. Users share accounts only within their group and never see other groups' users.",
  addGroup: "Add group",
  groupName: "Group name",
  groupNameRequired: "Enter a name.",
  colUsers: "Users",
  groupActions: (group: string) => `Actions for group ${group}`,
  renameGroup: "Rename",
  renameGroupTitle: (group: string) => `Rename “${group}”`,
  removeGroup: "Remove",
  removeGroupTitle: (group: string) => `Remove “${group}”?`,
  removeGroupText: "The group has no users. This cannot be undone.",
  groupHasUsers: "Groups with users cannot be removed; move them to another group first.",
  colQuota: "Storage quota",
  noQuota: "None",
  quotaHelp: "Quotas are set on the Storage page.",
};

const de: typeof en = {
  colGroup: "Gruppe",
  allGroups: "Alle Gruppen",
  chooseGroup: "Gruppe wählen",
  groupRequired: "Wähle eine Gruppe.",
  changeGroup: "Gruppe ändern",
  changeGroupTitle: (user) => `Gruppe von „${user}“ ändern`,
  newGroup: "Neue Gruppe",
  changeGroupText: (user, group, auditor = false) =>
    `Freigaben und Prüferzugänge zwischen ${user} und Benutzern von ${group} enden.${auditor ? "" : ` Die Konten von ${user} ziehen mit um.`}`,
  groupChanged: (user, group, n, grants = 0) =>
    `${user} ist jetzt in ${group}. ${n === 0 ? "Keine Freigabe endete." : n === 1 ? "1 Freigabe endete." : `${n} Freigaben endeten.`}${
      grants === 0 ? "" : grants === 1 ? " 1 Prüferzugang endete." : ` ${grants} Prüferzugänge endeten.`
    }`,
  role: "Rolle",
  roleMember: "Benutzer",
  roleAdmin: "Admin",
  roleAuditor: "Prüfer",
  roleAuditorTag: "Prüfer",
  auditorHelp:
    "Ein Prüfer liest nur, was ihm Besitzer seiner Gruppe freigeben, befristet und nur lesend. Er muss bei der ersten Anmeldung die Zwei-Faktor-Authentifizierung einrichten und besitzt nie Konten. Eine Steuerberatung bekommt eine Anmeldung je Kundengruppe.",
  removeAuditorText: (user) => `Die Prüferzugänge von ${user} enden sofort; sie bleiben als Verlauf in den Listen der Besitzer.`,
  auditorRemoved: (user, grants) =>
    grants.length === 0
      ? `${user} ist entfernt.`
      : `${user} ist entfernt. Beendete Prüferzugänge: ${grants.map((g) => `„${g.label}“ (${g.owner})`).join(", ")}.`,
  groupsTitle: "Gruppen",
  groupsHelp:
    "Jeder Benutzer gehört zu einer Kundengruppe, jedes Konto zu der seines Besitzers. Benutzer teilen Konten nur innerhalb ihrer Gruppe und sehen nie Benutzer anderer Gruppen.",
  addGroup: "Gruppe hinzufügen",
  groupName: "Gruppenname",
  groupNameRequired: "Gib einen Namen ein.",
  colUsers: "Benutzer",
  groupActions: (group) => `Aktionen für Gruppe ${group}`,
  renameGroup: "Umbenennen",
  renameGroupTitle: (group) => `„${group}“ umbenennen`,
  removeGroup: "Entfernen",
  removeGroupTitle: (group) => `„${group}“ entfernen?`,
  removeGroupText: "Die Gruppe hat keine Benutzer. Das lässt sich nicht rückgängig machen.",
  groupHasUsers: "Gruppen mit Benutzern lassen sich nicht entfernen; verschiebe sie zuerst in eine andere Gruppe.",
  colQuota: "Speicherkontingent",
  noQuota: "Keins",
  quotaHelp: "Kontingente werden auf der Seite Speicher festgelegt.",
};

const dictionaries = { en, de } satisfies Record<Lang, typeof en>;

export type UsersText = typeof en;

/** The texts in the current UI language (the live lang binding of i18n.ts). */
export function usersText(): UsersText {
  return dictionaries[lang];
}

export function usersTextFor(l: Lang): UsersText {
  return dictionaries[l];
}
