// @vitest-environment node
// The initial-bundle computation of check-dist (#288) on a made-up dist.
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { checkBundle, initialBundle, staticImports } from "./initialBundle.mjs";
import { isChunkOf, lazyChunks } from "./lazyChunks.mjs";

let dirs = [];
afterEach(() => {
  for (const d of dirs) rmSync(d, { recursive: true, force: true });
  dirs = [];
});

/** A dist with index.html (entry plus modulepreload links) and the given asset files. */
function dist(files, { entry = "index-A1.js", preloads = [] } = {}) {
  const dir = mkdtempSync(join(tmpdir(), "initial-bundle-"));
  dirs.push(dir);
  mkdirSync(join(dir, "assets"));
  const links = preloads.map((p) => `<link rel="modulepreload" crossorigin href="/assets/${p}">`).join("\n");
  writeFileSync(
    join(dir, "index.html"),
    `<!doctype html><html><head>\n<script type="module" crossorigin src="/assets/${entry}"></script>\n${links}\n<link rel="stylesheet" href="/assets/index.css"></head></html>`,
  );
  for (const [name, code] of Object.entries(files)) writeFileSync(join(dir, "assets", name), code);
  return dir;
}

describe("staticImports", () => {
  it("follows minified and plain static imports and re-exports, never import()", () => {
    const code =
      'import{a as b,c}from"./x.js";import"./y.js";export{z}from"./w.js";import * as n from "./v.js";' +
      'export * from "./u.js";const l=()=>import("./lazy.js");const m=import ("./lazy2.js");';
    expect(staticImports(code)).toEqual(["./x.js", "./y.js", "./w.js", "./v.js", "./u.js"]);
  });
});

describe("initialBundle", () => {
  it("is the entry and its static imports; a dynamically imported chunk is not in it", () => {
    const files = {
      "index-A1.js": 'import{b}from"./b-B2.js";const c=()=>import("./c-C3.js");b(c);',
      "b-B2.js": "export const b=x=>x;",
      "c-C3.js": 'import{b}from"./b-B2.js";export const c=b(1);',
    };
    const b = initialBundle(dist(files, { preloads: ["b-B2.js"] }));
    expect(b.entry).toBe("/assets/index-A1.js");
    expect(b.closure).toEqual(["/assets/index-A1.js", "/assets/b-B2.js"]);
    expect(b.preloads).toEqual(["/assets/b-B2.js"]);
    // Exact byte sum of the two files.
    expect(b.raw).toBe(Buffer.byteLength(files["index-A1.js"]) + Buffer.byteLength(files["b-B2.js"]));
    expect(b.gzip).toBeGreaterThan(0);
  });

  it("follows imports through chunks, once each", () => {
    const b = initialBundle(
      dist(
        {
          "index-A1.js": 'import"./b-B2.js";import"./d-D4.js";',
          "b-B2.js": 'import"./d-D4.js";',
          "d-D4.js": 'export*from"./b-B2.js";',
        },
        { preloads: ["b-B2.js", "d-D4.js"] },
      ),
    );
    expect(b.closure).toEqual(["/assets/index-A1.js", "/assets/b-B2.js", "/assets/d-D4.js"]);
  });

  it("fails on an imported chunk that is not in the build", () => {
    expect(() => initialBundle(dist({ "index-A1.js": 'import"./gone-X.js";' }))).toThrow(/gone-X\.js: imported, but not in the build/);
  });
});

describe("checkBundle", () => {
  const budget = 1000;

  it("passes a split build within the budget", () => {
    const d = dist(
      {
        "index-A1.js": 'import"./b-B2.js";()=>import("./LogPage-L9.js");',
        "b-B2.js": "",
        "LogPage-L9.js": 'import"./b-B2.js";',
      },
      { preloads: ["b-B2.js"] },
    );
    expect(checkBundle(d, { budget, chunks: ["LogPage"] }).problems).toEqual([]);
  });

  it("fails when a modulepreload is not in the closure, or the closure has a chunk no link preloads", () => {
    const d = dist({ "index-A1.js": 'import"./b-B2.js";', "b-B2.js": "", "x-X1.js": "" }, { preloads: ["x-X1.js"] });
    const [problem, ...rest] = checkBundle(d, { budget, chunks: [] }).problems;
    expect(rest).toEqual([]);
    expect(problem).toMatch(/differs from the entry plus its modulepreload links/);
    expect(problem).toMatch(/preloaded but not imported: \/assets\/x-X1\.js/);
    expect(problem).toMatch(/imported but not preloaded: \/assets\/b-B2\.js/);
  });

  it("fails over the budget, with the measured number", () => {
    const d = dist({ "index-A1.js": "x".repeat(1001) });
    expect(checkBundle(d, { budget, chunks: [] }).problems).toEqual([expect.stringMatching(/^initial JS is 1001 bytes, over the budget of 1000/)]);
  });

  it("fails on a missing lazy chunk and on a lazy chunk in the initial bundle", () => {
    const d = dist({ "index-A1.js": 'import"./Profile-P1.js";', "Profile-P1.js": "" }, { preloads: ["Profile-P1.js"] });
    expect(checkBundle(d, { budget, chunks: ["Profile", "UsersPage"] }).problems).toEqual([
      "lazy chunk Profile-P1.js is in the initial bundle",
      "no lazy chunk UsersPage-*.js in the build: something imports ./UsersPage statically",
    ]);
  });
});

describe("lazyChunks", () => {
  it("names each lazy module once and matches only its own chunk", () => {
    expect(new Set(lazyChunks).size).toBe(lazyChunks.length);
    expect(isChunkOf("Profile-D89d5hST.js", "Profile")).toBe(true);
    expect(isChunkOf("Profile-a_b-c.js", "Profile")).toBe(true);
    expect(isChunkOf("ProfilePage-D89d5hST.js", "Profile")).toBe(false);
    expect(isChunkOf("Profile-D89d5hST.css", "Profile")).toBe(false);
    expect(isChunkOf("xProfile-D89d5hST.js", "Profile")).toBe(false);
  });
});
