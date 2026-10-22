// End-to-end tests of the web UI against the demo server (tools/demo):
// made-up mail, a temporary database, no network. Run with `make web-e2e`.
import { defineConfig, devices } from "@playwright/test";
import { adminState } from "./e2e/global-setup";
import { imapPort, origin, port } from "./e2e/demo";

const db = process.env.DEMO_DATABASE_URL;
if (!db) throw new Error("set DEMO_DATABASE_URL to a PostgreSQL URL of a user with CREATEDB");

export default defineConfig({
  testDir: "e2e",
  // One demo server with shared state: one test at a time.
  workers: 1,
  fullyParallel: false,
  // No retries: a retry of the 2FA login would reuse its TOTP time step,
  // which the server rejects. A flaky test is fixed, not retried.
  retries: 0,
  forbidOnly: !!process.env.CI,
  timeout: 30_000,
  expect: { timeout: 10_000 },
  reporter: process.env.CI ? [["list"], ["github"]] : "list",
  globalSetup: "./e2e/global-setup.ts",
  use: {
    baseURL: origin,
    storageState: adminState,
    locale: "en-US",
    timezoneId: "Europe/Berlin",
    actionTimeout: 10_000,
    trace: "retain-on-failure",
  },
  projects: [
    {
      name: "chromium",
      use: {
        ...devices["Desktop Chrome"],
        launchOptions: { executablePath: process.env.CHROMIUM_PATH || undefined },
      },
    },
  ],
  webServer: {
    // The demo embeds internal/web/ui/dist when it is built, so the UI is
    // built first. A fresh demo per run: the tests change its data.
    command:
      `pnpm build && cd .. && go build -o bin/demo ./tools/demo && ` +
      `exec bin/demo -db "$DEMO_DATABASE_URL" -listen 127.0.0.1:${port} -imap-listen 127.0.0.1:${imapPort}`,
    url: `${origin}/healthz`,
    reuseExistingServer: false,
    timeout: 180_000,
    stdout: "pipe",
    // SIGTERM lets the demo drop its database and data directory.
    gracefulShutdown: { signal: "SIGTERM", timeout: 10_000 },
    env: { DEMO_DATABASE_URL: db },
  },
});
