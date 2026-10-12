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
      const v = en[key];
      if (typeof v === "string") expect(de[key], key).not.toBe("");
      if (Array.isArray(v)) {
        const d = de[key] as readonly string[];
        expect(d, key).toHaveLength(v.length);
        for (const s of [...v, ...d]) expect(s, key).not.toBe("");
      }
    }
    expect(de.searchIn("Inbox")).toBe("In Inbox suchen (from:, to:)");
    expect(de.switchTheme("dark")).toBe("Zum dunklen Modus wechseln");
    expect(en.messageCount(1, "1")).toBe("1 message");
    expect(en.messageCount(1200, "1,200")).toBe("1,200 messages");
    expect(de.messageCount(1, "1")).toBe("1 Nachricht");
  });
});
