// @vitest-environment node
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { hasRemoteURL, stripRemoteFonts } from "./stripRemoteFonts";

const fixture = readFileSync(new URL("./fixtures/carbon-fonts.css", import.meta.url), "utf8");

describe("stripRemoteFonts", () => {
  it("removes the remote @font-face rules and the @charset, nothing else", () => {
    expect(hasRemoteURL(fixture)).toBe(true);
    const { css, removed } = stripRemoteFonts(fixture);
    // https:, http: and protocol-relative.
    expect(removed).toBe(3);
    expect(css).not.toMatch(/@charset/);
    expect(css).not.toMatch(/s81c|cdn\.example/);
    expect(hasRemoteURL(css)).toBe(false);
    // A local @font-face, the reset, a data: URL with an http namespace and media queries stay.
    expect(css).toContain("@font-face{font-family:Local Font;src:url(./fonts/local.woff2)");
    expect(css).toContain("a,body{padding:0;border:0}");
    expect(css).toContain("xmlns='http://www.w3.org/2000/svg'");
    expect(css).toContain("@media (min-width:42rem){.cds--grid{max-width:99rem}}");
  });

  it("strips the real Carbon CSS clean", () => {
    const carbon = readFileSync(new URL("../node_modules/@carbon/styles/css/styles.min.css", import.meta.url), "utf8");
    const { css, removed } = stripRemoteFonts(carbon);
    expect(removed).toBeGreaterThan(0);
    expect(hasRemoteURL(css)).toBe(false);
    expect(css).not.toMatch(/@font-face/);
  });
});
