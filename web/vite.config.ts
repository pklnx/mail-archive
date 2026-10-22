import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { carbonStyles } from "./build/stripRemoteFonts.ts";

// The build is embedded into the Go binary (internal/web/ui).
export default defineConfig({
  // carbonStyles writes src/vendor/carbon.css (Carbon without its CDN fonts)
  // before Tailwind's plugin inlines it into index.css.
  plugins: [carbonStyles(), react(), tailwindcss()],
  build: {
    outDir: "../internal/web/ui/dist",
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    // `pnpm dev` talks to `mail-archive serve` on :8080.
    proxy: { "/api": "http://localhost:8080" },
  },
  test: {
    environment: "jsdom",
    // web/e2e holds the Playwright suite (`pnpm e2e`), not Vitest tests.
    include: ["src/**/*.test.{ts,tsx}", "build/**/*.test.ts"],
    setupFiles: ["src/test/setup.ts"],
  },
});
