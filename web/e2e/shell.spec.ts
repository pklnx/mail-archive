import { expect, test } from "./fixtures";

// A desktop window just above lg (66rem = 1056 px), not a phone: the rail is
// expanded by hover or focus, then the window narrows below lg (#258).
test.use({ viewport: { width: 1180, height: 820 } });

for (const how of ["hover", "focus"] as const) {
  test(`the rail expanded by ${how} does not stay an inert overlay when the window narrows below lg`, async ({ page }) => {
    await page.goto("/");
    const list = page.getByRole("list", { name: "Messages" });
    await expect(list.getByRole("button").first()).toBeVisible();
    const nav = page.getByRole("navigation", { name: "Main navigation" });
    const scrim = page.locator(".cds--side-nav__overlay-active");

    // At lg and up the SideNav is a rail; expand it and wait for that (Carbon delays it).
    await expect(nav).toHaveClass(/cds--side-nav--rail/);
    await expect(nav).not.toHaveClass(/cds--side-nav--expanded/);
    if (how === "hover") await nav.hover();
    else await nav.getByRole("link", { name: "All mail" }).focus();
    await expect(nav).toHaveClass(/cds--side-nav--expanded/);

    await page.setViewportSize({ width: 820, height: 1180 });
    // The positive signal first: the shell is in its narrow mode (the menu button shows, the rail is gone).
    await expect(page.getByRole("button", { name: "Open navigation" })).toBeVisible();
    await expect(nav).not.toHaveClass(/cds--side-nav--rail/);
    await expect(nav).not.toHaveClass(/cds--side-nav--expanded/);
    await expect(scrim).toHaveCount(0);

    // The page is usable: a message opens in the reading pane.
    await list.getByRole("button").first().click();
    await expect(page.getByRole("article")).toBeVisible();
  });
}
