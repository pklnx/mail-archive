import { afterEach, describe, expect, it, vi } from "vitest";
import * as i18n from "./i18n";
import { applySettings, detectLocale, languages, messagesFor, needsReload, resolve, storedSettings, t, translateWithId } from "./i18n";

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

describe("Carbon texts", () => {
  /** Every string leaf of an object, by its dotted path. */
  const leaves = (o: object, path = ""): [string, unknown][] =>
    Object.entries(o).flatMap(([k, v]) => (v && typeof v === "object" ? leaves(v, `${path}${k}.`) : [[`${path}${k}`, v]]));

  it("translates every Carbon text", () => {
    const en = new Map(leaves(messagesFor("en").carbon));
    const de = new Map(leaves(messagesFor("de").carbon));
    expect([...de.keys()].sort()).toEqual([...en.keys()].sort());
    for (const [key, v] of de) {
      expect(v, key).toBeTypeOf("string");
      expect(v, key).not.toBe("");
    }
    expect(messagesFor("de").carbon.showPassword).toBe("Passwort anzeigen");
  });

  it("looks up translateWithId ids in the current language", () => {
    const de = messagesFor("de");
    de.carbon.ids["test.id"] = "Test-Text";
    try {
      applySettings({ language: "de", locale: "de" });
      expect(translateWithId("test.id")).toBe("Test-Text");
      // A missing id shows as itself, so the German rendering tests find it.
      expect(translateWithId("missing.id")).toBe("missing.id");
      applySettings({ language: "en", locale: "en" });
      expect(translateWithId("test.id")).toBe("test.id");
    } finally {
      delete de.carbon.ids["test.id"];
      applySettings({ language: "en", locale: "en" });
    }
  });
});

describe("resolve", () => {
  const auto = "auto";
  it.each([
    [auto, auto, ["en-GB", "de"], "en", "en-GB"],
    ["de", auto, ["de-AT", "en"], "de", "de-AT"],
    // A chosen language takes the browser's format in that language, else the bare language.
    ["de", auto, ["en-GB"], "de", "de"],
    ["en", auto, ["de-DE", "en-GB"], "en", "en-GB"],
    ["en", "de-DE", ["de-DE"], "en", "de-DE"],
    ["de", "en-IE", ["de-DE"], "de", "en-IE"],
    ["de", "DE-at", ["en"], "de", "de-AT"],
    ["fr", auto, ["de-DE"], "de", "de-DE"],
    [auto, "x_y", ["en-US"], "en", "en-US"],
    ["", "", ["fr"], "en", "en"],
  ])("%s / %s on %j gives %s / %s", (language, locale, prefs, lang, loc) => {
    expect(resolve({ language, locale }, prefs)).toEqual({ lang, locale: loc });
  });
});

describe("needsReload", () => {
  it("is true only when the texts or formats change", () => {
    const prefs = ["en-US", "en"];
    expect(needsReload({ language: "en", locale: "auto" }, { language: "auto", locale: "auto" }, prefs)).toBe(false);
    expect(needsReload({ language: "auto", locale: "en-US" }, { language: "auto", locale: "auto" }, prefs)).toBe(false);
    expect(needsReload({ language: "de", locale: "auto" }, { language: "auto", locale: "auto" }, prefs)).toBe(true);
    expect(needsReload({ language: "auto", locale: "en-GB" }, { language: "auto", locale: "auto" }, prefs)).toBe(true);
  });
});

describe("settings", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    localStorage.clear();
    applySettings({ language: "auto", locale: "auto" });
  });
  it("applies to the live bindings and <html lang>", () => {
    applySettings({ language: "de", locale: "auto" }, ["en-US"]);
    expect(t.language).toBe("Sprache");
    expect(i18n.lang).toBe("de");
    expect(i18n.locale).toBe("de");
    expect(document.documentElement.lang).toBe("de");
    expect(storedSettings()).toEqual({ language: "de", locale: "auto" });
    applySettings({ language: "en", locale: "de-AT" }, ["en-US"]);
    expect(t.language).toBe("Language");
    expect(i18n.locale).toBe("de-AT");
  });
  it("reads invalid or blocked storage as auto", () => {
    localStorage.setItem("mail-archive.language", "{not json");
    expect(storedSettings()).toEqual({ language: "auto", locale: "auto" });
    localStorage.setItem("mail-archive.language", JSON.stringify({ language: 1, locale: "de" }));
    expect(storedSettings()).toEqual({ language: "auto", locale: "auto" });
    const blocked = {
      getItem: () => {
        throw new Error("blocked");
      },
      setItem: () => {
        throw new Error("blocked");
      },
    };
    vi.stubGlobal("localStorage", blocked);
    expect(storedSettings()).toEqual({ language: "auto", locale: "auto" });
    // Applied anyway, only not remembered.
    applySettings({ language: "de", locale: "auto" }, ["en-US"]);
    expect(t.language).toBe("Sprache");
  });
  it("names every language in itself", () => {
    const names = languages.map((l) => messagesFor(l).languageName);
    expect(new Set(names).size).toBe(languages.length);
    for (const n of names) expect(n).not.toBe("");
    expect(names).toEqual(["English", "Deutsch"]);
  });
});
