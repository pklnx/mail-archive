// Takes the screenshots in docs/public/screenshots from the demo server
// (go run ./tools/demo). Run through `make docs-screenshots`.
// Locators use roles and labels, as the UI's end-to-end tests do; the
// script waits for elements and frame load events, never for a time.
import { createHmac } from "node:crypto";
import { mkdir } from "node:fs/promises";
import { chromium } from "playwright-core";
import { bucket, describeUnpinned, minute, pinTimes } from "./pin-times.mjs";

const base = process.env.DEMO_URL ?? "http://localhost:18080";
// The demo's second, new instance (tools/demo -setup-listen, #396) and its
// made-up setup token (demoSetupToken in tools/demo).
const setupBase = process.env.DEMO_SETUP_URL ?? "http://localhost:18088";
const setupToken = "demo-setup-".repeat(4);
// DOCS_SCREENSHOTS_DIR sets the output folder; by default it is the docs' own.
const out = process.env.DOCS_SCREENSHOTS_DIR || new URL("../public/screenshots/", import.meta.url).pathname;
await mkdir(out, { recursive: true });

const browser = await chromium.launch({ executablePath: process.env.CHROMIUM_PATH || undefined });
const errors = [];
const desktop = { width: 1280, height: 800 };
const phoneSize = { width: 390, height: 780 };

// The fixed clock (scripts/docs-screenshots.sh): the demo dated its mail from
// DOCS_NOW, and the browser's clock stands 10 minutes after it.
const docsNow = Date.parse(process.env.DOCS_NOW ?? "");
if (Number.isNaN(docsNow)) throw new Error("set DOCS_NOW (run through make docs-screenshots)");
const browserNow = docsNow + bucket;
// The moment the demo was ready, in Unix seconds; without it, now.
const ready = process.env.DOCS_READY ? Number(process.env.DOCS_READY) * 1000 : Date.now();

// The times the server records during this run follow the real clock; the
// answers' times are moved next to DOCS_NOW (pin-times.mjs). A time after
// DOCS_NOW that stays unmoved fails the run at the end.
const unpinned = [];
// The run must end well inside half a 10-minute bucket after the demo was
// ready, or the rounded times would move into the next bucket.
const runLimit = 4 * minute;

// One set of context options for every context, the login's included: a
// fixed user agent, so the log's Client column does not follow the
// installed Chromium.
const contextOptions = {
  locale: "en-US",
  timezoneId: "Europe/Berlin",
  userAgent: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36",
};

// The demo login from tools/demo. Admins need 2FA, so the demo admin has a
// fixed TOTP secret.
const login = { username: "demo", password: "demo-password" };
const totpSecret = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP";

// totp computes an RFC 6238 code, one time step ahead: the demo used the
// current step to set up 2FA, and each step is accepted only once.
let step = Math.floor(Date.now() / 30000);
function totp() {
  step++;
  return totpAt(totpSecret, step);
}

/** The RFC 6238 code of a base32 secret for a time step. */
function totpAt(secret, at) {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = "";
  for (const c of secret) bits += alphabet.indexOf(c).toString(2).padStart(5, "0");
  const key = Buffer.from(bits.match(/.{8}/g).map((b) => parseInt(b, 2)));
  const msg = Buffer.alloc(8);
  msg.writeBigUInt64BE(BigInt(at));
  const mac = createHmac("sha1", key).update(msg).digest();
  const off = mac[mac.length - 1] & 0xf;
  return String((mac.readUInt32BE(off) & 0x7fffffff) % 1000000).padStart(6, "0");
}

// Log in once and share the session cookie: each TOTP step counts once.
let session;
async function loginOnce() {
  if (session) return session;
  const ctx = await browser.newContext(contextOptions);
  const res = await ctx.request.post(base + "/api/session", { data: login, headers: { Origin: base } });
  if (!res.ok()) throw new Error(`demo login failed: ${res.status()}`);
  const { challenge } = await res.json();
  const second = await ctx.request.post(base + "/api/session/2fa", { data: { challenge, code: totp() }, headers: { Origin: base } });
  if (!second.ok()) throw new Error(`demo second factor failed: ${second.status()}`);
  session = await ctx.storageState();
  await ctx.close();
  return session;
}

// pat from tools/demo: owns the account team and has the admin's personal
// shared with them (#389). No 2FA, so a plain login.
const shareOwner = { username: "pat", password: "pat-password" };
async function loginAs(user) {
  const ctx = await browser.newContext(contextOptions);
  const res = await ctx.request.post(base + "/api/session", { data: user, headers: { Origin: base } });
  if (!res.ok()) throw new Error(`login of ${user.username} failed: ${res.status()}`);
  const state = await ctx.storageState();
  await ctx.close();
  return state;
}

// The demo auditor (#393, tools/demo): 2FA with a secret of its own, set up
// with a code from an hour ago, so the current time step is unused. One
// active grant, which the login picks.
const auditor = { username: "stb-meyer", password: "stb-meyer-password", secret: "GEZDGNBVGEZDGNBVGEZDGNBVGEZDGNBV" };
async function loginAuditor() {
  const ctx = await browser.newContext(contextOptions);
  const res = await ctx.request.post(base + "/api/session", { data: { username: auditor.username, password: auditor.password }, headers: { Origin: base } });
  if (!res.ok()) throw new Error(`auditor login failed: ${res.status()}`);
  const { challenge } = await res.json();
  const code = totpAt(auditor.secret, Math.floor(Date.now() / 30000));
  const second = await ctx.request.post(base + "/api/session/2fa", { data: { challenge, code }, headers: { Origin: base } });
  if (!second.ok()) throw new Error(`auditor second factor failed: ${second.status()}`);
  const state = await ctx.storageState();
  await ctx.close();
  return state;
}

// phone: a touch screen, so the UI shows its touch sizes (pointer: coarse).
// as: another user's login instead of the demo admin's; state: a stored session.
async function page(colorScheme, viewport = desktop, { loggedIn = true, phone = false, as, state } = {}) {
  const storageState = state ?? (as ? await loginAs(as) : loggedIn ? await loginOnce() : undefined);
  const ctx = await browser.newContext({
    ...contextOptions,
    colorScheme,
    viewport,
    deviceScaleFactor: 2,
    storageState,
    ...(phone && { isMobile: true, hasTouch: true }),
  });
  // Date stands still; timers keep running (Carbon's animations, debounces).
  await ctx.clock.setFixedTime(browserNow);
  await ctx.route(base + "/api/**", async (route) => {
    const response = await route.fetch();
    if (!(response.headers()["content-type"] ?? "").includes("application/json")) return route.fulfill({ response });
    const result = pinTimes(await response.text(), { path: new URL(route.request().url()).pathname, docsNow, ready });
    unpinned.push(...result.unpinned);
    return route.fulfill({ response, body: result.body });
  });
  const p = await ctx.newPage();
  p.on("pageerror", (e) => errors.push(String(e)));
  return p;
}

/**
 * A screenshot without the mouse over a control, with Carbon's animations
 * finished. keepFocus leaves the focus where it is (an open menu or Modal
 * needs it); otherwise the focus ring of a clicked tab or button goes.
 */
async function shot(p, name, { keepFocus = false } = {}) {
  await p.mouse.move(0, 0);
  if (!keepFocus) await p.evaluate(() => document.activeElement instanceof HTMLElement && document.activeElement.blur());
  await p.screenshot({ path: `${out}/${name}.png`, animations: "disabled" });
}

/** Waits until the open message's body is shown: the HTML frame has loaded, or the text is there. */
async function bodyShown(p) {
  const body = p.locator("main").locator("iframe, .message__text").first();
  await body.waitFor();
  const frame = await (await body.elementHandle()).contentFrame();
  if (frame) await frame.waitForLoadState("load");
}

/** Opens the message header's Details (#203). */
async function openDetails(p) {
  const details = p.getByRole("article").getByRole("button", { name: "Details" });
  if ((await details.getAttribute("aria-expanded")) !== "true") await details.click();
}

async function openMessage(p, subject, path = "/") {
  await p.goto(base + path);
  await p.getByRole("list", { name: "Messages" }).getByRole("button", { name: new RegExp(subject) }).first().click();
  await p.getByRole("main").getByRole("heading", { name: new RegExp(subject) }).waitFor();
  await bodyShown(p);
}

{
  const p = await page("light", { width: 1280, height: 720 }, { loggedIn: false });
  await p.goto(base + "/");
  await p.getByLabel("User name").fill(login.username);
  await p.getByRole("button", { name: "Log in" }).waitFor();
  await shot(p, "login");
  await p.context().close();
}

// The page of the setup link (#396), on the new instance: the form, with
// the user name typed in. Nothing is sent.
{
  const ctx = await browser.newContext({ ...contextOptions, colorScheme: "light", viewport: { width: 1280, height: 720 }, deviceScaleFactor: 2 });
  await ctx.clock.setFixedTime(browserNow);
  const p = await ctx.newPage();
  p.on("pageerror", (e) => errors.push(String(e)));
  await p.goto(`${setupBase}/#setup=${setupToken}`);
  await p.getByLabel("User name").fill("patrick");
  await p.getByRole("button", { name: "Create admin" }).waitFor();
  await shot(p, "setup");
  await ctx.close();
}

for (const scheme of ["light", "dark"]) {
  const p = await page(scheme);
  await openMessage(p, "Photos from the weekend");
  await shot(p, `mail-${scheme}`);
  await p.context().close();
}

{
  // The user actions as icons in the header bar (#488), with Profile's
  // tooltip open: the focus on it through the keyboard, so no hover position.
  const p = await page("light");
  await openMessage(p, "Photos from the weekend");
  await p.getByRole("banner").getByRole("link", { name: "Mail Archive" }).focus();
  await p.keyboard.press("Tab");
  await p.getByRole("tooltip", { name: `Profile (${login.username})`, exact: true }).waitFor();
  await shot(p, "header-actions", { keepFocus: true });
  await p.context().close();
}

{
  // The user panel on a phone, opened from the person icon (#284): an icon beside each entry (#488).
  const p = await page("light", phoneSize, { phone: true });
  await p.goto(base + "/");
  await p.getByRole("list", { name: "Messages" }).getByRole("button").first().waitFor();
  await p.getByRole("banner").getByRole("button", { name: `User (${login.username})`, exact: true }).tap();
  await p.getByRole("list", { name: `User (${login.username})` }).getByRole("button", { name: "Log out" }).waitFor();
  await shot(p, "user-panel");
  await p.context().close();
}

{
  const p = await page("light");
  await p.goto(base + "/");
  // Mail to the tax advisor from the last month, with words from the body.
  const since = new Date(docsNow - 30 * 86400000).toISOString().slice(0, 10);
  await p.getByRole("searchbox", { name: "Search mail" }).fill(`to:taxadvisor after:${since} receipts`);
  const list = p.getByRole("list", { name: "Messages" });
  await list.locator("mark").first().waitFor();
  await list.getByRole("button", { name: /Documents for the tax return/ }).click();
  await p.getByRole("main").getByRole("heading", { name: /Documents for the tax return/ }).waitFor();
  await bodyShown(p);
  await shot(p, "search");
  await p.context().close();
}

{
  // A word only in an attachment (#390): the row names the PDF before the snippet from its text.
  const p = await page("light");
  await p.goto(base + "/");
  await p.getByRole("searchbox", { name: "Search mail" }).fill("deposit");
  const list = p.getByRole("list", { name: "Messages" });
  await list.locator(".mail-row__match").first().waitFor();
  await list.getByRole("button", { name: /Photos from the weekend and the lease/ }).click();
  await p.getByRole("main").getByRole("heading", { name: /Photos from the weekend and the lease/ }).waitFor();
  await bodyShown(p);
  await shot(p, "search-attachment");
  await p.context().close();
}

{
  // The filters over the list (#286): a recipient and the preset Last 30 days.
  const p = await page("light");
  await p.goto(base + "/?q=to%3Ataxadvisor");
  await p.getByRole("list", { name: "Messages" }).getByRole("button").first().waitFor();
  await p.getByRole("button", { name: "Filters", exact: true }).click();
  const filters = p.getByRole("group", { name: "Filters", exact: true });
  await filters.getByText("Last 30 days", { exact: true }).click();
  await p.waitForURL(/[?&]q=to%3Ataxadvisor(%20|\+)after%3A/);
  await p.waitForFunction(() => document.querySelector('.search-filters input[id$="-after"]')?.value !== "");
  // The focus stays in the filters: a blur to nowhere would close them.
  await shot(p, "search-filters", { keepFocus: true });
  await p.context().close();
}

{
  // The last message of a conversation, with the conversation open over the body.
  const p = await page("light");
  await openMessage(p, "Re: Quote for the kitchen renovation");
  await p.getByRole("list", { name: "Messages" }).evaluate((el) => el.closest(".mail-pane__fill")?.scrollTo(0, 0));
  await p.getByRole("article").getByRole("button", { name: /^Conversation \(\d+\)$/ }).click();
  await p.getByRole("dialog", { name: "Conversation" }).waitFor();
  await shot(p, "conversation", { keepFocus: true });
  await p.context().close();
}

{
  // The list grouped by conversation, with one conversation expanded.
  const p = await page("light");
  await p.goto(base + "/?group=1");
  await p.getByRole("button", { name: /Show 2 earlier messages/ }).click();
  const hide = p.getByRole("button", { name: "Hide the messages" });
  await hide.waitFor();
  // Start the list at the conversation. Only the list scrolls: scrollIntoView
  // would move the page under the header too.
  await hide.evaluate((el) => {
    const row = el.closest("li");
    let box = row.parentElement;
    while (box && !(box.scrollHeight > box.clientHeight && getComputedStyle(box).overflowY !== "visible")) box = box.parentElement;
    // The date heading stays at the top while the list scrolls: start below it.
    const heading = box.querySelector(".mail-list__group");
    const covered = heading ? heading.getBoundingClientRect().height : 0;
    box.scrollTop += row.getBoundingClientRect().top - box.getBoundingClientRect().top - covered;
  });
  await shot(p, "grouped");
  await p.context().close();
}

{
  // Only in archive: a message deleted on the server, open in the reader.
  const p = await page("light");
  await openMessage(p, "premium invoice", "/?gone=1");
  await openDetails(p);
  await p.getByText(/no longer on the server/).first().waitFor();
  await shot(p, "only-in-archive");
  await p.context().close();
}

{
  // A copy from the mail server: the envelope shows the BCC recipient.
  const p = await page("light");
  await openMessage(p, "Lease renewal for the office");
  await openDetails(p);
  const envelope = p.getByRole("button", { name: /Envelope \(journal\)/ });
  if ((await envelope.getAttribute("aria-expanded")) !== "true") await envelope.click();
  await p.getByText(/counsel@lawfirm\.example/).first().waitFor();
  await shot(p, "envelope");
  await p.context().close();
}

{
  const p = await page("light");
  await p.goto(base + "/?view=accounts");
  const main = p.getByRole("main");
  await main.getByRole("cell", { name: /^Last sync/ }).first().waitFor();
  await shot(p, "accounts");

  // Deleting the removed account "old-provider" completely, from its row's
  // menu: the preview and the typed-count confirmation in a danger Modal.
  // Nothing is confirmed.
  await main.getByRole("button", { name: /^Removed accounts/ }).click();
  await main.getByRole("button", { name: "Actions for old-provider" }).click();
  await p.getByRole("menuitem", { name: "Delete completely…" }).click();
  const purge = p.getByRole("dialog", { name: /Delete “old-provider” completely\?/ });
  await purge.getByLabel(/^Type \d+ to confirm$/).waitFor();
  await shot(p, "account-purge", { keepFocus: true });
  await purge.getByRole("button", { name: "Cancel" }).click();
  await purge.waitFor({ state: "detached" });
  await p.context().close();
}

{
  // The page of the IMAP account "personal": the title row, the tiles and
  // the sections, with Folders open for editing. A taller window shows the
  // whole page, from its heading to the folders.
  const p = await page("light", { width: 1280, height: 1300 });
  await p.goto(base + "/?view=accounts&id=personal");
  const main = p.getByRole("main");
  await main.getByRole("heading", { name: "personal", level: 1 }).waitFor();
  const folders = main.getByRole("region", { name: "Folders" });
  await folders.getByRole("button", { name: "Edit" }).click();
  await folders.getByRole("checkbox").first().waitFor();
  await folders.getByRole("button", { name: "Save folder selection" }).waitFor();
  await shot(p, "account-detail");
  await p.context().close();
}

{
  // The page of the smtp account "journal" (#395): Connection, its SMTP
  // secrets with the demo's one secret, and the Mail server setup with the
  // login lines. A taller window shows the setup's lines.
  const p = await page("light", { width: 1280, height: 1500 });
  await p.goto(base + "/?view=accounts&id=journal");
  const main = p.getByRole("main");
  await main.getByRole("heading", { name: "journal", level: 1 }).waitFor();
  await main.getByRole("region", { name: "SMTP secrets" }).getByRole("cell", { name: "mas_…x7qm", exact: true }).waitFor();
  await main.getByRole("region", { name: "Mail server setup" }).getByText(/smtp-\d+:mas_…/).waitFor();
  await shot(p, "account-smtp");
  await p.context().close();
}

{
  // pat's Accounts page: the own account team, and under it the account the
  // demo admin shared with pat (#389).
  const p = await page("light", desktop, { as: shareOwner });
  await p.goto(base + "/?view=accounts");
  const main = p.getByRole("main");
  await main.getByRole("table", { name: "Shared with you" }).getByRole("link", { name: "personal" }).waitFor();
  await main.getByRole("cell", { name: /^Last sync/ }).first().waitFor();
  await shot(p, "accounts-shared");
  await p.context().close();
}

{
  // The import of earlier mail from IMAP into the smtp account "journal":
  // server, login and cut-off; everything goes into its folder JOURNAL.
  const p = await page("light");
  await p.goto(base + "/?view=accounts&import=journal");
  const main = p.getByRole("main");
  await main.getByRole("heading", { name: "Import existing mail from IMAP", level: 1 }).waitFor();
  await main.getByLabel("IMAP server").fill("imap.example.com");
  await main.getByLabel("Username").fill("alex@example.com");
  await main.getByLabel("Cut-off (optional)").waitFor();
  await shot(p, "fill-form");
  await p.context().close();
}

{
  // A message the import copied into JOURNAL: no envelope, so the origin
  // row says so and names the mailbox it came from.
  const p = await page("light");
  await openMessage(p, "Invoice 2026-10");
  await openDetails(p);
  const origin = p.getByRole("button", { name: /Origin \(journal\)/ });
  if ((await origin.getAttribute("aria-expanded")) !== "true") await origin.click();
  await p.getByText(/Imported from IMAP: alex\.morgan@work\.example/).waitFor();
  await p.getByText("journal / JOURNAL").waitFor();
  await shot(p, "fill-origin");
  await p.context().close();
}

{
  const p = await page("light");
  await p.goto(base + "/?view=users");
  const main = p.getByRole("main");
  // The actions of a user are in its menu; open it for kim.
  await main.getByRole("button", { name: "Actions for kim" }).click();
  await p.getByRole("menuitem", { name: "Reset password" }).waitFor();
  await shot(p, "users", { keepFocus: true });
  await p.keyboard.press("Escape");

  // kim's page: role and state, Reset password and the menu, the tiles and the API keys.
  await p.goto(base + "/?view=users&id=kim");
  await main.getByRole("heading", { name: "kim", level: 1 }).waitFor();
  await main.getByRole("region", { name: "API keys" }).waitFor();
  await shot(p, "user-detail");

  // The SMTP secrets of every smtp account under the users table (#395).
  await p.goto(base + "/?view=users");
  const secrets = main.getByRole("region", { name: "SMTP secrets" });
  await secrets.getByRole("rowheader", { name: "journal" }).waitFor();
  await secrets.scrollIntoViewIfNeeded();
  await shot(p, "user-smtp-secrets");

  // The customer groups under the users table (#470): the demo has two.
  await p.goto(base + "/?view=users");
  const groups = main.getByRole("region", { name: "Groups" });
  await groups.getByRole("rowheader", { name: "Lindqvist Design" }).waitFor();
  await groups.scrollIntoViewIfNeeded();
  await shot(p, "user-groups");

  // The Storage page (#397, part 4): the instance quota of 1 GiB and the
  // second group's 512 MiB from the demo.
  await p.goto(base + "/?view=storage");
  await main.getByRole("heading", { name: "Storage", level: 1 }).waitFor();
  await main.getByRole("table", { name: "Customer groups" }).getByRole("rowheader", { name: "Lindqvist Design" }).waitFor();
  await shot(p, "storage");

  // The rules: the table with Add rule above it.
  await p.goto(base + "/?view=retention");
  await main.getByRole("tab", { name: "Rules", selected: true }).waitFor();
  await main.getByRole("cell", { name: "§147 AO: business letters and invoices" }).waitFor();
  await shot(p, "retention");

  await p.goto(base + "/?view=profile");
  await main.getByRole("heading", { name: "Profile", level: 1 }).waitFor();
  await main.getByRole("combobox", { name: "Language" }).waitFor();
  await shot(p, "profile");
  // The security tab. The panel is taller than the window; the top shows.
  await main.getByRole("tab", { name: "Sign-in & security" }).click();
  const security = main.getByRole("tabpanel", { name: "Sign-in & security" });
  await security.getByText("On", { exact: true }).waitFor();
  await security.getByRole("button", { name: "Add passkey" }).or(security.getByText(/Passkeys (work only|are not set up)/)).first().waitFor();
  await shot(p, "profile-security");
  // The API keys tab.
  await main.getByRole("tab", { name: "API keys" }).click();
  await main.getByRole("tabpanel", { name: "API keys" }).getByRole("button", { name: "Create API key" }).waitFor();
  await shot(p, "profile-api-keys");
  // The notifications tab of an admin (#397, part 5): the demo admin's
  // confirmed address for alert mail.
  await main.getByRole("tab", { name: "Notifications" }).click();
  const notifications = main.getByRole("tabpanel", { name: "Notifications" });
  await notifications.getByText("Alert mail goes to demo@example.com.").waitFor();
  await shot(p, "profile-notifications");
  await p.context().close();
}

{
  // The log: the demo's own login and the messages opened above, with two
  // action groups picked (#376).
  const p = await page("light");
  await p.goto(base + "/?view=log&action=login&action=read");
  await p.getByRole("main").getByRole("combobox", { name: "Action" }).getByText("Logins, Reading").waitFor();
  await p.getByRole("main").getByText("Opened message").first().waitFor();
  await shot(p, "log");
  // The open Action list shows every entry in full (#486), scrolled to its
  // end, where the longest are.
  const main = p.getByRole("main");
  await main.getByRole("combobox", { name: "Action" }).click();
  const last = main.getByRole("listbox").getByRole("option", { name: "Retention and legal holds" });
  await last.waitFor();
  await last.scrollIntoViewIfNeeded();
  await shot(p, "log-action-menu", { keepFocus: true });
  await p.context().close();
}

{
  // A phone: a message on its own screen, and the navigation behind the menu button.
  const p = await page("light", phoneSize, { phone: true });
  await openMessage(p, "Photos from the weekend");
  await shot(p, "mail-phone");
  await p.getByRole("button", { name: "Back to the list" }).click();
  await p.getByRole("button", { name: "Open navigation" }).click();
  await p.getByRole("navigation", { name: "Main navigation" }).getByRole("link", { name: "Profile" }).waitFor();
  await shot(p, "mail-phone-nav", { keepFocus: true });
  await p.context().close();
}

{
  // A phone: the list with the quick filter "Has attachment" selected and a
  // From filter set, its tag next to "Edit filters".
  const p = await page("light", phoneSize, { phone: true });
  await p.goto(base + "/?q=from%3Asam%20has%3Aattachment");
  await p.getByRole("group", { name: "Quick filters" }).getByRole("button", { name: "Has attachment", pressed: true }).waitFor();
  await p.getByRole("list", { name: "Active filters" }).getByRole("button", { name: "Edit filters" }).waitFor();
  await p.getByRole("list", { name: "Messages" }).getByRole("button", { name: /Photos from the weekend/ }).waitFor();
  await shot(p, "mail-phone-filters");
  await p.context().close();
}

{
  // A deletion run's page, with the typed-count confirmation of Delete
  // open; nothing is confirmed. Last, so its rule and run are on no other
  // shot (the log, the message view). A ten-year rule for personal /
  // Archive/2014, whose mail is twelve years old, gives the run its
  // candidates.
  const p = await page("light", { width: 1280, height: 900 });
  const api = async (method, path, data) => {
    const res = await p.request.fetch(base + path, { method, data, headers: { Origin: base, "Content-Type": "application/json" } });
    if (!res.ok()) throw new Error(`${method} ${path}: ${res.status()}`);
    return res.json();
  };
  const { accounts } = await api("GET", "/api/retention/rules");
  const personal = accounts.find((a) => a.name === "personal");
  await api("POST", "/api/retention/rules", { accountId: personal.id, folder: "Archive/2014", years: 10, timeZone: "Europe/Berlin", note: "", force: false });
  const run = await api("POST", "/api/retention/runs");
  await p.goto(base + `/?view=retention&tab=runs&id=${run.id}`);
  const main = p.getByRole("main");
  await main.getByRole("region", { name: "Groups" }).getByRole("cell").first().waitFor();
  await main.getByRole("button", { name: `Delete ${run.candidates} messages` }).click();
  await p.getByRole("dialog").getByLabel(/^To delete, type the number of messages/).waitFor();
  await shot(p, "retention-run", { keepFocus: true });
  await p.context().close();
}

// Auditor access (#393): the owner's page with the demo grants, and the
// auditor's view of the case.
{
  const p = await page("light");
  await p.goto(base + "/?view=auditors");
  const main = p.getByRole("main");
  // The demo names its cases by the years before DOCS_NOW.
  await main.getByRole("row").filter({ hasText: /^Betriebsprüfung / }).getByText("No access yet").waitFor();
  await main.getByRole("row").filter({ hasText: /^Umsatzsteuer-Sonderprüfung / }).getByText("No access yet").waitFor();
  await shot(p, "auditor-access");
  await p.context().close();
}
{
  const p = await page("light", desktop, { state: await loginAuditor() });
  await p.goto(base + "/");
  await p.getByText("Auditor access", { exact: true }).waitFor();
  await p.getByRole("treeitem", { name: "work · demo" }).waitFor();
  await p.getByRole("list", { name: "Messages" }).getByRole("button").first().waitFor();
  await shot(p, "auditor-view");
  await p.context().close();
}

await browser.close();
if (unpinned.length) {
  errors.push(
    `times after DOCS_NOW outside the pinned window (real-clock times that change from run to run; ` +
      `pin them in the window, derive them from DOCS_NOW in the demo, or list a stable field in STABLE_TIMES in pin-times.mjs):\n` +
      describeUnpinned([...new Map(unpinned.map((u) => [`${u.path} ${u.key}=${u.value}`, u])).values()]),
  );
}
if (Date.now() > ready + runLimit) {
  errors.push(`the run ended ${Math.round((Date.now() - ready) / minute)} minutes after the demo was ready; the limit is ${runLimit / minute}`);
}
if (errors.length) {
  console.error(errors.join("\n"));
  process.exit(1);
}
console.log(`screenshots written to ${out}`);
