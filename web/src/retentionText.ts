import { lang, type Lang } from "./i18n";

// The texts of customer groups on the Retention page (#470 part 4), and
// of the Add hold form's filter limit (#538). Only
// RetentionPage and RetentionRun import this module, so it is part of the
// lazy Retention chunk and not of the initial bundle, as usersText.ts is
// for the Users page.

const en = {
  colGroup: "Customer group",
  allGroups: "All groups",
  anyGroup: "Any group",
  noGroup: "No group",
  holdGroupHint: "Only the mail of the group's users, also of users who join it later.",
  holdScopeGroup: (group: string) => `group ${group}`,
  holdFilterOwnOnly:
    "Only after: and before: here, unless the hold is on you or on one of your own accounts. A hold with a search filter over other users' mail is added by the operator: ./ma hold add --query",
};

const de: typeof en = {
  colGroup: "Kundengruppe",
  allGroups: "Alle Gruppen",
  anyGroup: "Alle Gruppen",
  noGroup: "Keine Gruppe",
  holdGroupHint: "Nur die Mail der Benutzer der Gruppe, auch derer, die später hinzukommen.",
  holdScopeGroup: (group) => `Gruppe ${group}`,
  holdFilterOwnOnly:
    "Hier nur after: und before:, außer der Hold gilt für dich oder eines deiner eigenen Konten. Einen Hold mit Suchfilter über die Mail anderer Benutzer legt der Betreiber an: ./ma hold add --query",
};

const dictionaries = { en, de } satisfies Record<Lang, typeof en>;

export type RetentionText = typeof en;

/** The texts in the current UI language (the live lang binding of i18n.ts). */
export function retentionText(): RetentionText {
  return dictionaries[lang];
}

export function retentionTextFor(l: Lang): RetentionText {
  return dictionaries[l];
}
