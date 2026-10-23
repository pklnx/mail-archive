// @vitest-environment node
// layout.css sits in a layer above Tailwind's utilities. A class there with
// the name of a utility (text-warning, break-words …) would override that
// utility in every view not converted to Carbon yet (#171 review).
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { compile } from "tailwindcss";
import { describe, expect, it } from "vitest";

const src = dirname(fileURLToPath(import.meta.url));
const tailwind = resolve(src, "../node_modules/tailwindcss");

/** The class names layout.css defines. */
function layoutClasses(): string[] {
  const css = readFileSync(resolve(src, "layout.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  return [...new Set([...css.matchAll(/\.(-?[a-zA-Z_][\w-]*)/g)].map((m) => m[1]!))].filter((c) => !c.startsWith("cds--"));
}

/** Tailwind as the app configures it (index.css, with its own theme), without the other stylesheets. */
async function appTailwind() {
  return compile(readFileSync(resolve(src, "index.css"), "utf8"), {
    base: src,
    loadStylesheet: async (id: string, base: string) => {
      const file = id.startsWith("tailwindcss/") ? resolve(tailwind, id.slice("tailwindcss/".length)) : null;
      return { path: file ?? resolve(base, id), base: file ? tailwind : base, content: file ? readFileSync(file, "utf8") : "" };
    },
  });
}

describe("layout.css", () => {
  it("defines no class that is also a Tailwind utility", async () => {
    const classes = layoutClasses();
    expect(classes).toContain("account-tile");
    // build() adds to what it built before: one compiler for the classes,
    // another for the check that it finds utilities at all.
    const out = (await appTailwind()).build(classes);
    expect(classes.filter((c) => out.includes(`.${c} {`))).toEqual([]);
    const known = (await appTailwind()).build(["text-warning", "break-words", "break-all"]);
    for (const c of ["text-warning", "break-words", "break-all"]) expect(known, c).toContain(`.${c} {`);
  });
});
