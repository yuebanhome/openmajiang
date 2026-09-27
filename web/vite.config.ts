import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
export default defineConfig({
  plugins: [react()],
  // Keep fonts and other assets same-origin under the production CSP.
  build: { assetsInlineLimit: 0 },
  server: { proxy: { "/v1": { target: "http://127.0.0.1:8080", ws: true } } },
  test: {
    include: ["src/**/*.test.{ts,tsx}"],
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test-setup.ts"],
  },
});
