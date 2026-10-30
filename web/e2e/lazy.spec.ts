// The non-mail views and the shortcuts overview load on demand (#288),
// against the real Go handler and the real build: the mail view loads none
// of their chunks, a view's chunk comes with the asset headers, and every
// view loads by direct URL and on reload.
import type { Page } from "@playwright/test";
import { isChunkOf, lazyChunks } from "../scripts/lazyChunks.mjs";
import { expect, test } from "./fixtures";

const attr = (tag: string, name: string) => tag.match(new RegExp(`\\s${name}\\s*=\\s*["']([^"']*)["']`, "i"))?.[1];
const fileOf = (path: string) => path.split("/").pop() ?? "";

/** The JS files the served index.html loads up front: its module entry and its modulepreload links. */
async function initialFiles(page: Page): Promise<Set<string>> {
  const html = await (await page.request.get("/")).text();
  const entries = [...html.matchAll(/<script\b[^>]*>/gi)].map((m) => m[0]).filter((tag) => attr(tag, "type") === "module").map((tag) => attr(tag, "src"));
  const preloads = [...html.matchAll(/<link\b[^>]*>/gi)].map((m) => m[0]).filter((tag) => attr(tag, "rel") === "modulepreload").map((tag) => attr(tag, "href"));
  const files = [...entries, ...preloads].filter((p): p is string => !!p).map(fileOf);
  expect(entries).toHaveLength(1);
  return new Set(files);
}

/** The /assets/*.js files the page has fetched so far. */
async function loadedScripts(page: Page): Promise<string[]> {
  const paths = await page.evaluate(() => performance.getEntriesByType("resource").map((e) => new URL(e.name).pathname));
  return paths.filter((p) => p.startsWith("/assets/") && p.endsWith(".js")).map(fileOf);
}

const chunkResponse = (page: Page, name: string) => page.waitForResponse((r) => r.request().method() === "GET" && isChunkOf(fileOf(new URL(r.url()).pathname), name));

test("the mail view loads no lazy chunk", async ({ page }) => {
  const initial = await initialFiles(page);
  await page.goto("/");
  await expect(page.getByRole("list", { name: "Messages" }).getByRole("button").first()).toBeVisible();
  const loaded = await loadedScripts(page);
  expect(loaded.length).toBeGreaterThan(0);
  for (const file of loaded) {
    expect(initial, `${file} is not in the initial bundle`).toContain(file);
    for (const name of lazyChunks) expect(isChunkOf(file, name), `${file} is the lazy chunk ${name}`).toBe(false);
  }
});

test("a view's chunk loads on navigation, with the asset headers; the CSP is unchanged", async ({ page }) => {
  const doc = await page.goto("/");
  expect(doc?.headers()["content-security-policy"]).toContain("script-src 'self'");
  await expect(page.getByRole("list", { name: "Messages" }).getByRole("button").first()).toBeVisible();
  const chunk = chunkResponse(page, "LogPage");
  await page.getByRole("navigation", { name: "Main navigation" }).getByRole("link", { name: "Log" }).click();
  const res = await chunk;
  await expect(page.getByRole("heading", { name: "Log", level: 1 })).toBeVisible();
  expect(res.status()).toBe(200);
  const headers = res.headers();
  expect(headers["content-type"]).toMatch(/^text\/javascript/);
  expect(headers["cache-control"]).toBe("public, max-age=31536000, immutable");
  expect(headers["x-content-type-options"]).toBe("nosniff");
});

const views = [
  { view: "profile", heading: "Profile" },
  { view: "log", heading: "Log" },
  { view: "accounts", heading: "Accounts" },
  { view: "accounts&id=personal", heading: "personal" },
  { view: "users", heading: "Users" },
  // A detail page loads its view's chunk too.
  { view: "users&id=kim", heading: "kim" },
  { view: "retention", heading: "Retention" },
  { view: "retention&tab=holds", heading: "Retention" },
  // The demo's one rule.
  { view: "retention&id=1", heading: "/ work /" },
];
for (const { view, heading } of views) {
  test(`${view} loads by direct URL and on reload`, async ({ page }) => {
    await page.goto(`/?view=${view}`);
    const h1 = page.getByRole("main").getByRole("heading", { name: heading, level: 1 });
    await expect(h1).toBeVisible();
    await page.reload();
    await expect(h1).toBeVisible();
  });
}

test("the Profile view works after a lazy load, and g a goes back to the mail", async ({ page }) => {
  await page.goto("/?view=profile");
  const main = page.getByRole("main");
  await expect(main.getByRole("heading", { name: "Profile", level: 1 })).toBeVisible();
  await main.getByRole("tab", { name: "Sign-in & security" }).click();
  const changePassword = main.getByRole("heading", { name: "Change password", level: 2 });
  await expect(changePassword).toBeVisible();
  // Every tab panel stays mounted: the field of this section.
  await expect(main.locator("section").filter({ has: page.getByRole("heading", { name: "Change password", level: 2 }) }).getByLabel("Current password", { exact: true })).toBeVisible();
  // Outside any widget, so the shortcut is not the tab list's.
  await main.getByRole("heading", { name: "Profile", level: 1 }).click();
  await page.keyboard.press("g");
  await page.keyboard.press("a");
  await expect(page.getByRole("list", { name: "Messages" }).getByRole("button").first()).toBeVisible();
  await expect(page).toHaveURL(/\/$/);
});

test("? loads the shortcuts overview; Escape closes it and the focus goes back", async ({ page }) => {
  await page.goto("/");
  const first = page.getByRole("list", { name: "Messages" }).getByRole("button").first();
  await expect(first).toBeVisible();
  await first.focus();
  const chunk = chunkResponse(page, "ShortcutsModal");
  await page.keyboard.press("?");
  expect((await chunk).status()).toBe(200);
  const dialog = page.getByRole("dialog", { name: "Keyboard shortcuts" });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByText("All mail")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(first).toBeFocused();
});

// One "Loading…" per screen, and no skeleton in the accessibility tree
// (#287, #340; #335 R6), through the real lazy chunk and the real data load.
// The screen's data request is held until the test releases it, so the
// loading state can be observed; a MutationObserver installed before the
// app's script records the most "Loading…" statuses present at once.
// "Once" means at most one at any time, not one node for the whole load: by
// design the lazy fallback's status (ViewFallback) comes first and the
// view's own status follows it once the chunk is there, one after the
// other, never both (lazyView.tsx, Loading and ViewFallback). A status in a
// hidden tab panel (Profile keeps every panel mounted) is not counted: the
// browser exposes nothing of a hidden panel.
const loadingStates = [
  { name: "Users", url: "/?view=users", held: /^\/api\/users$/, loaded: (p: Page) => p.getByRole("main").getByRole("link", { name: "kim", exact: true }) },
  { name: "Accounts", url: "/?view=accounts", held: /^\/api\/accounts$/, loaded: (p: Page) => p.getByRole("main").getByRole("link", { name: "personal", exact: true }) },
  { name: "Retention rules", url: "/?view=retention", held: /^\/api\/retention\/rules$/, loaded: (p: Page) => p.getByRole("main").getByRole("row").filter({ hasText: "§147 AO" }) },
  { name: "Retention holds", url: "/?view=retention&tab=holds", held: /^\/api\/legal-holds$/, loaded: (p: Page) => p.getByRole("main").getByRole("tabpanel").getByRole("button", { name: "Add hold" }) },
  { name: "Retention runs", url: "/?view=retention&tab=runs", held: /^\/api\/retention\/runs$/, loaded: (p: Page) => p.getByRole("main").getByRole("tabpanel").getByRole("button", { name: "New deletion run" }) },
  { name: "Log", url: "/?view=log", held: /^\/api\/audit$/, loaded: (p: Page) => p.getByRole("main").getByRole("table") },
  {
    name: "Profile, Sign-in & security",
    url: "/?view=profile",
    tab: "Sign-in & security",
    held: /^\/api\/profile\/(2fa|passkeys)$/,
    loaded: (p: Page) => p.getByRole("main").getByRole("tabpanel", { name: "Sign-in & security" }).getByRole("button", { name: "Add passkey" }),
  },
  { name: "the start-up gate", url: "/", held: /^\/api\/session$/, noSkeleton: true, loaded: (p: Page) => p.getByRole("list", { name: "Messages" }).getByRole("button").first() },
];

for (const { name, url, tab, held, loaded, noSkeleton } of loadingStates) {
  test(`${name}: one Loading… at a time while it loads, its skeletons hidden from screen readers`, async ({ page }) => {
    await page.addInitScript((text) => {
      const w = window as unknown as { loadingMost: number; loadingAt: string[] };
      w.loadingMost = 0;
      w.loadingAt = [];
      // Only statuses a screen reader can get: rendered (not in a hidden tab
      // panel) and outside any aria-hidden subtree.
      const exposed = () =>
        [...document.querySelectorAll('[role="status"]')].filter(
          (e) => (e.textContent ?? "").includes(text) && e.checkVisibility({ visibilityProperty: true }) && !e.closest('[aria-hidden="true"]'),
        );
      new MutationObserver(() => {
        const now = exposed();
        if (now.length > w.loadingMost) {
          w.loadingMost = now.length;
          w.loadingAt = now.map((e) => e.parentElement?.closest("[class]")?.className ?? "");
        }
      }).observe(document, { childList: true, subtree: true, characterData: true, attributes: true });
    }, "Loading…");
    let release!: () => void;
    const released = new Promise<void>((r) => (release = r));
    await page.route(
      (u) => held.test(u.pathname),
      async (route) => {
        if (route.request().method() !== "GET") return route.fallback();
        await released;
        await route.fallback();
      },
    );
    const requested = page.waitForRequest((r) => r.method() === "GET" && held.test(new URL(r.url()).pathname));
    await page.goto(url);
    if (tab) await page.getByRole("main").getByRole("tab", { name: tab }).click();
    await requested;

    // Held: the status says Loading…, and every skeleton is inside an aria-hidden subtree.
    const loading = page.getByRole("status").filter({ hasText: "Loading…" });
    await expect(loading).toHaveCount(1);
    const skeletons = () =>
      page.evaluate(() => {
        const all = [...document.querySelectorAll('[class*="skeleton"]')];
        return { count: all.length, exposed: all.filter((e) => !e.closest('[aria-hidden="true"]')).map((e) => e.className) };
      });
    const during = await skeletons();
    // The gate has no skeleton, only its status.
    if (!noSkeleton) expect(during.count).toBeGreaterThan(0);
    expect(during.exposed).toEqual([]);

    release();
    await expect(loaded(page)).toBeVisible();
    await expect(loading).toHaveCount(0);
    expect((await skeletons()).count).toBe(0);
    // At least one status was seen (the hold made it observable), and never two at once.
    const most = await page.evaluate(() => {
      const w = window as unknown as { loadingMost: number; loadingAt: string[] };
      return { most: w.loadingMost, at: w.loadingAt };
    });
    expect(most.most, `statuses at once, inside: ${most.at.join(" | ")}`).toBe(1);
  });
}
