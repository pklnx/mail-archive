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
