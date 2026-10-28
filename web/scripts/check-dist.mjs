// Runs at the end of `pnpm build`: the built UI must load nothing from
// another host. Its CSP allows only the server itself, and IBM's font CDN
// (s81c.com, which Carbon's CSS points at) must never be contacted.
// Fails on:
// - "s81c.com" anywhere in the build,
// - a CSS url() or @import with an absolute URL (http:, https:, //host),
// - no @font-face with a local font file in the CSS,
// - the initial JS of the mail view (#288): over the budget, a lazy chunk
//   missing or in the initial bundle, or a closure that differs from the
//   entry plus its modulepreload links (initialBundle.mjs).
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { checkBundle } from "./initialBundle.mjs";
import { initialBudget, lazyChunks } from "./lazyChunks.mjs";

const dist = fileURLToPath(new URL("../../internal/web/ui/dist/", import.meta.url));
const remoteURL = /url\(\s*["']?\s*(?:[a-z][a-z0-9+.-]*:)?\/\//i;
const remoteImport = /@import\s+(?:url\(\s*)?["']?\s*(?:[a-z][a-z0-9+.-]*:)?\/\//i;
const localFont = /@font-face\s*\{[^{}]*src:\s*url\(\s*["']?\/assets\/[^)"']+\.woff2/i;

function files(dir) {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    return statSync(path).isDirectory() ? files(path) : [path];
  });
}

const problems = [];
let fonts = 0;
const all = files(dist).filter((f) => /\.(css|js|html)$/.test(f));
if (!all.some((f) => f.endsWith(".css"))) problems.push("no CSS file in the build");
for (const file of all) {
  const text = readFileSync(file, "utf8");
  const name = relative(dist, file);
  if (/s81c\.com/i.test(text)) problems.push(`${name}: refers to s81c.com (IBM's font CDN)`);
  if (remoteURL.test(text)) problems.push(`${name}: url() with an absolute URL: ${text.match(remoteURL)[0]}…`);
  if (remoteImport.test(text)) problems.push(`${name}: @import of an absolute URL`);
  if (file.endsWith(".css") && localFont.test(text)) fonts++;
}
if (fonts === 0) problems.push("no @font-face with a local /assets/*.woff2 file in the CSS");

if (problems.length) {
  console.error(`check-dist: the build refers to other hosts or misses its fonts:\n  ${problems.join("\n  ")}`);
  process.exit(1);
}
console.log(`check-dist: ${all.length} files, no remote URLs, local fonts present`);

const bundle = checkBundle(dist, { budget: initialBudget, chunks: lazyChunks });
const kB = (n) => `${(n / 1000).toFixed(1)} kB`;
console.log(
  `check-dist: initial JS ${bundle.raw} bytes (${kB(bundle.raw)}, gzip ${kB(bundle.gzip)}) in ${bundle.closure.length} chunk(s), ` +
    `budget ${initialBudget} (${initialBudget - bundle.raw} bytes left)`,
);
if (bundle.problems.length) {
  console.error(`check-dist: the initial bundle of the mail view is wrong:\n  ${bundle.problems.join("\n  ")}`);
  process.exit(1);
}
console.log(`check-dist: lazy chunks ${lazyChunks.join(", ")} present, none in the initial bundle`);
