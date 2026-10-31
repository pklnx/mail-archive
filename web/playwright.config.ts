// End-to-end tests of the web UI against the demo server (tools/demo):
// made-up mail, a temporary database, no network. Run with `make web-e2e`.
// E2E_BROWSER picks the engine: chromium (the default) runs every spec;
// webkit runs only frame-links.spec.ts until #398 (`pnpm e2e:webkit`, #459).
import { defineConfig, devices } from "@playwright/test";
import { adminState } from "./e2e/global-setup";
import { imapPort, origin, port } from "./e2e/demo";

const db = process.env.DEMO_DATABASE_URL;
if (!db) throw new Error("set DEMO_DATABASE_URL to a PostgreSQL URL of a user with CREATEDB");
const browser = process.env.E2E_BROWSER || "chromium";
if (browser !== "chromium" && browser !== "webkit") throw new Error(`E2E_BROWSER must be chromium or webkit, not ${JSON.stringify(browser)}`);

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
    // No DOM snapshots: Playwright's DOM snapshotter runs script in every
    // frame of the page, and in a sandboxed frame without allow-scripts (the
    // mail frame) the browser blocks that and logs "Blocked script execution",
    // which the guards rightly fail on (#260). The rest of the trace stays.
    trace: { mode: "retain-on-failure", snapshots: { dom: false, aria: true, screen: true } },
  },
  projects: [
    browser === "chromium"
      ? {
          name: "chromium",
          use: {
            ...devices["Desktop Chrome"],
            launchOptions: { executablePath: process.env.CHROMIUM_PATH || undefined },
          },
        }
      : {
          name: "webkit",
          // One spec until #398 runs the suite in WebKit.
          testMatch: /frame-links\.spec\.ts$/,
          use: {
            ...devices["Desktop Safari"],
            // No screencast frames either (#398 review): only the trace's
            // actions, network and console.
            trace: { mode: "retain-on-failure", snapshots: { dom: false, aria: true, screen: false } },
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
