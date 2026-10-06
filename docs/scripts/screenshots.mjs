// Takes the screenshots in docs/public/screenshots from the demo server
// (go run ./tools/demo). Run through `make docs-screenshots`.
import { mkdir } from "node:fs/promises";
import { chromium } from "playwright-core";

const base = process.env.DEMO_URL ?? "http://127.0.0.1:18080";
const out = new URL("../public/screenshots/", import.meta.url).pathname;
await mkdir(out, { recursive: true });

const browser = await chromium.launch({ executablePath: process.env.CHROMIUM_PATH || undefined });
const errors = [];
const desktop = { width: 1280, height: 800 };

// The demo login from tools/demo.
const login = { username: "demo", password: "demo-password" };

async function page(colorScheme, viewport = desktop, { loggedIn = true } = {}) {
  const ctx = await browser.newContext({ locale: "en-US", timezoneId: "Europe/Berlin", colorScheme, viewport, deviceScaleFactor: 2 });
  if (loggedIn) {
    const res = await ctx.request.post(base + "/api/session", { data: login, headers: { Origin: base } });
    if (!res.ok()) throw new Error(`demo login failed: ${res.status()}`);
  }
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
  await p.getByRole("searchbox").fill("invoice");
  await p.waitForFunction(() => document.querySelectorAll('[aria-label="Messages"] mark').length > 0);
  await p.getByRole("button", { name: /City Utilities/ }).click();
  await p.getByRole("heading", { name: /Your invoice for October/ }).waitFor();
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
