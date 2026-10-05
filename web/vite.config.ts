import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// The build is embedded into the Go binary (internal/web/ui).
export default defineConfig({
  plugins: [react(), tailwindcss()],
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
  },
});
