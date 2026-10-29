// @vitest-environment node
// Motion (#287): the app's own animations and transitions are written only
// inside @media (prefers-reduced-motion: no-preference), so the system's
// "reduce motion" setting leaves none of them; the reduce block only turns
// off Carbon's own (SideNav, its overlay, the user panel). The motion
// tokens are Carbon's values.
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const src = dirname(fileURLToPath(import.meta.url));
const css = readFileSync(resolve(src, "layout.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

/** The top-level @media blocks of a stylesheet: their condition, body and place. */
function mediaBlocks(text: string): { query: string; start: number; end: number }[] {
  const blocks: { query: string; start: number; end: number }[] = [];
  for (const m of text.matchAll(/@media([^{]*)\{/g)) {
    if (blocks.some((b) => m.index! < b.end)) continue; // nested
    let depth = 1;
    let i = m.index! + m[0].length;
    for (; i < text.length && depth > 0; i++) depth += text[i] === "{" ? 1 : text[i] === "}" ? -1 : 0;
    blocks.push({ query: m[1]!.trim(), start: m.index!, end: i });
  }
  return blocks;
}

/** The animation and transition declarations: property, value and place. */
function motionDeclarations(text: string) {
  return [...text.matchAll(/(?<![\w-])((?:animation|transition)(?:-[a-z-]+)?)\s*:\s*([^;}]+)/g)].map((m) => ({ prop: m[1]!, value: m[2]!.trim(), at: m.index! }));
}

describe("motion", () => {
  const blocks = mediaBlocks(css);
  const inside = (at: number, query: RegExp) => blocks.some((b) => at > b.start && at < b.end && query.test(b.query));
  const noPreference = /^\(prefers-reduced-motion: no-preference\)/;
  const reduce = /^\(prefers-reduced-motion: reduce\)$/;

  it("has the app's motion only where no reduced motion is asked for", () => {
    const decls = motionDeclarations(css);
    // Guards the scan: the folder overlay, the conversation and the toggle animate.
    expect(decls.filter((d) => inside(d.at, noPreference)).length).toBeGreaterThanOrEqual(5);
    const outside = decls.filter((d) => !inside(d.at, noPreference) && !(inside(d.at, reduce) && d.value === "none"));
    expect(outside.map((d) => `${d.prop}: ${d.value}`)).toEqual([]);
  });

  it("finds a declaration outside the block (the check itself)", () => {
    const sample = "@media (prefers-reduced-motion: no-preference) { .a { animation: x 1s; } }\n.b { transition: opacity 1s; }";
    const b = mediaBlocks(sample);
    const outside = motionDeclarations(sample).filter((d) => !b.some((x) => d.at > x.start && d.at < x.end));
    expect(outside.map((d) => d.prop)).toEqual(["transition"]);
  });

  it("turns off Carbon's SideNav, its overlay and the user panel under reduced motion", () => {
    const block = blocks.find((b) => reduce.test(b.query));
    expect(block).toBeDefined();
    const body = css.slice(block!.start, block!.end);
    for (const sel of [".app-shell > .cds--side-nav,", ".app-shell > .cds--side-nav__overlay", ".cds--header-panel.app-user-panel"]) expect(body).toContain(sel);
    expect(body).toMatch(/transition:\s*none/);
  });

  it("uses Carbon's motion tokens (@carbon/motion tokens/motion.tokens.json)", () => {
    // @carbon/motion comes with @carbon/styles.
    const require = createRequire(createRequire(import.meta.url).resolve("@carbon/styles/package.json"));
    const tokens = JSON.parse(readFileSync(resolve(dirname(require.resolve("@carbon/motion/package.json")), "tokens/motion.tokens.json"), "utf8"));
    const ms = (d: { $value: { value: number; unit: string } }) => `${d.$value.value}${d.$value.unit}`;
    const bezier = (e: { $value: number[] }) => `cubic-bezier(${e.$value.join(", ")})`;
    const vars = Object.fromEntries([...css.matchAll(/(--app-(?:duration|easing)-[\w-]+)\s*:\s*([^;]+);/g)].map((m) => [m[1]!, m[2]!.trim()]));
    expect(vars).toEqual({
      "--app-duration-fast-02": ms(tokens.duration.fast["02"]),
      "--app-duration-moderate-01": ms(tokens.duration.moderate["01"]),
      "--app-easing-entrance-productive": bezier(tokens.easing.entrance.productive),
      "--app-easing-entrance-expressive": bezier(tokens.easing.entrance.expressive),
    });
    // Every duration and easing the motion uses is one of these.
    for (const d of motionDeclarations(css).filter((d) => d.value !== "none")) {
      expect(d.value.replace(/var\(--app-[\w-]+\)/g, "").replace(/[a-z-]+/g, "").trim(), `${d.prop}: ${d.value}`).toBe("");
    }
  });
});
