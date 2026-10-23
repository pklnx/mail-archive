// @vitest-environment node
// The views use Carbon and layout.css only (#168). Every class name in the
// source is Carbon's (cds--) or defined in layout.css: a utility class left
// over from Tailwind, or a typo, would style nothing. It reads only the
// className= attributes of the top-level .tsx files in src: class names set
// another way (props such as Carbon's renderIcon, classList, other folders)
// are not checked.
import { readFileSync, readdirSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const src = dirname(fileURLToPath(import.meta.url));

/** The class names layout.css defines. */
function layoutClasses(): Set<string> {
  const css = readFileSync(resolve(src, "layout.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  return new Set([...css.matchAll(/\.(-?[a-zA-Z_][\w-]*)/g)].map((m) => m[1]!));
}

/** The expression of each className={…} (braces balanced), or its "…" string. */
function classNameValues(code: string): string[] {
  const values: string[] = [];
  for (const m of code.matchAll(/className=("[^"]*"|\{)/g)) {
    if (m[1] !== "{") {
      values.push(m[1]!);
      continue;
    }
    let depth = 1;
    let i = m.index! + m[0].length;
    for (; i < code.length && depth > 0; i++) depth += code[i] === "{" ? 1 : code[i] === "}" ? -1 : 0;
    values.push(code.slice(m.index! + m[0].length, i - 1));
  }
  return values;
}

/**
 * The class names in the className values of a component file: the text of
 * its string literals and the static text of its templates. A literal that
 * is compared (=== "active") is no class name, and a name built at run time
 * (block--${x}) is left out.
 */
function usedClasses(code: string): string[] {
  const names: string[] = [];
  for (const value of classNameValues(code)) {
    // A template's ${…} parts are expressions of their own.
    const parts: string[] = [];
    const text = value.replace(/`([^`]*)`/g, (_, tpl: string) => {
      const plain = tpl.replace(/\$\{([^}]*)\}/g, (__, expr: string) => {
        parts.push(expr);
        return " ";
      });
      return `"${plain}"`;
    });
    for (const expr of [text, ...parts]) {
      for (const m of expr.matchAll(/(===?|!==?)?\s*"([^"]*)"/g)) {
        if (m[1]) continue;
        // A name that ends in - or _ was completed by a ${…}: built at run time.
        names.push(...m[2]!.split(/\s+/).filter((c) => c && !/[-_]$/.test(c)));
      }
    }
  }
  return names;
}

/** A class layout.css defines, or the block of BEM elements it defines (shell-nav for shell-nav__count). */
function isDefined(defined: Set<string>, c: string): boolean {
  return defined.has(c) || [...defined].some((d) => d.startsWith(`${c}__`) || d.startsWith(`${c}--`));
}

const components = () =>
  readdirSync(src)
    .filter((f) => f.endsWith(".tsx") && !f.endsWith(".test.tsx"))
    .map((f) => ({ file: f, code: readFileSync(resolve(src, f), "utf8") }));

describe("class names", () => {
  it("finds the names in each form of className", () => {
    const code = [
      `<a className="x y" />`,
      `<b className={"z"} />`,
      "<c className={`p q--${v}${w ? \" r\" : \"\"}`} />",
      `<d className={k === "on" ? "s" : "t u"} />`,
      `<e className={[f ? "" : "v", g ? "w" : ""].join(" ")} />`,
    ].join("\n");
    expect(usedClasses(code)).toEqual(["x", "y", "z", "p", "r", "s", "t", "u", "v", "w"]);
  });

  it("uses only Carbon's classes and those of layout.css", () => {
    const defined = layoutClasses();
    expect(defined.has("account-tile")).toBe(true);
    const unknown = components().flatMap(({ file, code }) =>
      usedClasses(code)
        .filter((c) => !c.startsWith("cds--") && !isDefined(defined, c))
        .map((c) => `${file}: ${c}`),
    );
    expect(unknown).toEqual([]);
  });

  it("finds class names in every view", () => {
    // Guards the scan itself: an empty result would pass the test above.
    const counted = components().map(({ file, code }) => [file, usedClasses(code).length] as const);
    for (const file of ["App.tsx", "AccountsPage.tsx", "Profile.tsx", "UsersPage.tsx", "MessageList.tsx"]) {
      expect(counted.find(([f]) => f === file)?.[1], file).toBeGreaterThan(0);
    }
  });
});
