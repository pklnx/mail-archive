import { afterEach, describe, expect, it } from "vitest";
import { applySettings, languages } from "./i18n";
import { retentionText, retentionTextFor } from "./retentionText";

afterEach(() => applySettings({ language: "en", locale: "en" }));

describe("retention texts (#470 part 4)", () => {
  it("translates every text", () => {
    const en = retentionTextFor("en");
    for (const l of languages) {
      const other = retentionTextFor(l);
      expect(Object.keys(other).sort(), l).toEqual(Object.keys(en).sort());
      for (const key of Object.keys(en) as (keyof typeof en)[]) {
        expect(typeof other[key], `${l}.${key}`).toBe(typeof en[key]);
        if (typeof other[key] === "string") expect(other[key], `${l}.${key}`).not.toBe("");
      }
    }
  });

  it("follows the UI language", () => {
    expect(retentionText().colGroup).toBe("Customer group");
    applySettings({ language: "de", locale: "de" });
    expect(retentionText().colGroup).toBe("Kundengruppe");
  });
});
