// Carbon's precompiled CSS declares IBM Plex with @font-face rules that load
// the fonts from IBM's CDN (1.www.s81c.com). The app bundles its own Plex
// files (src/fonts.css) and its CSP allows no other host, so these rules go.
//
// @tailwindcss/vite inlines CSS @imports itself, so a Vite transform hook
// never sees Carbon's file. Instead, this plugin writes a stripped copy to
// src/vendor/carbon.css (not committed) when Vite starts, for `pnpm dev`,
// `pnpm build` and Vitest alike; index.css imports that copy.
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import type { Plugin } from "vite";

/** An absolute URL in a CSS url(): http:, https: or protocol-relative. */
const remoteURL = /url\(\s*["']?\s*(?:[a-z][a-z0-9+.-]*:)?\/\//i;

/**
 * Removes every @font-face rule whose src loads from an absolute URL, and a
 * leading @charset (CSS layers do not allow it inside an imported file).
 * Other rules, including @font-face rules with relative URLs, stay as they are.
 */
export function stripRemoteFonts(css: string): { css: string; removed: number } {
  let removed = 0;
  // @font-face blocks contain declarations only, never nested braces.
  const out = css
    .replace(/^﻿?\s*@charset\s+(?:"[^"]*"|'[^']*')\s*;/i, "")
    .replace(/@font-face\s*\{[^{}]*\}/gi, (rule) => {
      if (!remoteURL.test(rule)) return rule;
      removed++;
      return "";
    });
  return { css: out, removed };
}

/** Whether CSS still refers to a remote resource (the build's backstop checks the same). */
export function hasRemoteURL(css: string): boolean {
  return remoteURL.test(css) || /s81c\.com/i.test(css);
}

const webRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
/** The stripped copy that src/index.css imports. */
export const vendoredCarbon = resolve(webRoot, "src/vendor/carbon.css");

/** Writes the stripped Carbon CSS before Vite resolves any CSS. */
export function carbonStyles(): Plugin {
  const write = () => {
    const source = createRequire(import.meta.url).resolve("@carbon/styles/css/styles.min.css");
    const { css, removed } = stripRemoteFonts(readFileSync(source, "utf8"));
    if (removed === 0) throw new Error(`${source}: no remote @font-face rules found; check the strip before upgrading`);
    if (hasRemoteURL(css)) throw new Error(`${source}: a remote URL is left after stripping the fonts`);
    const header = "/* Generated from @carbon/styles/css/styles.min.css by web/build/stripRemoteFonts.ts. Do not edit. */\n";
    mkdirSync(dirname(vendoredCarbon), { recursive: true });
    let current = "";
    try {
      current = readFileSync(vendoredCarbon, "utf8");
    } catch {
      // not written yet
    }
    // Unchanged content is not rewritten, so `pnpm dev` does not reload.
    if (current !== header + css) writeFileSync(vendoredCarbon, header + css);
  };
  return {
    name: "mail-archive:carbon-styles",
    enforce: "pre",
    configResolved: write,
  };
}
