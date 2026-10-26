import { defineConfig } from "@playwright/test";

const nativeFixture = process.env.URMAIL_E2E_NATIVE_FIXTURE === "1";

export default defineConfig({
  testDir: "./e2e",
  timeout: 30_000,
  fullyParallel: false,
  reporter: "list",
  use: {
    baseURL:
      process.env.URMAIL_E2E_BASE_URL ||
      (nativeFixture ? "http://127.0.0.1:5187" : "https://127.0.0.1"),
    ignoreHTTPSErrors: true,
  },
  webServer: nativeFixture
    ? {
        command: "npm run dev -- --host 127.0.0.1 --port 5187 --strictPort",
        url: "http://127.0.0.1:5187",
        reuseExistingServer: !process.env.CI,
      }
    : undefined,
});
