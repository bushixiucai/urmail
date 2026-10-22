import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  timeout: 30_000,
  fullyParallel: false,
  reporter: "list",
  use: {
    baseURL: process.env.URMAIL_E2E_BASE_URL || "https://127.0.0.1",
    ignoreHTTPSErrors: true,
    extraHTTPHeaders: { "Content-Type": "application/json" },
  },
});
