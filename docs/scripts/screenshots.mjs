// Takes the screenshots in docs/public/screenshots from the demo server
// (go run ./tools/demo). Run through `make docs-screenshots`.
// Locators use roles and labels, as the UI's end-to-end tests do; the
// script waits for elements and frame load events, never for a time.
import { createHmac } from "node:crypto";
import { mkdir } from "node:fs/promises";
import { chromium } from "playwright-core";

const base = process.env.DEMO_URL ?? "http://localhost:18080";
const out = new URL("../public/screenshots/", import.meta.url).pathname;
await mkdir(out, { recursive: true });

const browser = await chromium.launch({ executablePath: process.env.CHROMIUM_PATH || undefined });
const errors = [];
const desktop = { width: 1280, height: 800 };
const phoneSize = { width: 390, height: 780 };

// The fixed clock (scripts/docs-screenshots.sh): the demo dated its mail from
// DOCS_NOW, and the browser's clock stands 10 minutes after it.
const minute = 60000;
const bucket = 10 * minute;
const docsNow = Date.parse(process.env.DOCS_NOW ?? "");
if (Number.isNaN(docsNow)) throw new Error("set DOCS_NOW (run through make docs-screenshots)");
const browserNow = docsNow + bucket;
// The moment the demo was ready, in Unix seconds; without it, now.
const ready = process.env.DOCS_READY ? Number(process.env.DOCS_READY) * 1000 : Date.now();

// The times the server records during this run (syncs and the next one,
// logins, received times, the log) follow the real clock. In API answers,
// each RFC 3339 time from an hour before the demo was ready to two days
// after moves to DOCS_NOW plus its distance from that moment, rounded to
// 10 minutes. That keeps their order and hides how long the run took. The
// mail's own dates (at or before DOCS_NOW) and dates years ahead, such as
// retention dates, are outside that window and stay as they are.
const timestamp = /"(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2}))"/g;
function pinTimes(json) {
  return json.replace(timestamp, (whole, value) => {
    const t = Date.parse(value);
    if (Number.isNaN(t) || t < ready - 60 * minute || t > ready + 2 * 24 * 60 * minute) return whole;
    return JSON.stringify(new Date(docsNow + Math.round((t - ready) / bucket) * bucket).toISOString());
  });
}

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
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = "";
  for (const c of totpSecret) bits += alphabet.indexOf(c).toString(2).padStart(5, "0");
  const key = Buffer.from(bits.match(/.{8}/g).map((b) => parseInt(b, 2)));
  const msg = Buffer.alloc(8);
  msg.writeBigUInt64BE(BigInt(step));
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

// phone: a touch screen, so the UI shows its touch sizes (pointer: coarse).
async function page(colorScheme, viewport = desktop, { loggedIn = true, phone = false } = {}) {
  const storageState = loggedIn ? await loginOnce() : undefined;
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
    return route.fulfill({ response, body: pinTimes(await response.text()) });
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

for (const scheme of ["light", "dark"]) {
  const p = await page(scheme);
  await openMessage(p, "Photos from the weekend");
  await shot(p, `mail-${scheme}`);
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
  await main.getByText(/Last sync/).first().waitFor();
  await shot(p, "accounts");

  // Deleting the removed account "old-provider" completely: the preview
  // and the typed-count confirmation in a danger Modal. Nothing is confirmed.
  await main.getByRole("button", { name: /^Removed accounts/ }).click();
  await main.getByRole("button", { name: "Delete completely…" }).click();
  const purge = p.getByRole("dialog", { name: /Delete “old-provider” completely\?/ });
  await purge.getByLabel(/^Type \d+ to confirm$/).waitFor();
  await shot(p, "account-purge", { keepFocus: true });
  await purge.getByRole("button", { name: "Cancel" }).click();
  await purge.waitFor({ state: "detached" });
  await p.context().close();
}

{
  // Editing the IMAP account "personal": its own page, with the folders. A
  // taller window shows the whole page, from its heading to the folders.
  const p = await page("light", { width: 1280, height: 1100 });
  await p.goto(base + "/?view=accounts&edit=personal");
  const main = p.getByRole("main");
  await main.getByRole("heading", { name: "Edit “personal”" }).waitFor();
  await main.getByRole("button", { name: "Choose folders" }).click();
  await main.getByRole("checkbox").first().waitFor();
  await main.getByRole("button", { name: "Save folder selection" }).waitFor();
  await shot(p, "account-edit");
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

  await p.goto(base + "/?view=retention");
  await main.getByRole("tab", { name: "Rules", selected: true }).waitFor();
  await main.getByText("§147 AO: business letters and invoices").waitFor();
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
  await p.context().close();
}

{
  // The log: the demo's own login and the messages opened above.
  const p = await page("light");
  await p.goto(base + "/?view=log");
  await p.getByRole("main").getByText("Opened message").first().waitFor();
  await shot(p, "log");
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

await browser.close();
if (errors.length) {
  console.error(errors.join("\n"));
  process.exit(1);
}
console.log(`screenshots written to ${out}`);
