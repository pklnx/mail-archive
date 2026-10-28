import { describe, expect, it } from "vitest";
import { filterText, filterTextFor } from "./filterText";
import { applySettings } from "./i18n";

/** Every string leaf of an object, by its dotted path; functions are called with "x". */
function leaves(o: object, path = ""): [string, string][] {
  return Object.entries(o).flatMap(([k, v]): [string, string][] => {
    const p = path ? `${path}.${k}` : k;
    if (typeof v === "string") return [[p, v]];
    if (typeof v === "function") return [[p, String(v("x"))]];
    return leaves(v as object, p);
  });
}

describe("filterText", () => {
  it("translates every text", () => {
    const en = leaves(filterTextFor("en"));
    const de = new Map(leaves(filterTextFor("de")));
    expect([...de.keys()].sort()).toEqual(en.map(([k]) => k).sort());
    for (const [k, v] of en) {
      expect(de.get(k), k).not.toBe("");
      expect(v, k).not.toBe("");
    }
    expect(filterTextFor("de").presets.custom).toBe("Benutzerdefiniert");
    expect(filterTextFor("de").until).toBe("Bis");
  });

  it("follows the UI language", () => {
    try {
      applySettings({ language: "de", locale: "de" });
      expect(filterText().since).toBe("Ab");
      applySettings({ language: "en", locale: "en" });
      expect(filterText().since).toBe("Since");
    } finally {
      applySettings({ language: "en", locale: "en" });
    }
  });
});
