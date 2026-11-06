import { expect, test, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

test.skip(
  process.env.URMAIL_E2E_NATIVE_FIXTURE !== "1",
  "Requires the isolated local Vite fixture",
);

interface Handlers {
  getOffsite?: () => unknown;
  onPut?: (body: unknown) => void;
  onTestConnection?: (body: unknown) => void;
  testResponse?: unknown;
  testStatus?: number;
}

/** FE-03 admin fixture: platform admin session + mocked offsite backup API. */
async function setupAdminFixture(page: Page, handlers: Handlers = {}) {
  const catalogue = JSON.parse(
    await readFile(
      resolve(process.cwd(), "../internal/i18n/locales/zh-CN.json"),
      "utf8",
    ),
  ) as Record<string, string>;

  await page.addInitScript(() => {
    localStorage.setItem("urmail.session-token", "fixture-admin-token");
    localStorage.setItem("urmail.csrf-token", "fixture-csrf");
  });

  await page.route("**/*", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;

    if (!path.startsWith("/api/")) return route.continue();

    let payload: unknown = {};

    if (path.startsWith("/api/v1/i18n/")) payload = catalogue;
    else if (path.endsWith("/auth/me"))
      payload = {
        user: {
          id: 1,
          email: "admin@urmail.test",
          display_name: "Platform Admin",
          locale: "zh-CN",
        },
        tenants: [],
        is_admin: true,
        current_tenant_id: null,
      };
    else if (path.endsWith("/platform/overview")) payload = { overview: {} };
    else if (path.endsWith("/platform/tenants")) payload = { tenants: [] };
    else if (path.endsWith("/platform/brands")) payload = { brands: [] };
    else if (path.endsWith("/platform/plans")) payload = { plans: [] };
    else if (path.endsWith("/platform/coupons")) payload = { coupons: [] };
    else if (path.endsWith("/platform/payment-merchants"))
      payload = { merchants: [] };
    else if (path.endsWith("/platform/payment-reviews"))
      payload = { reviews: [] };
    else if (path.endsWith("/platform/refunds")) payload = { refunds: [] };
    else if (path.endsWith("/platform/coupon-reissues"))
      payload = { coupons: [] };
    else if (path.endsWith("/platform/billing-document-profile"))
      payload = { profile: { party: { name: "" }, locale: "zh-CN", tax: {} } };
    else if (path.endsWith("/platform/reminder-policy"))
      payload = { days: [7, 3, 1, 0] };
    else if (path.endsWith("/platform/alerts")) payload = { alerts: [] };
    else if (path.endsWith("/platform/settings")) payload = { settings: {} };
    else if (path.endsWith("/platform/brand-hosts")) payload = { hosts: [] };
    else if (path.endsWith("/platform/audit")) payload = { logs: [] };
    else if (path.endsWith("/platform/hosts")) payload = { hosts: [] };
    else if (path.endsWith("/platform/backup/offsite")) {
      const method = route.request().method();
      if (method === "PUT") {
        const putBody = route.request().postDataJSON() as { type?: string };
        handlers.onPut?.(putBody);
        payload =
          putBody?.type === ""
            ? {
                configured: false,
                enabled: false,
                has_credentials: false,
                credential_summary: {},
              }
            : {
                configured: true,
                enabled: true,
                type: "webdav",
                endpoint: "https://dav.example.test",
                has_credentials: true,
                credential_summary: { username: true, password: true },
              };
      }
      if (method === "GET") {
        payload = handlers.getOffsite
          ? handlers.getOffsite()
          : {
              configured: false,
              enabled: false,
              has_credentials: false,
              credential_summary: {},
            };
      }
    } else if (path.endsWith("/test-connection")) {
      handlers.onTestConnection?.(route.request().postDataJSON());
      payload = handlers.testResponse ?? { ok: true };
    }

    await route.fulfill({
      status: handlers.testStatus ?? 200,
      contentType: "application/json",
      body: JSON.stringify(payload),
    });
  });
}

test.describe("Platform offsite backup admin", () => {
  test("three-protocol form, test-connection, save and clear (webdav)", async ({
    page,
  }) => {
    const testBodies: unknown[] = [];
    const putBodies: unknown[] = [];
    await setupAdminFixture(page, {
      getOffsite: () => ({
        configured: false,
        enabled: false,
        has_credentials: false,
        credential_summary: {},
      }),
      onPut: (body) => {
        putBodies.push(body);
      },
      onTestConnection: (body) => {
        testBodies.push(body);
      },
      testResponse: { ok: true },
    });
    await page.goto("/platform?tab=backup");

    // Empty state is a normal state, not an error.
    await expect(page.getByTestId("backup-empty-state")).toBeVisible();
    await expect(page.getByTestId("backup-type-select")).toBeVisible();

    // Dynamic fields appear per protocol.
    await page.getByTestId("backup-type-select").selectOption("webdav");
    await expect(page.getByTestId("backup-webdav-username")).toBeVisible();
    await expect(page.getByTestId("backup-webdav-password")).toBeVisible();
    await expect(page.getByTestId("backup-bucket")).toHaveCount(0);

    // Local validation blocks save without endpoint/credentials.
    await page.getByTestId("backup-save").click();
    await expect(page.getByTestId("backup-validation-errors")).toBeVisible();

    // Fill the webdav destination and probe it before saving.
    await page.getByTestId("backup-endpoint").fill("https://dav.example.test");
    await page.getByTestId("backup-webdav-username").fill("ops");
    await page.getByTestId("backup-webdav-password").fill("pw");
    await page.getByTestId("backup-test-connection").click();
    await expect(page.getByTestId("backup-test-result")).toContainText(
      "连接测试通过",
    );
    expect(testBodies).toEqual([
      expect.objectContaining({
        type: "webdav",
        credentials: { username: "ops", password: "pw" },
      }),
    ]);

    // Save; PUT replaces the destination per contract.
    await page.getByTestId("backup-save").click();
    await expect(page.getByTestId("backup-save-result")).toBeVisible();
    await expect(page.getByTestId("backup-current-config")).toBeVisible();
    expect(putBodies).toEqual([
      {
        type: "webdav",
        endpoint: "https://dav.example.test",
        enabled: false,
        credentials: { username: "ops", password: "pw" },
      },
    ]);

    // Two-step clear: PUT {"type":""} resets to the explicit disabled state.
    await page.getByTestId("backup-clear").click();
    await page.getByTestId("backup-clear-confirm").click();
    await expect(page.getByTestId("backup-empty-state")).toBeVisible();
  });
});

test.describe("Platform offsite backup masking and error copy", () => {
  test("masks stored credentials, switches protocols, maps contract errors", async ({
    page,
  }) => {
    const handlers = {
      getOffsite: () => ({
        configured: true,
        enabled: true,
        type: "s3",
        endpoint: "https://s3.example.internal:9000",
        bucket: "urmail-backups",
        prefix: "gen",
        age_recipient: "age1example",
        has_credentials: true,
        credential_summary: { access_key: true, secret_key: true },
      }),
      onPut: () => {},
      onTestConnection: () => {},
      testResponse: {
        ok: false,
        error: "destination not configured",
      },
      testStatus: 200,
    };
    await setupAdminFixture(page, handlers);
    await page.goto("/platform?tab=backup");

    // Masking: summary chips shown, no plaintext backfill.
    await expect(page.getByTestId("backup-credentials-summary")).toBeVisible();
    await expect(page.getByTestId("backup-credentials-summary")).toContainText(
      "已保存：access_key",
    );
    const accessKey = page.getByTestId("backup-access-key-id");
    await expect(accessKey).toHaveValue("");

    // Known ok:false detail maps to localised copy (empty body probes the
    // already-saved destination per contract).
    await page.getByTestId("backup-test-connection").click();
    await expect(page.getByTestId("backup-test-result")).toContainText(
      "目的地尚未配置",
    );

    // HTTP 400 INVALID_PARAMS maps to the contract error-code copy.
    handlers.testResponse = { error: { code: "INVALID_PARAMS" } };
    handlers.testStatus = 400;
    await page.getByTestId("backup-test-connection").click();
    await expect(page.getByTestId("backup-test-result")).toContainText(
      "参数不合法（400 INVALID_PARAMS）",
    );

    // Protocol switch keeps the form dynamic under an existing config.
    await page.getByTestId("backup-type-select").selectOption("sftp");
    await expect(page.getByTestId("backup-host-key-fingerprint")).toBeVisible();
    await expect(page.getByTestId("backup-bucket")).toHaveCount(0);
  });
});
