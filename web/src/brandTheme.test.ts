// @vitest-environment node
// The pklnx design system theme (#211): index.css loads it right after
// Carbon's CSS and in Carbon's layer, so its token values replace Carbon's
// for the same theme classes, while layout.css stays above both.
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const src = dirname(fileURLToPath(import.meta.url));
const themePath = createRequire(import.meta.url).resolve("@pklnx/design-system/theme/pklnx-carbon.css");

const withoutComments = (css: string) => css.replace(/\/\*[\s\S]*?\*\//g, "");

/** The declarations of the rule whose selector list contains selector. */
function block(css: string, selector: string): Record<string, string> {
  for (const m of withoutComments(css).matchAll(/([^{}]+)\{([^}]*)\}/g)) {
    if (!m[1]!.split(",").map((s) => s.trim()).includes(selector)) continue;
    const decls: Record<string, string> = {};
    for (const d of m[2]!.split(";")) {
      const i = d.indexOf(":");
      if (i > 0) decls[d.slice(0, i).trim()] = d.slice(i + 1).trim();
    }
    return decls;
  }
  throw new Error(`no rule for ${selector}`);
}

describe("pklnx theme", () => {
  it("is imported after Carbon in Carbon's layer, before the app layout", () => {
    const imports = [...withoutComments(readFileSync(resolve(src, "index.css"), "utf8")).matchAll(/@import\s+"([^"]+)"\s*([^;]*);/g)].map(
      (m) => `${m[1]} ${m[2]!.trim()}`.trim(),
    );
    expect(imports).toEqual([
      "./fonts.css",
      "./vendor/carbon.css layer(carbon)",
      "@pklnx/design-system/theme/pklnx-carbon.css layer(carbon)",
      "./layout.css layer(app)",
    ]);
  });

  it("sets Purple as the primary button colour for both theme classes theme.ts uses", () => {
    const css = readFileSync(themePath, "utf8");
    expect(block(css, ".cds--white")["--cds-button-primary"]).toBe("#8a3ffc");
    expect(block(css, ".cds--g100")["--cds-button-primary"]).toBe("#8a3ffc");
  });

  it("sets in the dark block every token the light block sets, so none leaks from :root", () => {
    const css = readFileSync(themePath, "utf8");
    expect(Object.keys(block(css, ".cds--g100")).sort()).toEqual(Object.keys(block(css, ".cds--white")).sort());
  });

  it("refers to no other file or host, so check-dist and the CSP hold", () => {
    expect(withoutComments(readFileSync(themePath, "utf8"))).not.toMatch(/url\(|@import|@font-face/);
  });
});
