// Logs the demo admin in once through the API, as docs/scripts/screenshots.mjs
// does, and saves the session for every test that does not test the login.
// One login for the whole suite: the server accepts each TOTP time step once.
import { mkdir } from "node:fs/promises";
import { dirname } from "node:path";
import { request } from "@playwright/test";
import { admin, origin, timeStep, totp } from "./demo";

export const adminState = new URL("./.auth/admin.json", import.meta.url).pathname;

export default async function globalSetup(): Promise<void> {
  const ctx = await request.newContext({ baseURL: origin, extraHTTPHeaders: { Origin: origin } });
  try {
    const first = await ctx.post("/api/session", { data: { username: admin.name, password: admin.password } });
    if (!first.ok()) throw new Error(`demo login: ${first.status()} ${await first.text()}`);
    const { challenge } = (await first.json()) as { challenge: string };
    // One step ahead: the demo used the current step to set up 2FA.
    const code = totp(admin.totpSecret, timeStep() + 1);
    const second = await ctx.post("/api/session/2fa", { data: { challenge, code } });
    if (!second.ok()) throw new Error(`demo second factor: ${second.status()} ${await second.text()}`);
    await mkdir(dirname(adminState), { recursive: true });
    await ctx.storageState({ path: adminState });
  } finally {
    await ctx.dispose();
  }
}
