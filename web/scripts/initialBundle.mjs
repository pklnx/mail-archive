// The initial JS of the built UI (#288): the entry chunk that index.html
// loads and every chunk it imports statically, followed through the chunks.
// Dynamic import() is not followed: those chunks load on demand.
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { dirname, join, posix } from "node:path";
import { gzipSync } from "node:zlib";
import { isChunkOf } from "./lazyChunks.mjs";

// Static imports and re-exports of a relative module, minified or not:
// `import{a as b}from"./x.js"`, `import"./y.js"`, `export{z}from"./w.js"`,
// `import * as n from "./v.js"`. The bindings cannot hold "(" or a quote,
// so `import("./c.js")` never matches.
const staticImport = /\b(?:import|export)\s*(?:[\w$*{}\s,]*?\bfrom\s*)?["'](\.{1,2}\/[^"']+\.js)["']/g;

/** The relative modules one chunk imports statically. */
export function staticImports(code) {
  return [...code.matchAll(staticImport)].map((m) => m[1]);
}

const attr = (tag, name) => tag.match(new RegExp(`\\s${name}\\s*=\\s*["']([^"']*)["']`, "i"))?.[1];

/**
 * Reads dist/index.html and follows the entry's static imports. Returns the
 * files of the closure and of the modulepreload links (paths as in the
 * HTML, e.g. "/assets/index-x.js"), the raw byte sum and the gzip sum.
 */
export function initialBundle(dist) {
  const html = readFileSync(join(dist, "index.html"), "utf8");
  const scripts = [...html.matchAll(/<script\b[^>]*>/gi)].map((m) => m[0]).filter((tag) => attr(tag, "type") === "module" && attr(tag, "src"));
  if (scripts.length !== 1) throw new Error(`index.html: expected one <script type="module" src>, found ${scripts.length}`);
  const entry = attr(scripts[0], "src");
  const preloads = [...html.matchAll(/<link\b[^>]*>/gi)]
    .map((m) => m[0])
    .filter((tag) => attr(tag, "rel") === "modulepreload")
    .map((tag) => attr(tag, "href"));

  const closure = [];
  const seen = new Set();
  const visit = (path) => {
    if (seen.has(path)) return;
    seen.add(path);
    const file = join(dist, path);
    if (!existsSync(file)) throw new Error(`${path}: imported, but not in the build`);
    closure.push(path);
    const code = readFileSync(file, "utf8");
    for (const rel of staticImports(code)) visit(posix.join(posix.dirname(path), rel));
  };
  visit(entry);

  let raw = 0;
  let gzip = 0;
  for (const path of closure) {
    const bytes = readFileSync(join(dist, path));
    raw += bytes.length;
    gzip += gzipSync(bytes).length;
  }
  return { entry, closure, preloads, raw, gzip };
}

/**
 * The bundle check of check-dist.mjs. Fails closed: a closure that differs
 * from the entry plus its modulepreload links (Vite emits chunks some other
 * way, the count could be short), initial JS over the budget, a lazy chunk
 * missing from the build, or a lazy chunk in the initial closure.
 */
export function checkBundle(dist, { budget, chunks }) {
  const b = initialBundle(dist);
  const problems = [];
  const expected = new Set([b.entry, ...b.preloads]);
  const missing = [...expected].filter((p) => !b.closure.includes(p));
  const extra = b.closure.filter((p) => !expected.has(p));
  if (missing.length || extra.length) {
    problems.push(
      `the static import closure of ${b.entry} differs from the entry plus its modulepreload links` +
        `${missing.length ? `; preloaded but not imported: ${missing.join(", ")}` : ""}` +
        `${extra.length ? `; imported but not preloaded: ${extra.join(", ")}` : ""}` +
        ` (closure: ${b.closure.join(", ")}; preloads: ${b.preloads.join(", ") || "none"})`,
    );
  }
  if (b.raw > budget) {
    problems.push(
      `initial JS is ${b.raw} bytes, over the budget of ${budget}: load more of it lazily, ` +
        `or raise the budget in scripts/lazyChunks.mjs only in a PR that says why (above 579000 the #282 "Done when" breaks)`,
    );
  }
  const assets = dirname(join(dist, b.entry));
  const files = readdirSync(assets);
  for (const name of chunks) {
    const own = files.filter((f) => isChunkOf(f, name));
    if (own.length === 0) problems.push(`no lazy chunk ${name}-*.js in the build: something imports ./${name} statically`);
    for (const f of own) {
      if (b.closure.some((p) => posix.basename(p) === f)) problems.push(`lazy chunk ${f} is in the initial bundle`);
    }
  }
  return { ...b, problems };
}
