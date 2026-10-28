import { lang, type Lang } from "./i18n";

// The texts of the settings pages (Accounts, Users, Retention) that i18n.ts
// does not have. Only these pages import this module, so it is part of their
// lazy chunks and not of the initial bundle (#285).

const en = {
  created: "Created",
  openProfile: "Open your profile",
};

const de: typeof en = {
  created: "Erstellt",
  openProfile: "Dein Profil öffnen",
};

const dictionaries = { en, de } satisfies Record<Lang, typeof en>;

export type SettingsText = typeof en;

/** The texts in the current UI language (the live lang binding of i18n.ts). */
export function settingsText(): SettingsText {
  return dictionaries[lang];
}

export function settingsTextFor(l: Lang): SettingsText {
  return dictionaries[l];
}
