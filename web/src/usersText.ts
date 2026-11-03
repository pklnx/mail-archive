import { lang, type Lang } from "./i18n";

// The texts of customer groups on the Users page (#470 part 2). Only
// UsersPage imports this module, so it is part of the lazy Users chunk and
// not of the initial bundle, as settingsText.ts is for the settings pages.

const sharesEnded = (n: number) => (n === 0 ? "No shares ended." : n === 1 ? "1 share ended." : `${n} shares ended.`);

const en = {
  colGroup: "Group",
  allGroups: "All groups",
  chooseGroup: "Choose a group",
  groupRequired: "Choose a group.",
  changeGroup: "Change group",
  changeGroupTitle: (user: string) => `Change the group of “${user}”`,
  newGroup: "New group",
  changeGroupText: (user: string, group: string) =>
    `Shares between ${user} and users of ${group} end. The accounts ${user} owns move along.`,
  groupChanged: (user: string, group: string, n: number) => `${user} is now in ${group}. ${sharesEnded(n)}`,
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
};

const de: typeof en = {
  colGroup: "Gruppe",
  allGroups: "Alle Gruppen",
  chooseGroup: "Gruppe wählen",
  groupRequired: "Wähle eine Gruppe.",
  changeGroup: "Gruppe ändern",
  changeGroupTitle: (user) => `Gruppe von „${user}“ ändern`,
  newGroup: "Neue Gruppe",
  changeGroupText: (user, group) =>
    `Freigaben zwischen ${user} und Benutzern von ${group} enden. Die Konten von ${user} ziehen mit um.`,
  groupChanged: (user, group, n) =>
    `${user} ist jetzt in ${group}. ${n === 0 ? "Keine Freigabe endete." : n === 1 ? "1 Freigabe endete." : `${n} Freigaben endeten.`}`,
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
