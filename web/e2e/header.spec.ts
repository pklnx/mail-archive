import { expect, test } from "./fixtures";

const cases = [
  { theme: "light", width: 1280 },
  { theme: "light", width: 375 },
  { theme: "dark", width: 1280 },
  { theme: "dark", width: 375 },
] as const;

for (const { theme, width } of cases) {
  test(`the ${theme} header mark is centred on the label at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 800 });
    await page.emulateMedia({ colorScheme: theme });
    await page.goto("/");

    const name = page.locator(".app-header__name");
    const mark = name.locator(".app-header__logo");
    await expect(mark).toBeVisible();
    await expect(name).toContainText("Mail Archive");
    await expect(mark).toHaveClass(new RegExp(`(^|\\s)app-mark--${theme}(\\s|$)`));

    // Load the actual label face before measuring it, so canvas metrics cannot
    // accidentally validate a fallback font.
    const font = await name.evaluate(async (anchor) => {
      const text = anchor.textContent ?? "";
      const label = anchor.querySelector(":scope > span");
      if (!label) throw new Error("Carbon HeaderName label span was not found");
      const style = getComputedStyle(label);
      const loadedFont = `${style.fontWeight} ${style.fontSize} ${style.fontFamily}`;
      await document.fonts.load(loadedFont, text);
      return { font: loadedFont, loaded: document.fonts.check(loadedFont, text) };
    });
    expect(font.loaded, `label font ${font.font} must be loaded`).toBe(true);

    const metrics = await page.evaluate(() => {
      const anchor = document.querySelector(".app-header__name");
      const header = document.querySelector(".cds--header");
      const mark = document.querySelector(".app-header__logo");
      const label = anchor?.querySelector(":scope > span");
      const textNode = label && Array.from(label.childNodes).find(
        (node): node is Text => node.nodeType === Node.TEXT_NODE && Boolean(node.textContent?.trim()),
      );
      if (!(anchor instanceof HTMLElement) || !(header instanceof HTMLElement) ||
          !(mark instanceof HTMLImageElement) || !(textNode instanceof Text) || !label) {
        throw new Error("header mark or label text was not found");
      }

      // The visible art in both mail-archive-mark-light.svg and
      // mail-archive-mark-dark.svg is centred at y=34 in the 64 px viewBox.
      const markRect = mark.getBoundingClientRect();
      const headerRect = header.getBoundingClientRect();
      const range = document.createRange();
      range.selectNodeContents(textNode);
      const textRect = range.getBoundingClientRect();
      const style = getComputedStyle(label);
      const canvas = document.createElement("canvas");
      const context = canvas.getContext("2d");
      if (!context) throw new Error("canvas text metrics are unavailable");
      context.font = `${style.fontWeight} ${style.fontSize} ${style.fontFamily}`;
      const textMetrics = context.measureText("H");
      const baseline = textRect.top + textMetrics.fontBoundingBoxAscent;
      const capCentre = baseline - textMetrics.actualBoundingBoxAscent / 2;
      const drawnCentre = markRect.top + markRect.height * (34 / 64);

      return {
        difference: drawnCentre - capCentre,
        markTop: markRect.top,
        markBottom: markRect.bottom,
        headerTop: headerRect.top,
        headerBottom: headerRect.bottom,
      };
    });

    expect(Math.abs(metrics.difference), `drawn centre offset: ${metrics.difference}px`).toBeLessThanOrEqual(0.5);
    expect(metrics.markTop).toBeGreaterThanOrEqual(metrics.headerTop);
    expect(metrics.markBottom).toBeLessThanOrEqual(metrics.headerBottom);
  });
}
