import { describe, expect, it } from "vitest";
import { detectLocale, messagesFor } from "./i18n";

describe("detectLocale", () => {
  it("picks the first supported browser language", () => {
    expect(detectLocale(["de-DE", "en-US"])).toEqual({ lang: "de", locale: "de-DE" });
    expect(detectLocale(["fr-FR", "en-GB", "de"])).toEqual({ lang: "en", locale: "en-GB" });
    expect(detectLocale(["DE-at"])).toEqual({ lang: "de", locale: "de-AT" });
  });
  it("falls back to English", () => {
    expect(detectLocale(["fr", "es"])).toEqual({ lang: "en", locale: "en" });
    expect(detectLocale([])).toEqual({ lang: "en", locale: "en" });
  });
});

describe("messages", () => {
  it("translates every text", () => {
    const en = messagesFor("en");
    const de = messagesFor("de");
    expect(Object.keys(de).sort()).toEqual(Object.keys(en).sort());
    for (const key of Object.keys(en) as (keyof typeof en)[]) {
      if (typeof en[key] === "string") expect(de[key], key).not.toBe("");
    }
    expect(de.searchIn("Inbox")).toBe("In Inbox suchen");
    expect(de.switchTheme("dark")).toBe("Zum dunklen Modus wechseln");
  });
});
