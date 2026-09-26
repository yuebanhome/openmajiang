import { defineConfig, devices } from "@playwright/test";
export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  workers: 1,
  timeout: 180_000,
  expect: { timeout: 15_000 },
  retries: 0,
  maxFailures: process.env.CI ? 1 : undefined,
  reporter: [["list"], ["html", { open: "never" }]],
  use: {
    actionTimeout: 15_000,
    navigationTimeout: 30_000,
    baseURL: process.env.PUBLIC_BASE_URL || "http://127.0.0.1:8080",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    video: "retain-on-failure",
  },
  projects: [
    { name: "chromium-desktop", use: { ...devices["Desktop Chrome"] } },
    { name: "chromium-mobile", use: { ...devices["Pixel 7"] } },
    { name: "webkit-mobile-simulation", use: { ...devices["iPhone 13"] } },
  ],
});
