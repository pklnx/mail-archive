// The design system's rule "at most one kind="primary" button per view"
// (docs/development/index.md, Web UI) is held by per-component tests alone.
// This spec counts the primary buttons of every view a person reaches, so a
// new conflict fails here, naming the view and its buttons (#229).
//
// A view is a page, a tab panel or a modal, and each is counted alone:
// - a modal: its dialog (the page behind it is another view),
// - a tab: the visible tab panel,
// - a page: its <main>, or the whole page in the mail view, where the
//   message list sits beside the reader's <main>.
//
// Carbon puts the primary class on two buttons that are not primary actions:
// the copy button of a CodeSnippet and the close X of a Modal. They are
// excluded, as Profile.test.tsx does for the copy button.
import { origin, unique } from "./demo";
import { accountRow, expect, test } from "./fixtures";
import type { Locator, Page } from "@playwright/test";

const primary = ".cds--btn--primary:not(.cds--copy-btn):not(.cds--modal-close)";

/** The visible primary buttons in a view, by their names. */
async function primaries(scope: Locator): Promise<string[]> {
  return scope
    .locator(primary)
    .filter({ visible: true })
    .evaluateAll((els) => els.map((el) => (el.getAttribute("aria-label") || el.textContent || el.className).trim()));
}

interface View {
  name: string;
  /**
   * Opens the view and waits for its own signal (a heading, a known control,
   * the first row of the list), so that the count never runs before React
   * has rendered. Returns the scope to count in.
   */
  open: (page: Page) => Promise<Locator>;
  /** The one primary the view must show: a positive control for the selector and the scope. */
  exactly?: string;
  /** Undoes what open set up (an entry made through the API), after the count. */
  cleanup?: (page: Page) => Promise<void>;
}

/** A page of the app, ready when its heading shows. */
async function mainPage(page: Page, url: string, heading: string): Promise<Locator> {
  await page.goto(url);
  return ready(page, heading);
}

async function ready(page: Page, heading: string): Promise<Locator> {
  const main = page.getByRole("main");
  await expect(main.getByRole("heading", { name: heading, level: 1, exact: true })).toBeVisible();
  return main;
}

/** A tab of a tabbed page: its panel is the view. */
async function tabPanel(page: Page, url: string, heading: string, name: string, signal: (panel: Locator) => Locator): Promise<Locator> {
  const main = await mainPage(page, url, heading);
  const tab = main.getByRole("tab", { name, exact: true });
  await tab.click();
  await expect(tab).toHaveAttribute("aria-selected", "true");
  // Carbon keeps the other panels in the DOM, hidden; getByRole skips them.
  const panel = main.getByRole("tabpanel");
  await expect(panel).toHaveCount(1);
  await expect(signal(panel)).toBeVisible();
  return panel;
}

/** A tab opened by its URL: its panel is the view. */
async function tabByURL(page: Page, url: string, heading: string, name: string, signal: (panel: Locator) => Locator): Promise<Locator> {
  const main = await mainPage(page, url, heading);
  await expect(main.getByRole("tab", { name, exact: true })).toHaveAttribute("aria-selected", "true");
  const panel = main.getByRole("tabpanel");
  await expect(panel).toHaveCount(1);
  await expect(signal(panel)).toBeVisible();
  return panel;
}

const messages = (page: Page) => page.getByRole("list", { name: "Messages" });

/** A request to the demo's API, as the page sends it (same origin). */
async function api(page: Page, method: string, url: string, data?: unknown): Promise<unknown> {
  const res = await page.request.fetch(url, { method, data, headers: { Origin: origin, "Content-Type": "application/json" } });
  expect(res.ok(), `${method} ${url}: ${res.status()}`).toBe(true);
  return (res.headers()["content-type"] ?? "").includes("application/json") ? res.json() : null;
}

// The entries the hold and run views make, and their cleanup removes again:
// a hold whose filter matches nothing, and a rule with a run proposed for it.
let holdId = 0;
async function addHold(page: Page): Promise<string> {
  const name = unique("e2e-primary hold");
  holdId = ((await api(page, "POST", "/api/legal-holds", {
    name, reason: "primary buttons", user: "", accountId: null, filter: { from: "nobody@primary.example" }, since: null, until: null, all: false,
  })) as { id: number }).id;
  return name;
}
async function releaseHold(page: Page) {
  if (holdId) await api(page, "POST", `/api/legal-holds/${holdId}/release`, { note: "e2e done" });
  holdId = 0;
}
let ruleId = 0;
let runId = 0;
async function proposeRun(page: Page): Promise<number> {
  const { accounts } = (await api(page, "GET", "/api/retention/rules")) as { accounts: { id: number; name: string }[] };
  const personal = accounts.find((a) => a.name === "personal")!;
  ruleId = ((await api(page, "POST", "/api/retention/rules", {
    accountId: personal.id, folder: "Archive/2014", years: 10, timeZone: "Europe/Berlin", note: "primary buttons", force: false,
  })) as { id: number }).id;
  const run = (await api(page, "POST", "/api/retention/runs")) as { id: number; candidates: number };
  runId = run.id;
  expect(run.candidates).toBeGreaterThan(0);
  return run.candidates;
}
async function cancelRun(page: Page) {
  if (runId) await api(page, "POST", `/api/retention/runs/${runId}/cancel`);
  if (ruleId) await api(page, "DELETE", `/api/retention/rules/${ruleId}`);
  runId = 0;
  ruleId = 0;
}
async function runPage(page: Page): Promise<Locator> {
  const n = await proposeRun(page);
  await page.goto(`/?view=retention&tab=runs&id=${runId}`);
  const main = page.getByRole("main");
  await expect(main.getByRole("heading", { name: `Run ${runId}: ${n} message(s), `, level: 1 })).toBeVisible();
  await expect(main.getByRole("button", { name: `Delete ${n} messages` })).toBeVisible();
  return main;
}

const views: View[] = [
  {
    name: "Mail list",
    open: async (page) => {
      await page.goto("/");
      await expect(messages(page).getByRole("button").first()).toBeVisible();
      return page.locator("body");
    },
  },
  {
    name: "Mail, opened message",
    open: async (page) => {
      await page.goto("/");
      await messages(page).getByRole("button").first().click();
      await expect(page.getByRole("article").getByRole("heading", { level: 1 })).toBeVisible();
      return page.locator("body");
    },
  },

  {
    // The list shows its one primary: the positive control for the selector and the scope.
    name: "Accounts",
    exactly: "Add account",
    open: async (page) => {
      const main = await mainPage(page, "/?view=accounts", "Accounts");
      await expect(main.getByRole("button", { name: "Add account" })).toBeVisible();
      return main;
    },
  },
  { name: "Accounts, Add account", open: (page) => mainPage(page, "/?view=accounts&add=1", "Add account") },
  {
    // An account's page: Sync is its one primary.
    name: "Accounts, account page",
    exactly: "Sync",
    open: async (page) => {
      await mainPage(page, "/?view=accounts", "Accounts");
      await accountRow(page, "personal").getByRole("link", { name: "personal", exact: true }).click();
      return ready(page, "personal");
    },
  },
  {
    // A section's form: Save is tertiary, Cancel ghost.
    name: "Accounts, account page, Connection open",
    exactly: "Sync",
    open: async (page) => {
      const main = await mainPage(page, "/?view=accounts&id=personal", "personal");
      await main.getByRole("region", { name: "Connection" }).getByRole("button", { name: "Edit" }).click();
      await expect(main.getByLabel("IMAP server", { exact: true })).toBeVisible();
      return main;
    },
  },
  {
    name: "Accounts, account page, Remove dialog",
    open: async (page) => {
      const main = await mainPage(page, "/?view=accounts&id=personal", "personal");
      await main.getByRole("button", { name: "Actions for personal" }).click();
      await page.getByRole("menuitem", { name: "Remove" }).click();
      const dialog = page.getByRole("dialog", { name: "Remove “personal”?" });
      await expect(dialog.getByRole("button", { name: "Cancel" })).toBeVisible();
      return dialog;
    },
  },
  { name: "Accounts, smtp account page", open: (page) => mainPage(page, "/?view=accounts&id=journal", "journal") },
  { name: "Accounts, removed account page", open: (page) => mainPage(page, "/?view=accounts&id=old-provider", "old-provider") },
  {
    // The demo's smtp (journal) account is the one whose row's menu offers
    // the import; its name comes from the row.
    name: "Accounts, Import from IMAP",
    open: async (page) => {
      const main = await mainPage(page, "/?view=accounts", "Accounts");
      const row = main.getByRole("row").filter({ has: page.getByRole("button", { name: "complete (SMTP)" }) });
      const account = (await row.getByRole("rowheader").getByRole("link").textContent())?.trim() ?? "";
      expect(account).not.toBe("");
      return mainPage(page, `/?view=accounts&import=${encodeURIComponent(account)}`, "Import existing mail from IMAP");
    },
  },

  {
    name: "Users",
    open: async (page) => {
      const main = await mainPage(page, "/?view=users", "Users");
      await expect(main.getByRole("button", { name: "Add user" })).toBeVisible();
      return main;
    },
  },
  {
    // The modal alone: the page behind it keeps its own primary.
    name: "Users, Add user dialog",
    open: async (page) => {
      const main = await mainPage(page, "/?view=users", "Users");
      await main.getByRole("button", { name: "Add user" }).click();
      const dialog = page.getByRole("dialog", { name: "Add user" });
      await expect(dialog.getByLabel("User name")).toBeVisible();
      return dialog;
    },
  },
  {
    // The one-time password has a CodeSnippet with a copy button.
    name: "Users, generated password",
    open: async (page) => {
      const name = unique("e2e-primary");
      const main = await mainPage(page, "/?view=users", "Users");
      await main.getByRole("button", { name: "Add user" }).click();
      const dialog = page.getByRole("dialog", { name: "Add user" });
      await dialog.getByLabel("User name").fill(name);
      await dialog.getByRole("button", { name: "Add user" }).click();
      await expect(dialog).toHaveCount(0);
      const generated = main.getByRole("status").filter({ hasText: `Password for ${name}` });
      await expect(generated.getByRole("button", { name: "Copy to clipboard" })).toBeVisible();
      return main;
    },
  },

  // A user's page: Reset password is tertiary, the rest is in the menu.
  { name: "Users, user page", open: (page) => mainPage(page, "/?view=users&id=kim", "kim") },
  { name: "Users, own user page", open: (page) => mainPage(page, "/?view=users&id=demo", "demo") },
  {
    name: "Users, user page, Reset password dialog",
    open: async (page) => {
      const main = await mainPage(page, "/?view=users&id=kim", "kim");
      await main.getByRole("button", { name: "Reset password" }).click();
      const dialog = page.getByRole("dialog", { name: "Reset the password of “kim”?" });
      await expect(dialog.getByRole("button", { name: "Cancel" })).toBeVisible();
      return dialog;
    },
  },

  { name: "Log", open: (page) => mainPage(page, "/?view=log", "Log") },

  // Each tab is a view, opened by its URL. The signal is its one primary.
  ...[
    { name: "Rules", tab: "", signal: "Add rule" },
    { name: "Legal holds", tab: "&tab=holds", signal: "Add hold" },
    { name: "Deletion runs", tab: "&tab=runs", signal: "New deletion run" },
  ].map(({ name, tab, signal }) => ({
    name: `Retention, ${name}`,
    exactly: signal,
    open: (page: Page) => tabByURL(page, `/?view=retention${tab}`, "Retention", name, (panel) => panel.getByRole("button", { name: signal })),
  })),
  { name: "Retention, Add rule", exactly: "Add rule", open: (page) => mainPage(page, "/?view=retention&add=rule", "Add rule") },
  { name: "Retention, Add hold", exactly: "Add hold", open: (page) => mainPage(page, "/?view=retention&tab=holds&add=hold", "Add hold") },
  {
    // The demo's rule, from its row.
    name: "Retention, rule page",
    open: async (page) => {
      const main = await mainPage(page, "/?view=retention", "Retention");
      const link = main.getByRole("row").filter({ hasText: "§147 AO" }).getByRole("rowheader").getByRole("link");
      const what = ((await link.textContent()) ?? "").replace(" (removed)", "");
      await link.click();
      return ready(page, what);
    },
  },
  {
    name: "Retention, rule page, Remove dialog",
    open: async (page) => {
      const main = await mainPage(page, "/?view=retention", "Retention");
      await main.getByRole("row").filter({ hasText: "§147 AO" }).getByRole("rowheader").getByRole("link").click();
      await main.getByRole("button", { name: /^Actions for / }).click();
      await page.getByRole("menuitem", { name: "Remove" }).click();
      const dialog = page.getByRole("dialog", { name: /^Remove the rule for / });
      await expect(dialog.getByRole("button", { name: "Cancel" })).toBeVisible();
      return dialog;
    },
  },
  {
    name: "Retention, hold page",
    open: async (page) => {
      const name = await addHold(page);
      return mainPage(page, `/?view=retention&tab=holds&id=${holdId}`, name);
    },
    cleanup: releaseHold,
  },
  {
    name: "Retention, hold page, Release dialog",
    open: async (page) => {
      const name = await addHold(page);
      const main = await mainPage(page, `/?view=retention&tab=holds&id=${holdId}`, name);
      await main.getByRole("button", { name: `Actions for ${name}` }).click();
      await page.getByRole("menuitem", { name: "Release" }).click();
      const dialog = page.getByRole("dialog", { name: `Release the hold “${name}”?` });
      await expect(dialog.getByLabel("Why is it released?")).toBeVisible();
      return dialog;
    },
    cleanup: releaseHold,
  },
  // A run's page: Delete is a danger button, not a primary.
  { name: "Retention, run page", open: runPage, cleanup: cancelRun },
  {
    name: "Retention, run page, Delete dialog",
    open: async (page) => {
      const main = await runPage(page);
      await main.getByRole("button", { name: /^Delete \d+ messages$/ }).click();
      const dialog = page.getByRole("dialog");
      await expect(dialog.getByLabel(/^To delete, type the number of messages/)).toBeFocused();
      return dialog;
    },
    cleanup: cancelRun,
  },

  // The 2FA setup steps are not reachable with the demo admin (2FA is on);
  // Profile.test.tsx covers them.
  {
    name: "Profile, Display",
    open: (page) => tabPanel(page, "/?view=profile", "Profile", "Display", (panel) => panel.getByLabel("Language", { exact: true })),
  },
  {
    name: "Profile, Sign-in & security",
    open: (page) => tabPanel(page, "/?view=profile", "Profile", "Sign-in & security", (panel) => panel.getByRole("heading", { name: "Passkeys" })),
  },
  {
    name: "Profile, API keys",
    open: (page) => tabPanel(page, "/?view=profile", "Profile", "API keys", (panel) => panel.getByRole("button").first()),
  },
];

function tooMany(name: string, found: string[]): string {
  return `view "${name}" has ${found.length} primary buttons: ${found.map((n) => `“${n}”`).join(", ")}`;
}

for (const view of views) {
  test(`at most one primary button: ${view.name}`, async ({ page }) => {
    try {
      const scope = await view.open(page);
      if (view.exactly) await expect(scope.locator(primary).filter({ visible: true })).toHaveCount(1);
      const found = await primaries(scope);
      expect(found.length, tooMany(view.name, found)).toBeLessThanOrEqual(1);
      if (view.exactly) expect(found, `the primary button of "${view.name}"`).toEqual([view.exactly]);
    } finally {
      await view.cleanup?.(page);
    }
  });
}

// The login page is a view too, without a session. Its probe of
// GET /api/session answers 401 by design, as login.spec.ts allows.
test.describe("logged out", () => {
  test.use({ storageState: { cookies: [], origins: [] } });

  test("at most one primary button: Login", async ({ page, guards }) => {
    guards.allowHTTPError("/api/session", 401);
    await page.goto("/");
    const main = page.getByRole("main");
    await expect(main.getByRole("heading", { name: "Mail Archive", level: 1 })).toBeVisible();
    await expect(main.getByRole("button", { name: "Log in" })).toBeVisible();
    const found = await primaries(main);
    expect(found.length, tooMany("Login", found)).toBeLessThanOrEqual(1);
  });
});
