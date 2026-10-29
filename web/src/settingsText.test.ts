import { afterEach, describe, expect, it } from "vitest";
import { applySettings, languages } from "./i18n";
import { settingsText, settingsTextFor } from "./settingsText";

afterEach(() => applySettings({ language: "en", locale: "en" }));

describe("settings texts", () => {
  it("translates every text", () => {
    const en = settingsTextFor("en");
    for (const l of languages) {
      const other = settingsTextFor(l);
      expect(Object.keys(other).sort(), l).toEqual(Object.keys(en).sort());
      for (const key of Object.keys(en) as (keyof typeof en)[]) {
        expect(typeof other[key], `${l}.${key}`).toBe(typeof en[key]);
        if (typeof other[key] === "string") expect(other[key], `${l}.${key}`).not.toBe("");
      }
    }
    expect(settingsTextFor("de").created).toBe("Erstellt");
  });

  it("follows the UI language", () => {
    expect(settingsText().openProfile).toBe("Open your profile");
    applySettings({ language: "de", locale: "de" });
    expect(settingsText().openProfile).toBe("Dein Profil öffnen");
  });
});
