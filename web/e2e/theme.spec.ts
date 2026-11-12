// The pklnx design system theme (#211) in the built app: Purple for the
// interactive colour in both themes, and in dark mode Carbon G100's white
// focus and tertiary colours (the five tokens v0.1.0 turned Purple, because
// its light block also matched :root).
import type { Page } from "@playwright/test";
import { expect, test } from "./fixtures";

const purple = "rgb(138, 63, 252)"; // Purple 60, #8a3ffc

/** The tokens' values as the browser resolves them, as rgb(…) colours. */
function resolved(page: Page, tokens: string[]): Promise<Record<string, string>> {
  return page.evaluate((names) => {
    const probe = document.createElement("div");
    document.body.append(probe);
    const out: Record<string, string> = {};
    for (const name of names) {
      probe.style.color = `var(${name})`;
      out[name] = getComputedStyle(probe).color;
    }
    probe.remove();
    return out;
  }, tokens);
}

const tokens = ["--cds-focus", "--cds-icon-interactive", "--cds-button-tertiary", "--cds-button-tertiary-hover", "--cds-button-tertiary-active"];

const expected = {
  // The pklnx light theme: Purple 60, its hover and Purple 80.
  light: {
    "--cds-focus": purple,
    "--cds-icon-interactive": purple,
    "--cds-button-tertiary": purple,
    "--cds-button-tertiary-hover": "rgb(120, 34, 251)",
    "--cds-button-tertiary-active": "rgb(73, 29, 139)",
  },
  // Carbon G100: white, Gray 10 and Gray 30.
  dark: {
    "--cds-focus": "rgb(255, 255, 255)",
    "--cds-icon-interactive": "rgb(255, 255, 255)",
    "--cds-button-tertiary": "rgb(255, 255, 255)",
    "--cds-button-tertiary-hover": "rgb(244, 244, 244)",
    "--cds-button-tertiary-active": "rgb(198, 198, 198)",
  },
};

test("primary buttons are Purple in both themes; focus is Purple in light and white in dark", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "light" });
  await page.goto("/?view=accounts");
  const main = page.getByRole("main");
  const add = main.getByRole("button", { name: "Add account" });
  const html = page.locator("html");

  await expect(html).toHaveClass(/(^|\s)cds--white(\s|$)/);
  await expect(add).toHaveCSS("background-color", purple);
  expect(await resolved(page, tokens)).toEqual(expected.light);

  // theme.ts follows the system: the class on <html> is the signal.
  await page.emulateMedia({ colorScheme: "dark" });
  await expect(html).toHaveClass(/(^|\s)cds--g100(\s|$)/);
  await expect(add).toHaveCSS("background-color", purple);
  expect(await resolved(page, tokens)).toEqual(expected.dark);
});

// Dark mode on the Users and Accounts pages (#330, #333; #335 D1 and D2): the
// table cells, the sticky actions column on a phone, the tiles and an
// account's section form. Each element's effective background (its own and
// its ancestors' backgrounds, composited down to the first opaque one) is
// dark, and each piece of text in it has a WCAG contrast of at least 4.5:1
// against its own effective background. WCAG exempts disabled text, and
// Carbon's placeholders are about 3:1, so neither is checked. The same
// reading in light mode finds light backgrounds, so a theme that does not
// switch fails one side. The mouse stays off the checked rows: a hovered
// row's background is Carbon's layer-hover.
interface Reading {
  /** Elements whose background was not on the expected side. */
  backgrounds: { what: string; luminance: number }[];
  /** Text below 4.5:1. */
  texts: { what: string; contrast: number }[];
  /** How many backgrounds and texts were read, so an empty selection fails. */
  read: { backgrounds: number; texts: number };
}

/** Reads the backgrounds of the elements selector matches and the text inside them. */
function readColours(page: Page, selector: string, scheme: "light" | "dark"): Promise<Reading> {
  return page.evaluate(
    ({ selector, scheme }) => {
      type RGBA = [number, number, number, number];
      const parse = (c: string): RGBA => {
        const m = c.match(/rgba?\(([^)]+)\)/);
        if (!m) throw new Error(`colour ${c}`);
        const [r, g, b, a = "1"] = m[1]!.split(/[ ,/]+/).filter(Boolean);
        return [Number(r), Number(g), Number(b), Number(a)];
      };
      // Source over destination, both in sRGB as the browser paints them.
      const over = (top: RGBA, below: RGBA): RGBA => {
        const a = top[3] + below[3] * (1 - top[3]);
        if (a === 0) return [0, 0, 0, 0];
        const mix = (i: number) => (top[i]! * top[3] + below[i]! * below[3] * (1 - top[3])) / a;
        return [mix(0), mix(1), mix(2), a];
      };
      const background = (el: Element): RGBA => {
        const layers: RGBA[] = [];
        for (let e: Element | null = el; e; e = e.parentElement) {
          const c = parse(getComputedStyle(e).backgroundColor);
          if (c[3] > 0) layers.push(c);
          if (c[3] === 1) break;
        }
        // The canvas behind everything: the root's background has been reached above.
        return layers.reduceRight<RGBA>((below, top) => over(top, below), [255, 255, 255, 1]);
      };
      const luminance = ([r, g, b]: RGBA) => {
        const lin = (v: number) => {
          const s = v / 255;
          return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
        };
        return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
      };
      const contrast = (a: number, b: number) => (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
      const shown = (el: Element) => {
        const r = el.getBoundingClientRect();
        // Visually hidden text (Carbon's 1px clip) is for screen readers only.
        return el.checkVisibility({ visibilityProperty: true }) && r.width > 1 && r.height > 1;
      };
      const disabled = (el: Element) => !!el.closest(':disabled, [aria-disabled="true"], [class*="--disabled"]');
      const name = (el: Element) => `${el.tagName.toLowerCase()}.${[...el.classList].join(".")} "${(el.textContent ?? (el as HTMLInputElement).value ?? "").trim().slice(0, 30)}"`;
      const out = { backgrounds: [] as { what: string; luminance: number }[], texts: [] as { what: string; contrast: number }[], read: { backgrounds: 0, texts: 0 } };
      const roots = [...document.querySelectorAll(selector)].filter(shown);
      for (const root of roots) {
        const l = luminance(background(root));
        out.read.backgrounds++;
        if (scheme === "dark" ? l > 0.1 : l < 0.7) out.backgrounds.push({ what: name(root), luminance: l });
        if (scheme === "light") continue;
        // The elements in it that paint text: their own text nodes, or a field's value.
        const painters = [root, ...root.querySelectorAll("*")].filter(
          (e) =>
            shown(e) &&
            !disabled(e) &&
            (e instanceof HTMLInputElement || e instanceof HTMLTextAreaElement ? e.value !== "" : [...e.childNodes].some((n) => n.nodeType === Node.TEXT_NODE && n.textContent!.trim() !== "")),
        );
        for (const e of painters) {
          const bg = background(e);
          const fg = over(parse(getComputedStyle(e).color), bg);
          const c = contrast(luminance(fg), luminance(bg));
          out.read.texts++;
          if (c < 4.5) out.texts.push({ what: name(e), contrast: Math.round(c * 100) / 100 });
        }
      }
      return out;
    },
    { selector, scheme },
  );
}

/** Checks the selector's elements in both schemes: light backgrounds in light, dark ones with readable text in dark. */
async function bothSchemes(page: Page, selector: string, what: string) {
  const html = page.locator("html");
  // Off the checked rows and tiles: the page's title.
  await page.getByRole("main").getByRole("heading", { level: 1 }).hover();
  await page.emulateMedia({ colorScheme: "light" });
  await expect(html).toHaveClass(/(^|\s)cds--white(\s|$)/);
  const light = await readColours(page, selector, "light");
  expect(light.read.backgrounds, `${what}: elements read`).toBeGreaterThan(0);
  expect(light.backgrounds, `${what}: light backgrounds in light mode`).toEqual([]);
  await page.emulateMedia({ colorScheme: "dark" });
  await expect(html).toHaveClass(/(^|\s)cds--g100(\s|$)/);
  const dark = await readColours(page, selector, "dark");
  expect(dark.read.texts, `${what}: texts read`).toBeGreaterThan(0);
  expect(dark.backgrounds, `${what}: dark backgrounds in dark mode`).toEqual([]);
  expect(dark.texts, `${what}: text contrast in dark mode`).toEqual([]);
}

for (const width of [1280, 375]) {
  test.describe(`${width} px`, () => {
    test.use({ viewport: { width, height: 800 } });

    test("dark mode: the users table, its actions column and a user's tiles", async ({ page }) => {
      await page.goto("/?view=users");
      const main = page.getByRole("main");
      await expect(main.getByRole("link", { name: "kim", exact: true })).toBeVisible();
      // Row headers, cells and, below lg, the sticky actions column.
      await bothSchemes(page, ".users-table tbody th, .users-table tbody td", "users table");
      await main.getByRole("link", { name: "kim", exact: true }).click();
      await expect(main.getByRole("heading", { name: "kim", level: 1 })).toBeVisible();
      await bothSchemes(page, ".app-summary__tile dt, .app-summary__tile dd", "kim's tiles");
    });

    test("dark mode: the accounts table, an account's tiles and its Connection form", async ({ page }) => {
      await page.goto("/?view=accounts");
      const main = page.getByRole("main");
      await expect(main.getByRole("link", { name: "personal", exact: true })).toBeVisible();
      await bothSchemes(page, ".accounts-table tbody th, .accounts-table tbody td", "accounts table");
      await main.getByRole("link", { name: "personal", exact: true }).click();
      await expect(main.getByRole("heading", { name: "personal", level: 1 })).toBeVisible();
      await bothSchemes(page, ".app-summary__tile dt, .app-summary__tile dd", "personal's tiles");
      const connection = main.getByRole("region", { name: "Connection" });
      await connection.getByRole("button", { name: "Edit" }).click();
      await expect(connection.getByLabel("Name", { exact: true })).toBeFocused();
      // Off the field, so its focus colours are not what is read.
      await connection.getByLabel("Name", { exact: true }).blur();
      await bothSchemes(
        page,
        '.page__section:has(> .app-section-head) .cds--label, .page__section:has(> .app-section-head) .cds--text-input, .page__section:has(> .app-section-head) .cds--select-input, .page__section:has(> .app-section-head) .cds--form__helper-text',
        "Connection form",
      );
    });
  });
}
