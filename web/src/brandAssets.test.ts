// @vitest-environment node
// The mail-archive logo files (#237) are copied unchanged from
// pklnx/design-system; brand-assets.json records the source and checksum of
// each. BRAND-ASSETS.md holds their terms; LICENSE stays plain MIT.
import { createHash } from "node:crypto";
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { dirname, extname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const repo = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const read = (path: string) => readFileSync(resolve(repo, path));
const text = (path: string) => readFileSync(resolve(repo, path), "utf8");
const sha256 = (data: Buffer) => createHash("sha256").update(data).digest("hex");

interface BrandAssets {
  repository: string;
  commit: string;
  files: { path: string; source: string; sha256: string }[];
}
const assets = JSON.parse(text("brand-assets.json")) as BrandAssets;
const listed = assets.files.map((f) => f.path).sort();

/** Files in dir (relative to the repository) whose extension is in exts. */
function filesIn(dir: string, exts: string[]): string[] {
  return readdirSync(resolve(repo, dir), { withFileTypes: true })
    .filter((e) => e.isFile() && exts.includes(extname(e.name)))
    .map((e) => `${dir}/${e.name}`);
}

/** Width and height from a PNG's IHDR chunk. */
function pngSize(data: Buffer): [number, number] {
  expect(data.subarray(1, 4).toString("latin1")).toBe("PNG");
  expect(data.subarray(12, 16).toString("latin1")).toBe("IHDR");
  return [data.readUInt32BE(16), data.readUInt32BE(20)];
}

describe("brand assets", () => {
  it("names a full design-system commit", () => {
    expect(assets.repository).toBe("https://github.com/pklnx/design-system");
    expect(assets.commit).toMatch(/^[0-9a-f]{40}$/);
  });

  it("are unchanged copies: every listed file has its recorded sha256", () => {
    expect(assets.files.length).toBeGreaterThan(0);
    for (const f of assets.files) {
      expect(f.source, f.path).toMatch(/^project\/assets\/Logos\/products\/mail-archive-/);
      expect(f.sha256, f.path).toMatch(/^[0-9a-f]{64}$/);
      expect(existsSync(resolve(repo, f.path)), f.path).toBe(true);
      expect(sha256(read(f.path)), f.path).toBe(f.sha256);
    }
  });

  it("lists every brand file, so none is added without a row", () => {
    const present = [
      ...filesIn("web/src/brand", [".svg", ".png"]),
      ...filesIn("web/public", [".png", ".svg"]),
      ...filesIn("docs/public", [".svg", ".png"]),
    ].sort();
    // web/src/brand holds only brand files.
    expect(readdirSync(resolve(repo, "web/src/brand")).map((n) => `web/src/brand/${n}`).sort()).toEqual(
      listed.filter((p) => p.startsWith("web/src/brand/")),
    );
    expect(present).toEqual(listed);
  });

  it("BRAND-ASSETS.md lists exactly these files and states the terms", () => {
    const md = text("BRAND-ASSETS.md");
    const section = md.split(/^## Files$/m)[1];
    expect(section, "no Files section").toBeDefined();
    const paths = [...section!.matchAll(/^- `([^`]+)`$/gm)].map((m) => m[1]!).sort();
    expect(paths).toEqual(listed);
    expect(md.replace(/\s+/g, " ")).toContain(
      "You may redistribute these files unchanged as part of unmodified copies of this project; modified versions must replace them.",
    );
    expect(md).toContain("](LICENSE)");
    expect(md).toContain("](README.md)");
  });

  it("LICENSE stays the plain MIT licence", () => {
    // The plain MIT text as of #237 (maintainer decision: the logo terms live
    // in BRAND-ASSETS.md, not in LICENSE). A deliberate change to LICENSE
    // updates this hash in the same PR.
    expect(sha256(read("LICENSE"))).toBe("3ab472d18a36afd1b4960db218d4c7d23f5b46b297f04017b8323b3290f05363");
  });

  it("README.md links BRAND-ASSETS.md", () => {
    expect(text("README.md")).toContain("](BRAND-ASSETS.md)");
  });

  it("the web app manifest names existing icons of the stated sizes and white colours", () => {
    const manifest = JSON.parse(text("web/public/manifest.webmanifest")) as {
      background_color: string;
      theme_color: string;
      icons: { src: string; sizes: string; type: string; purpose: string }[];
    };
    expect(manifest.background_color).toBe("#ffffff");
    expect(manifest.theme_color).toBe("#ffffff");
    for (const icon of manifest.icons) {
      const file = `web/public${icon.src}`;
      expect(existsSync(resolve(repo, file)), icon.src).toBe(true);
      const [w, h] = pngSize(read(file));
      expect(`${w}x${h}`, icon.src).toBe(icon.sizes);
    }
    expect(manifest.icons.filter((i) => i.purpose === "maskable")).toHaveLength(1);
  });

  it("index.html links the PNG and SVG favicons, the manifest and the touch icon", () => {
    const html = text("web/index.html");
    expect(html).toContain('<link rel="icon" type="image/png" sizes="32x32" href="/favicon-32.png" />');
    expect(html).toContain('<link rel="icon" type="image/svg+xml" href="/src/brand/mail-archive-tile.svg" />');
    expect(html).toContain('<link rel="manifest" href="/manifest.webmanifest" />');
    expect(html).toContain('<link rel="apple-touch-icon" href="/apple-touch-icon.png" />');
    expect(pngSize(read("web/public/favicon-32.png"))).toEqual([32, 32]);
  });
});
