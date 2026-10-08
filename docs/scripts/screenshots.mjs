// Takes the screenshots in docs/public/screenshots from the demo server
// (go run ./tools/demo). Run through `make docs-screenshots`.
import { createHmac } from "node:crypto";
import { mkdir } from "node:fs/promises";
import { chromium } from "playwright-core";

const base = process.env.DEMO_URL ?? "http://localhost:18080";
const out = new URL("../public/screenshots/", import.meta.url).pathname;
await mkdir(out, { recursive: true });

const browser = await chromium.launch({ executablePath: process.env.CHROMIUM_PATH || undefined });
const errors = [];
const desktop = { width: 1280, height: 800 };

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
  const ctx = await browser.newContext();
  const res = await ctx.request.post(base + "/api/session", { data: login, headers: { Origin: base } });
  if (!res.ok()) throw new Error(`demo login failed: ${res.status()}`);
  const { challenge } = await res.json();
  const second = await ctx.request.post(base + "/api/session/2fa", { data: { challenge, code: totp() }, headers: { Origin: base } });
  if (!second.ok()) throw new Error(`demo second factor failed: ${second.status()}`);
  session = await ctx.storageState();
  await ctx.close();
  return session;
}

async function page(colorScheme, viewport = desktop, { loggedIn = true } = {}) {
  const storageState = loggedIn ? await loginOnce() : undefined;
  const ctx = await browser.newContext({ locale: "en-US", timezoneId: "Europe/Berlin", colorScheme, viewport, deviceScaleFactor: 2, storageState });
  const p = await ctx.newPage();
  p.on("pageerror", (e) => errors.push(String(e)));
  return p;
}

{
  const p = await page("light", { width: 1280, height: 720 }, { loggedIn: false });
  await p.goto(base + "/");
  await p.getByLabel("User name").fill(login.username);
  await p.getByRole("button", { name: "Log in" }).waitFor();
  await p.screenshot({ path: `${out}/login.png` });
  await p.context().close();
}

async function openMessage(p, subject) {
  await p.goto(base + "/");
  await p.getByRole("button", { name: new RegExp(subject) }).first().click();
  await p.getByRole("heading", { name: new RegExp(subject) }).waitFor();
  await p.waitForTimeout(500); // let the HTML frame render
}

for (const scheme of ["light", "dark"]) {
  const p = await page(scheme);
  await openMessage(p, "Photos from the weekend");
  await p.screenshot({ path: `${out}/mail-${scheme}.png` });
  await p.context().close();
}

{
  const p = await page("light");
  await p.goto(base + "/");
  // Mail to the tax advisor from the last month, with words from the body.
  const since = new Date(Date.now() - 30 * 86400000).toISOString().slice(0, 10);
  await p.getByRole("searchbox").fill(`to:taxadvisor after:${since} receipts`);
  await p.waitForFunction(() => document.querySelectorAll('[aria-label="Messages"] mark').length > 0);
  await p.getByRole("button", { name: /Documents for the tax return/ }).click();
  await p.getByRole("heading", { name: /Documents for the tax return/ }).waitFor();
  await p.screenshot({ path: `${out}/search.png` });
  await p.context().close();
}

{
  const p = await page("light");
  await p.goto(base + "/?view=accounts");
  await p.getByText(/Last sync/).first().waitFor();
  await p.screenshot({ path: `${out}/accounts.png` });

  await p.getByRole("button", { name: "Edit" }).first().click();
  await p.getByRole("button", { name: "Choose folders" }).click();
  await p.getByRole("checkbox").first().waitFor();
  await p.screenshot({ path: `${out}/account-edit.png` });
  await p.context().close();
}

{
  const p = await page("light");
  await p.goto(base + "/?view=users");
  await p.getByText("sam").first().waitFor();
  await p.screenshot({ path: `${out}/users.png` });
  await p.goto(base + "/?view=profile");
  await p.getByRole("heading", { name: "Profile" }).waitFor();
  await p.screenshot({ path: `${out}/profile.png` });
  await p.context().close();
}

{
  const p = await page("light", { width: 390, height: 780 });
  await openMessage(p, "Photos from the weekend");
  await p.screenshot({ path: `${out}/mail-phone.png` });
  await p.context().close();
}

await browser.close();
if (errors.length) {
  console.error(errors.join("\n"));
  process.exit(1);
}
console.log(`screenshots written to ${out}`);
