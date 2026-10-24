// Checks the built site (docs/.vitepress/dist) after `vitepress build`, as
// part of `pnpm build`: the pklnx theme (#237) brings IBM Plex and Carbon
// icons, whose licence texts must ship in every build; the default theme's
// Inter font must not; the brand files the config names must be there; and no
// Carbon or IBM package may be installed for the docs (the theme's
// @carbon/styles peer is optional and stays out).
import { readFileSync, readdirSync, existsSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { parseAllDocuments } from "yaml";

const docs = join(dirname(fileURLToPath(import.meta.url)), "..");
const dist = join(docs, ".vitepress/dist");
const designSystem = dirname(createRequire(import.meta.url).resolve("@pklnx/design-system/package.json"));

const errors = [];
const fail = (msg) => errors.push(msg);

/** All files under dir, as paths relative to dist. */
function walk(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) =>
    e.isDirectory() ? walk(join(dir, e.name)) : [relative(dist, join(dir, e.name))],
  );
}

if (!existsSync(join(dist, "index.html"))) {
  console.error(`check-dist: no build in ${dist}`);
  process.exit(1);
}
const files = walk(dist);

// Licence texts, byte-equal to the package's.
const licenses = {
  "licenses/IBM-Plex-OFL.txt": join(designSystem, "project/fonts/OFL.txt"),
  "licenses/pklnx-design-system-notices.txt": join(designSystem, "THIRD_PARTY_NOTICES.md"),
};
for (const [path, source] of Object.entries(licenses)) {
  const built = join(dist, path);
  if (!existsSync(built)) {
    fail(`${path} is missing`);
  } else if (!readFileSync(built).equals(readFileSync(source))) {
    fail(`${path} differs from ${relative(docs, source)}`);
  }
}
if (existsSync(join(dist, "licenses/pklnx-design-system-notices.txt")) &&
  !readFileSync(join(dist, "licenses/pklnx-design-system-notices.txt"), "utf8").includes("Apache License")) {
  fail("licenses/pklnx-design-system-notices.txt lacks the Apache License text");
}

// Fonts: IBM Plex from the theme, no Inter (theme-without-fonts).
const css = files.filter((f) => f.startsWith("assets/") && f.endsWith(".css")).map((f) => readFileSync(join(dist, f), "utf8"));
const plex = css.some((c) =>
  [...c.matchAll(/@font-face\s*\{[^}]*\}/g)].some((m) => /IBM Plex/.test(m[0]) && /url\([^)]*IBMPlex[^)]*\.woff2/.test(m[0])),
);
if (!plex) fail("no built CSS has an @font-face for an IBM Plex woff2");
for (const f of files) {
  if (f.startsWith("assets/") && /\.woff2$/.test(f) && !/IBMPlex/.test(f)) fail(`unexpected font ${f}`);
  if (/inter/i.test(f) && /\.(woff2?|ttf|otf)$/.test(f)) fail(`Inter font emitted: ${f}`);
}
if (!files.some((f) => f.startsWith("assets/") && /IBMPlexSans-Regular.*\.woff2$/.test(f))) fail("IBM Plex Sans woff2 not emitted");
// Tailwind's preflight (from vitepress-openapi) sets the html font from
// --font-sans; custom.css points it at the theme's font, or pages show a
// system font instead of IBM Plex.
if (!css.some((c) => /:root\{--font-sans:var\(--vp-font-family-base\);--font-mono:var\(--vp-font-family-mono\)\}/.test(c))) {
  fail("no CSS points Tailwind's --font-sans and --font-mono at the theme's fonts");
}

// The home page head and footer.
const index = readFileSync(join(dist, "index.html"), "utf8");
/** Whether html has a tag of this name with all these attributes, in any order. */
const hasTag = (html, tag, attrs) =>
  [...html.matchAll(new RegExp(`<${tag}\\s[^>]*>`, "g"))].some((m) =>
    Object.entries(attrs).every(([k, v]) => m[0].includes(` ${k}="${v}"`)),
  );
const lightBar = { name: "theme-color", content: "#ffffff", media: "(prefers-color-scheme: light)" };
const darkBar = { name: "theme-color", content: "#161616", media: "(prefers-color-scheme: dark)" };
if (!hasTag(index, "meta", lightBar)) fail("index.html lacks the light theme-color");
if (!hasTag(index, "meta", darkBar)) fail("index.html lacks the dark theme-color");
const need = {
  "tile icon": /<link rel="icon" type="image\/svg\+xml" href="\/mail-archive\/mail-archive-tile\.svg">/,
  "PNG favicon": /<link rel="icon" type="image\/png" sizes="32x32" href="\/mail-archive\/mail-archive-favicon-32\.png">/,
  "footer OFL link": /href="\/mail-archive\/licenses\/IBM-Plex-OFL\.txt"/,
  "footer notices link": /href="\/mail-archive\/licenses\/pklnx-design-system-notices\.txt"/,
};
for (const [what, re] of Object.entries(need)) if (!re.test(index)) fail(`index.html lacks the ${what}`);
if (/name="theme-color" content="#2563eb"/.test(index)) fail("index.html still has the old theme-color");

// Brand files named by themeConfig.logo, the hero and head.
for (const f of [
  "mail-archive-mark-light.svg",
  "mail-archive-mark-dark.svg",
  "mail-archive-tile.svg",
  "mail-archive-favicon-32.png",
]) {
  if (!files.includes(f)) fail(`${f} is missing from the build`);
}

// No @carbon/ or @ibm/ package installed: the importer's dependencies and the
// packages and snapshots keys. Peer declarations (the theme's optional
// @carbon/styles) are not installs and are not checked.
// pnpm's lockfile holds two documents (pnpm itself, then the project); both count.
const locks = parseAllDocuments(readFileSync(join(docs, "pnpm-lock.yaml"), "utf8")).map((d) => d.toJS() ?? {});
const installed = locks.flatMap((lock) => [
  ...Object.keys(lock.packages ?? {}),
  ...Object.keys(lock.snapshots ?? {}),
  ...Object.values(lock.importers ?? {}).flatMap((imp) =>
    ["dependencies", "devDependencies", "optionalDependencies"].flatMap((k) => Object.keys(imp[k] ?? {})),
  ),
]);
if (!installed.some((name) => name.startsWith("@pklnx/design-system"))) fail("docs/pnpm-lock.yaml: @pklnx/design-system not found");
for (const name of installed) {
  if (/^@(carbon|ibm)\//.test(name)) fail(`docs/pnpm-lock.yaml installs ${name}`);
}

if (errors.length) {
  console.error(`check-dist: ${errors.length} problem(s):\n  ${errors.join("\n  ")}`);
  process.exit(1);
}
console.log(`check-dist: ok (${files.length} files)`);
