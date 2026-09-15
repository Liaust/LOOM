import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: ".",
  testMatch: "mini-dashboard.spec.ts",
  outputDir: "test-results",
  fullyParallel: false,
  workers: 1,
  reporter: "line",
  use: {
    baseURL: "http://127.0.0.1:4173",
    viewport: { width: 1024, height: 600 },
    deviceScaleFactor: 1,
    colorScheme: "dark",
    locale: "en-GB",
    screenshot: "off",
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"], viewport: { width: 1024, height: 600 }, deviceScaleFactor: 1 },
    },
  ],
  webServer: {
    command: "node server.mjs",
    url: "http://127.0.0.1:4173/",
    reuseExistingServer: false,
    timeout: 15_000,
  },
});
