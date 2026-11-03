import { afterEach, describe, expect, it } from "vitest";
import { applySettings, languages } from "./i18n";
import { usersText, usersTextFor } from "./usersText";

afterEach(() => applySettings({ language: "en", locale: "en" }));

describe("users texts (#470)", () => {
  it("translates every text", () => {
    const en = usersTextFor("en");
    for (const l of languages) {
      const other = usersTextFor(l);
      expect(Object.keys(other).sort(), l).toEqual(Object.keys(en).sort());
      for (const key of Object.keys(en) as (keyof typeof en)[]) {
        expect(typeof other[key], `${l}.${key}`).toBe(typeof en[key]);
        if (typeof other[key] === "string") expect(other[key], `${l}.${key}`).not.toBe("");
      }
    }
  });

  it("follows the UI language", () => {
    expect(usersText().groupsTitle).toBe("Groups");
    applySettings({ language: "de", locale: "de" });
    expect(usersText().groupsTitle).toBe("Gruppen");
  });
});
